package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"

	hertztracing "github.com/hertz-contrib/obs-opentelemetry/tracing"

	"github.com/kitex-contrib/registry-etcd"

	clientv3 "go.etcd.io/etcd/client/v3"

	"shopmicro/gateway/conf"
	"shopmicro/gateway/health"
	"shopmicro/gateway/middleware"
	"shopmicro/gateway/proxy"

	pkgconf "shopmicro/pkg/conf"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/otel"
	"shopmicro/pkg/redis"
)

func Run(configPath string) error {

	var cfg conf.Config

	if err := pkgconf.Load(
		configPath,
		&cfg,
	); err != nil {

		return err
	}

	cfg.ApplyEnv()

	// Fill zero-value config fields with defaults.
	cfg.SetDefaults()

	if err := cfg.Validate(); err != nil {
		return err
	}

	// =========================
	// logger
	// =========================

	logger.Init(cfg.Mode)

	defer logger.L.Sync()

	// =========================
	// otel tracer
	// =========================

	shutdownTracer := otel.InitTracer(
		"shopmicro-gateway",
		cfg.Mode,
	)

	defer shutdownTracer(
		context.Background(),
	)

	// =========================
	// etcd resolver
	// =========================

	r, err := etcd.NewEtcdResolver(
		cfg.Etcd.Addrs,
	)

	if err != nil {
		return err
	}

	// Etcd client for health check (readiness probe)
	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   cfg.Etcd.Addrs,
		DialTimeout: 3 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("create etcd client for health: %w", err)
	}
	defer etcdClient.Close()

	// =========================
	// hertz
	// =========================

	hlog.SetLevel(
		hlog.LevelWarn,
	)

	// hertz otel tracing
	tracer, traceCfg := hertztracing.NewServerTracer()

	h := server.New(
		tracer,

		server.WithHostPorts(
			cfg.Server.Addr,
		),

		server.WithMaxRequestBodySize(
			1<<20, // 1MB
		),
	)

	// =========================
	// redis (for distributed rate limiting)
	// =========================

	rdb, redisCleanup, err := redis.New(cfg.Redis)
	if err != nil {
		return err
	}
	defer redisCleanup()

	// =========================
	// middleware
	// =========================

	// otel middleware (creates root span + injects trace context)
	h.Use(
		hertztracing.ServerMiddleware(
			traceCfg,
		),
	)

	// panic recovery — custom version logs stack trace
	h.Use(
		middleware.RecoveryMiddleware(),
	)

	// cors
	h.Use(
		middleware.CORS(
			&middleware.CORSConfig{
				AllowOrigins:     cfg.CORS.AllowOrigins,
				AllowCredentials: cfg.CORS.AllowCredentials,
			},
			cfg.Mode,
		),
	)

	// security headers (HSTS, CSP, X-Frame-Options, etc.)
	h.Use(
		middleware.SecurityHeadersMiddleware(cfg.Mode),
	)

	// access log
	h.Use(
		middleware.AccessLogMiddleware(),
	)

	// trace-id header (reads from hertztracing span, no duplicate span)
	h.Use(
		middleware.TraceIDMiddleware(),
	)

	// distributed rate limit (Redis sliding window, per-IP)
	rlWindow := time.Duration(cfg.RateLimit.WindowS) * time.Second
	rl := middleware.NewRedisRateLimiter(rdb, cfg.RateLimit.Limit, rlWindow)

	// P1-15: Configure per-endpoint rate limits for sensitive operations
	for epKey, epCfg := range cfg.RateLimit.Endpoints {
		rl.SetEndpointLimit(epKey, epCfg.Limit, epCfg.WindowS)
	}

	h.Use(
		middleware.RateLimitMiddleware(rl),
	)

	// =========================
	// proxy
	// =========================

	cbInterval, cbTimeout := cfg.CircuitBreakerDurations()

	p := proxy.New(

		proxy.WithJWT(
			cfg.JWT.Secret,
		),

		proxy.WithRedis(rdb),

		proxy.WithPublicMethods(
			"RegisterUser",
			"Login",
			"RefreshToken",
		),

		// P1-12: Explicit method whitelist for user service
		proxy.WithAllowedMethods("user",
			"RegisterUser",
			"Login",
			"RefreshToken",
			"Logout",
			"UpdateUser",
			"DeleteUser",
			"ListUser",
		),

		// Circuit breaker wraps RPC calls inside proxy.Call(),
		// not HTTP handlers — eliminates double-response bug.
		proxy.WithCircuitBreaker(
			cfg.CircuitBreaker.MaxRequests,
			cbInterval,
			cbTimeout,
			uint32(cfg.CircuitBreaker.ConsecutiveFailures),
		),

		// Per-endpoint rate limit enforced in proxy handler (method-independent).
		// Prevents GET bypass of Login's strict rate limit.
		proxy.WithEndpointRateLimit(rl.NewEndpointCheckFn()),
	)

	// discover services
	if err := p.DiscoverAndRegister(
		r,
		cfg.Etcd.Addrs,
		cfg.ProtoBase,
	); err != nil {

		return err
	}

	// watch services — lifecycle context; cancel immediately after
	// shutdown signal so the watch goroutine exits during graceful shutdown,
	// not sometime after Run() returns.
	watchCtx, watchCancel := context.WithCancel(context.Background())

	p.WatchServices(
		watchCtx,
		r,
		cfg.Etcd.Addrs,
		cfg.ProtoBase,
	)

	// =========================
	// health check
	// =========================

	h.GET(
		"/health",
		health.Handler(),
	)

	// P0-07: Readiness probe for K8s — checks Redis + Etcd connectivity
	h.GET(
		"/ready",
		health.ReadyHandler(rdb, etcdClient),
	)

	// =========================
	// api routes
	// =========================

	api := h.Group("/api/v1")

	// P1-15: Login endpoint — per-endpoint rate limit is enforced in
	// proxy.Handler() via WithEndpointRateLimit, so no additional middleware
	// is needed here. The generic routes below handle all methods uniformly.
	api.POST(
		"/:service/:method",
		p.Handler(),
	)

	api.GET(
		"/:service/:method",
		p.Handler(),
	)

	api.PUT(
		"/:service/:method",
		p.Handler(),
	)

	api.DELETE(
		"/:service/:method",
		p.Handler(),
	)

	// =========================
	// graceful shutdown
	// =========================

	// P1-13: Use WaitGroup to ensure main() blocks until shutdown completes
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()

		quit := make(
			chan os.Signal,
			1,
		)

		signal.Notify(
			quit,
			syscall.SIGINT,
			syscall.SIGTERM,
		)

		sig := <-quit

		logger.Infof(
			"shutdown signal received: %v",
			sig,
		)

		// P2-05: cancel watchCtx so WatchServices goroutine exits promptly.
		watchCancel()

		ctx, cancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)

		defer cancel()

		if err := h.Shutdown(
			ctx,
		); err != nil {

			// GoLand stop
			if strings.Contains(
				err.Error(),
				"engine is not running",
			) {

				logger.Warnf(
					"hertz already stopped",
				)

				return
			}

			logger.Errorf(
				"hertz shutdown failed: %v",
				err,
			)

			return
		}

		logger.Infof(
			"gateway stopped",
		)
	}()

	// =========================
	// start server
	// =========================

	logger.Infof(
		"gateway started at %s",
		cfg.Server.Addr,
	)

	if err := h.Run(); err != nil {

		// graceful shutdown
		if strings.Contains(
			err.Error(),
			"engine is not running",
		) {

			logger.Warnf(
				"hertz already stopped",
			)

			wg.Wait()
			return nil
		}

		wg.Wait()
		return err
	}

	wg.Wait()
	return nil
}

func main() {

	configPath := flag.String(
		"c",
		"conf/config.yaml",
		"config file path",
	)

	flag.Parse()

	if err := Run(
		*configPath,
	); err != nil {

		logger.Fatalf(
			"gateway run failed: %v",
			err,
		)
	}
}
