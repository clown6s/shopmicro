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

	v1 "shopmicro/district/kitex_gen/api/district/v1/districtservice"

	"shopmicro/district/conf"
	"shopmicro/district/data"
	"shopmicro/district/handle"

	pkgconf "shopmicro/pkg/conf"
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
	defer logger.L.Sync()

	// =========================
	// otel 追踪器
	// =========================

	shutdownTracer := otel.InitTracer("district-service", cfg.Mode)
	defer shutdownTracer(context.Background())

	// =========================
	// 数据层
	// =========================

	dataProvider, cleanup, err := data.New(cfg.MySQL, cfg.Redis)
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

	svr := v1.NewServer(
		handle.NewDistrictHandler(dataProvider),

		server.WithServerBasicInfo(&rpcinfo.EndpointBasicInfo{
			ServiceName: "district",
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

		// Panic 恢复 —— 防止 handler panic 导致进程崩溃
		server.WithMiddleware(panicRecoveryMiddleware),
	)

	// =========================
	// HTTP 健康检查服务端（用于 K8s 探针）
	// =========================

	healthy := &atomic.Bool{}
	healthy.Store(true)

	healthMux := buildHealthMux(dataProvider, healthy)
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

		// 标记为不健康，用于 K8s 探针
		healthy.Store(false)

		done := make(chan error, 1)
		go func() { done <- svr.Stop() }()

		select {
		case err := <-done:
			if err != nil && !isKitexStopped(err) {
				logger.Errorf("kitex shutdown failed: %v", err)
			} else {
				logger.Infof("district service stopped")
			}
		case <-time.After(5 * time.Second):
			logger.Warnf("kitex shutdown timed out after 5s, forcing exit")
		}

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = healthSrv.Shutdown(shutdownCtx)
		shutdownCancel()
	}()

	// =========================
	// 启动服务
	// =========================

	logger.Infof(
		"district service started on %s (health: %s)",
		cfg.Server.Addr,
		cfg.GetHealthAddr(),
	)

	if err := svr.Run(); err != nil {
		if isKitexStopped(err) {
			logger.Warnf("kitex already stopped")
			wg.Wait()
			return nil
		}
		wg.Wait()
		return err
	}

	wg.Wait()
	return nil
}

// buildHealthMux 构建健康检查 HTTP 路由。
//   - /health  存活探针 — 返回最小信息，不泄露内部依赖细节
//   - /healthz 就绪探针 — 检查 MySQL + Redis 连通性（内部使用，生产需网络策略限制）
func buildHealthMux(d *data.Data, healthy *atomic.Bool) *http.ServeMux {
	mux := http.NewServeMux()

	// /health 仅做基础存活检查，不暴露依赖细节
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"status":"degraded"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	// /healthz 用于 K8s readiness probe（内部使用）
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		unhealthy := false

		if d.DB == nil {
			logger.Warn("healthz: db is nil")
			unhealthy = true
		} else if err := d.DB.PingContext(ctx); err != nil {
			logger.Warnf("healthz: db ping failed: %v", err)
			unhealthy = true
		}

		if d.Rdb == nil {
			logger.Warn("healthz: rdb is nil")
			unhealthy = true
		} else if !d.Rdb.Ping(ctx) {
			logger.Warn("healthz: redis ping failed")
			unhealthy = true
		}

		if unhealthy {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"status":"unhealthy"}`)
			return
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	return mux
}

func main() {

	configPath := flag.String("c", "conf/config.yaml", "config file path")
	flag.Parse()

	if err := Run(*configPath); err != nil {
		log.Fatalf("run district service failed: %v", err)
	}
}

// panicRecoveryMiddleware 捕获 kitex handler panic，防止 panic 导致进程崩溃。
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

// isKitexStopped 检查错误是否表示 Kitex 服务端已经被停止
//（例如通过信号处理器调用 svr.Stop()）。
func isKitexStopped(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
