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

	// P0-01 fix: 使用配置文件中的 OTEL endpoint
	shutdownTracer := otel.InitTracerWithEndpoint(
		"shopmicro-gateway",
		cfg.Mode,
		cfg.OTEL.Endpoint,
	)
	defer shutdownTracer(context.Background())

	// =========================
	// etcd resolver
	// =========================

	r, err := etcd.NewEtcdResolver(cfg.Etcd.Addrs)
	if err != nil {
		return err
	}

	// Etcd client for health check (readiness probe)
	// R4-P3-05: Unified DialTimeout (5s) — same as proxy.newEtcdClient
	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   cfg.Etcd.Addrs,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("create etcd client for health: %w", err)
	}
	defer etcdClient.Close()

	// =========================
	// hertz
	// =========================

	hlog.SetLevel(hlog.LevelWarn)

	// hertz otel tracing
	tracer, traceCfg := hertztracing.NewServerTracer()

	h := server.New(
		tracer,

		server.WithHostPorts(cfg.Server.Addr),

		server.WithMaxRequestBodySize(1<<20), // 1MB

		// P2-SEC-03 fix: 添加服务器超时配置防止慢客户端攻击
		server.WithReadTimeout(10*time.Second),
		server.WithWriteTimeout(30*time.Second),
		server.WithIdleTimeout(60*time.Second),
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

	h.Use(hertztracing.ServerMiddleware(traceCfg)) // otel root span + trace context
	h.Use(middleware.RecoveryMiddleware(cfg.Mode)) // panic recovery
	h.Use(middleware.CORS(&middleware.CORSConfig{  // cors
		AllowOrigins:     cfg.CORS.AllowOrigins,
		AllowCredentials: cfg.CORS.AllowCredentials,
	}, cfg.Mode))
	h.Use(middleware.SecurityHeadersMiddleware(cfg.Mode)) // HSTS, CSP, X-Frame-Options…
	h.Use(middleware.AccessLogMiddleware())               // access log
	h.Use(middleware.TraceIDMiddleware())                 // trace-id response header

	// distributed rate limit (Redis sliding window, per-IP)
	rl := setupRateLimiter(rdb, cfg)
	h.Use(middleware.RateLimitMiddleware(rl))

	// =========================
	// proxy
	// =========================

	cbInterval, cbTimeout := cfg.CircuitBreakerDurations()

	p := proxy.New(

		proxy.WithJWT(cfg.JWT.Secret, cfg.JWT.Issuer),

		proxy.WithRedis(rdb),

		proxy.WithPublicMethods(
			"RegisterUser",
			"Login",
			"RefreshToken",
		),

		proxy.WithAllowedMethods("user",
			"RegisterUser",
			"Login",
			"RefreshToken",
			"Logout",
			"UpdateUser",
			"DeleteUser",
			"ListUser",
		),

		// district service method whitelist
		proxy.WithAllowedMethods("district",
			"GetProvinceTree",
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
	if err := p.DiscoverAndRegister(r, cfg.Etcd.Addrs, cfg.ProtoBase); err != nil {
		return err
	}

	// watch services — lifecycle context; cancel immediately after
	// shutdown signal so the watch goroutine exits during graceful shutdown,
	// not sometime after Run() returns.
	watchCtx, watchCancel := context.WithCancel(context.Background())
	p.WatchServices(watchCtx, r, cfg.Etcd.Addrs, cfg.ProtoBase)

	// =========================
	// routes
	// =========================

	registerRoutes(h, p, rdb, etcdClient)

	// =========================
	// graceful shutdown
	// =========================

	// P1-13: Use WaitGroup to ensure main() blocks until shutdown completes
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()

		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

		sig := <-quit
		logger.Infof("shutdown signal received: %v", sig)

		// P2-05: cancel watchCtx so WatchServices goroutine exits promptly.
		watchCancel()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := h.Shutdown(ctx); err != nil {
			if isHertzStopped(err) {
				logger.Warnf("hertz already stopped")
				return
			}
			logger.Errorf("hertz shutdown failed: %v", err)
			return
		}

		logger.Infof("gateway stopped")
	}()

	// =========================
	// start server
	// =========================

	logger.Infof("gateway started at %s", cfg.Server.Addr)

	if err := h.Run(); err != nil {
		if isHertzStopped(err) {
			logger.Warnf("hertz already stopped")
			wg.Wait()
			return nil
		}
		wg.Wait()
		return err
	}

	wg.Wait()
	return nil
}

// setupRateLimiter 根据配置初始化限流器（含 per-endpoint 覆盖）。
func setupRateLimiter(rdb *redis.Client, cfg conf.Config) *middleware.RedisRateLimiter {
	rlWindow := time.Duration(cfg.RateLimit.WindowS) * time.Second
	rl := middleware.NewRedisRateLimiter(rdb, cfg.RateLimit.Limit, rlWindow)

	// P1-15: Configure per-endpoint rate limits for sensitive operations
	for epKey, epCfg := range cfg.RateLimit.Endpoints {
		rl.SetEndpointLimit(epKey, epCfg.Limit, epCfg.WindowS)
	}

	return rl
}

// registerRoutes 注册健康检查端点和业务 API 路由。
func registerRoutes(h *server.Hertz, p *proxy.Proxy, rdb *redis.Client, etcdClient *clientv3.Client) {
	// P2-SEC-06 fix: 健康检查端点返回最小信息，不泄露内部拓扑
	h.GET("/health", health.Handler())

	// P0-07: Readiness probe for K8s — checks Redis + Etcd connectivity
	// 注意：/ready 端点在生产环境应通过 K8s 网络策略限制访问
	h.GET("/ready", health.ReadyHandler(rdb, etcdClient))

	// P1-15: 业务路由 — per-endpoint rate limit 在 proxy.Handler() 内统一执行
	api := h.Group("/api/v1")
	handler := p.Handler()
	api.POST("/:service/:method", handler)
	api.GET("/:service/:method", handler)
	api.PUT("/:service/:method", handler)
	api.DELETE("/:service/:method", handler)
}

// isHertzStopped 判断 hertz 返回的 err 是否属于"引擎已停止"的情况
// （常见于 GoLand Stop 按钮触发的正常停止）。
func isHertzStopped(err error) bool {
	return strings.Contains(err.Error(), "engine is not running")
}

func main() {

	configPath := flag.String(
		"c",
		"conf/config.yaml",
		"config file path",
	)

	flag.Parse()

	if err := Run(*configPath); err != nil {
		logger.Fatalf("gateway run failed: %v", err)
	}
}
