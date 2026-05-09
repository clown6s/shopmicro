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

	"github.com/kitex-contrib/registry-etcd"

	"shopmicro/gateway/conf"
	"shopmicro/gateway/health"
	"shopmicro/gateway/middleware"
	"shopmicro/gateway/proxy"

	pkgconf "shopmicro/pkg/conf"
	"shopmicro/pkg/logger"
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

	shutdownTracer := middleware.InitTracer(
		"shopmicro-gateway",
	)

	defer shutdownTracer(context.Background())

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

	hlog.SetLevel(hlog.LevelWarn)

	h := server.New(
		server.WithHostPorts(cfg.Server.Addr),
		server.WithMaxRequestBodySize(1<<20), // 1MB
	)

	// =========================
	// middleware
	// =========================

	// 官方 recovery
	h.Use(hrecovery.Recovery())

	// 自定义 panic recover
	h.Use(middleware.RecoveryMiddleware())

	// cors
	h.Use(
		middleware.CORS(
			&middleware.CORSConfig{
				AllowOrigins: cfg.CORS.AllowOrigins,
			},
		),
	)

	// trace
	h.Use(
		middleware.TraceMiddleware(),
	)

	// access log
	h.Use(
		middleware.AccessLogMiddleware(),
	)

	// rate limit
	h.Use(
		middleware.RateLimitMiddleware(),
	)

	// circuit breaker
	h.Use(
		middleware.CircuitBreakerMiddleware(),
	)

	// =========================
	// proxy
	// =========================

	p := proxy.New(
		proxy.WithJWT(cfg.JWT.Secret),
		proxy.WithPublicMethods(
			"RegisterUser",
			"Login",
		),
	)

	// 首次发现服务
	if err := p.DiscoverAndRegister(
		r,
		cfg.Etcd.Addrs,
		cfg.ProtoBase,
	); err != nil {

		return err
	}

	// 动态监听服务
	p.WatchServices(
		r,
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

		quit := make(chan os.Signal, 1)

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

		if err := h.Shutdown(ctx); err != nil {

			// GoLand stop 时可能出现
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

		logger.L.Infof("gateway stopped")
	}()

	// =========================
	// start server
	// =========================

	logger.L.Infof(
		"gateway started at %s",
		cfg.Server.Addr,
	)

	// 阻塞运行
	if err := h.Run(); err != nil {

		// shutdown 时可能返回
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

	if err := Run(*configPath); err != nil {

		logger.L.Fatalf(
			"gateway run failed: %v",
			err,
		)
	}
}
