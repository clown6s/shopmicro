package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	server "github.com/cloudwego/kitex/server"

	kotelkitex "github.com/kitex-contrib/obs-opentelemetry/tracing"
	"github.com/kitex-contrib/registry-etcd"

	v1 "shopmicro/product/kitex_gen/api/product/v1/productservice"

	"shopmicro/product/conf"
	"shopmicro/product/data"
	"shopmicro/product/handler"
	productMiddleware "shopmicro/product/middleware"

	pkgconf "shopmicro/pkg/conf"
	pkghealth "shopmicro/pkg/health"
	"shopmicro/pkg/jwt"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/otel"
)

func Run(configPath string) error {

	var cfg conf.Config

	if err := pkgconf.Load(configPath, &cfg); err != nil {
		return err
	}

	cfg.ApplyEnv()

	if err := cfg.Validate(); err != nil {
		return err
	}

	// =========================
	// 日志
	// =========================

	if err := logger.Init(cfg.Mode); err != nil {
		return fmt.Errorf("logger init failed: %w", err)
	}
	defer logger.C().Sync()

	// =========================
	// otel 追踪器
	// =========================

	shutdownTracer, err := otel.InitTracer("product-service", cfg.Mode)
	if err != nil {
		return fmt.Errorf("otel init failed: %w", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownTracer(ctx)
	}()

	// =========================
	// 数据层
	// =========================

	dataProvider, cleanup, err := data.New(cfg.MySQL)
	if err != nil {
		return err
	}
	defer cleanup()

	// =========================
	// etcd 注册中心
	// =========================

	r, err := etcd.NewEtcdRegistry(
		cfg.Etcd.Addrs,
		etcd.WithDialTimeoutOpt(5*time.Second),
	)
	if err != nil {
		return err
	}

	// =========================
	// 服务地址
	// =========================

	addr, err := net.ResolveTCPAddr("tcp", cfg.Server.Addr)
	if err != nil {
		return err
	}

	// =========================
	// kitex 服务端
	// =========================

	productHandler, err := handler.NewProductHandler(dataProvider.Category())
	if err != nil {
		return fmt.Errorf("create product handler: %w", err)
	}

	// =========================
	// 鉴权拦截器
	// =========================

	var authMws []endpoint.Middleware
	{
		if cfg.Auth.JWTSecret != "" {
			jwtParser, err := jwt.NewParser(cfg.Auth.JWTSecret, "")
			if err != nil {
				return fmt.Errorf("init jwt parser: %w", err)
			}
			authMws = append(authMws, productMiddleware.UnaryJWTInterceptor(jwtParser))
			logger.Info("product auth: JWT interceptor enabled")
		} else if cfg.Mode == "prod" {
			return fmt.Errorf("auth.jwtSecret is required in production mode")
		}

		if cfg.Auth.InternalToken != "" {
			authMws = append(authMws, productMiddleware.UnaryServiceAuthInterceptor(cfg.Auth.InternalToken))
			logger.Info("product auth: internal token interceptor enabled")
		}
	}

	// 构建中间件链：鉴权 → panicRecovery
	var mws []endpoint.Middleware
	if len(authMws) > 0 {
		mws = append(mws, authMws...)
	}
	mws = append(mws, panicRecoveryMiddleware)

	chainMiddleware := func(next endpoint.Endpoint) endpoint.Endpoint {
		for i := len(mws) - 1; i >= 0; i-- {
			next = mws[i](next)
		}
		return next
	}

	svr := v1.NewServer(
		productHandler,

		server.WithServerBasicInfo(&rpcinfo.EndpointBasicInfo{
			ServiceName: "product",
		}),

		server.WithRegistry(r),

		server.WithServiceAddr(addr),

		server.WithMetaHandler(transmeta.MetainfoServerHandler),

		// otel 链路追踪
		server.WithSuite(
			kotelkitex.NewServerSuite(
				kotelkitex.WithEnableGRPCMetadata(),
			),
		),

		// 鉴权 + panicRecovery
		server.WithMiddleware(chainMiddleware),
	)

	// =========================
	// HTTP 健康检查服务端
	// =========================

	healthy := &atomic.Bool{}
	healthy.Store(true)

	healthMux := pkghealth.BuildHealthMux(pkghealth.Dependencies{DB: dataProvider.DB}, healthy)
	healthSrv := &http.Server{
		Addr:         cfg.GetHealthAddr(),
		Handler:      healthMux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	go func() {
		if err := healthSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warnf("health check server stopped: %v", err)
		}
	}()

	// =========================
	// 优雅关闭
	// =========================

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()

		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

		sig := <-quit
		logger.Infof("shutdown signal received: %v", sig)

		healthy.Store(false)

		done := make(chan error, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Errorf("svr.Stop() panic recovered: %v", r)
					done <- fmt.Errorf("svr.Stop panic: %v", r)
				}
			}()
			done <- svr.Stop()
		}()

		select {
		case err := <-done:
			if err != nil && !isKitexStopped(err) {
				logger.Errorf("kitex shutdown failed: %v", err)
			} else {
				logger.Infof("product service stopped")
			}
		case <-time.After(5 * time.Second):
			logger.Warnf("kitex shutdown timed out after 5s, forcing exit")
		}

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := healthSrv.Shutdown(shutdownCtx); err != nil {
			logger.Warnf("health server shutdown error: %v", err)
		}
		shutdownCancel()
	}()

	// =========================
	// 启动服务
	// =========================

	logger.Infof(
		"product service started on %s (health: %s)",
		cfg.Server.Addr,
		cfg.GetHealthAddr(),
	)

	if err := svr.Run(); err != nil {
		if isKitexStopped(err) {
			logger.Warnf("kitex already stopped")
			wg.Wait()
			return nil
		}
		return err
	}

	wg.Wait()
	return nil
}

// isKitexStopped 检查错误是否表示 Kitex 服务端已经被停止
func isKitexStopped(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// panicRecoveryMiddleware 捕获 kitex handler panic
func panicRecoveryMiddleware(next endpoint.Endpoint) endpoint.Endpoint {
	return func(ctx context.Context, req, resp interface{}) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf("kitex panic recovered: %v", r)
				err = fmt.Errorf("internal error: panic recovered")
			}
		}()
		return next(ctx, req, resp)
	}
}

func main() {

	configPath := flag.String("c", "conf/config.yaml", "config file path")
	flag.Parse()

	if err := Run(*configPath); err != nil {
		log.Fatalf("run product service failed: %v", err)
	}
}
