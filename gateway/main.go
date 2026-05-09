package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	hrecovery "github.com/cloudwego/hertz/pkg/app/middlewares/server/recovery"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"

	hertztracing "github.com/hertz-contrib/obs-opentelemetry/tracing"

	"github.com/kitex-contrib/registry-etcd"

	"shopmicro/gateway/conf"
	"shopmicro/gateway/health"
	"shopmicro/gateway/middleware"
	"shopmicro/gateway/proxy"

	pkgconf "shopmicro/pkg/conf"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/otel"
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
	// middleware
	// =========================

	// otel middleware
	h.Use(
		hertztracing.ServerMiddleware(
			traceCfg,
		),
	)

	// official recovery
	h.Use(
		hrecovery.Recovery(),
	)

	// custom panic recovery
	h.Use(
		middleware.RecoveryMiddleware(),
	)

	// cors
	h.Use(
		middleware.CORS(
			&middleware.CORSConfig{
				AllowOrigins: cfg.CORS.AllowOrigins,
			},
		),
	)

	// access log
	h.Use(
		middleware.AccessLogMiddleware(),
	)

	// rate limit (global, including /health is fine)
	h.Use(
		middleware.RateLimitMiddleware(),
	)

	// =========================
	// proxy
	// =========================

	p := proxy.New(

		proxy.WithJWT(
			cfg.JWT.Secret,
		),

		proxy.WithPublicMethods(
			"RegisterUser",
			"Login",
		),
	)

	// discover services
	if err := p.DiscoverAndRegister(
		r,
		cfg.Etcd.Addrs,
		cfg.ProtoBase,
	); err != nil {

		return err
	}

	// watch services
	p.WatchServices(
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

	// =========================
	// api routes
	// =========================

	api := h.Group("/api/v1")

	// P0-5: Circuit breaker only on API routes — /health must remain reachable.
	api.Use(
		middleware.CircuitBreakerMiddleware(),
	)

	api.GET(
		"/:service/:method",
		p.Handler(),
	)

	api.POST(
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

	go func() {

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

		logger.L.Infof(
			"shutdown signal received: %v",
			sig,
		)

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

				logger.L.Warnf(
					"hertz already stopped",
				)

				return
			}

			logger.L.Errorf(
				"hertz shutdown failed: %v",
				err,
			)

			return
		}

		logger.L.Infof(
			"gateway stopped",
		)
	}()

	// =========================
	// start server
	// =========================

	logger.L.Infof(
		"gateway started at %s",
		cfg.Server.Addr,
	)

	if err := h.Run(); err != nil {

		// graceful shutdown
		if strings.Contains(
			err.Error(),
			"engine is not running",
		) {

			logger.L.Warnf(
				"hertz already stopped",
			)

			return nil
		}

		return err
	}

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

		logger.L.Fatalf(
			"gateway run failed: %v",
			err,
		)
	}
}
