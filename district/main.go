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
	"strings"
	"sync"
	"syscall"
	"time"

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
	// logger
	// =========================

	logger.Init(cfg.Mode)
	defer logger.L.Sync()

	// =========================
	// otel tracer
	// =========================

	shutdownTracer := otel.InitTracer("district-service", cfg.Mode)
	defer shutdownTracer(context.Background())

	// =========================
	// data
	// =========================

	dataProvider, cleanup, err := data.New(cfg.MySQL, cfg.Redis, cfg.Mode)
	if err != nil {
		return err
	}
	defer cleanup()

	// =========================
	// etcd registry
	// =========================

	r, err := etcd.NewEtcdRegistry(
		cfg.Etcd.Addrs,
		etcd.WithDialTimeoutOpt(5*time.Second),
	)
	if err != nil {
		return err
	}

	// =========================
	// service addr
	// =========================

	addr, err := net.ResolveTCPAddr("tcp", cfg.Server.Addr)
	if err != nil {
		return err
	}

	// =========================
	// kitex server
	// =========================

	svr := v1.NewServer(
		handle.NewDistrictHandler(dataProvider),

		server.WithServerBasicInfo(&rpcinfo.EndpointBasicInfo{
			ServiceName: "district",
		}),

		server.WithRegistry(r),

		server.WithServiceAddr(addr),

		server.WithMetaHandler(transmeta.MetainfoServerHandler),

		// otel tracing
		server.WithSuite(
			kotelkitex.NewServerSuite(
				kotelkitex.WithEnableGRPCMetadata(),
			),
		),
	)

	// =========================
	// HTTP health check server (for K8s probes)
	// =========================

	healthMux := buildHealthMux(dataProvider)
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
	// graceful shutdown
	// =========================

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()

		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

		sig := <-quit
		logger.Infof("shutdown signal received: %v", sig)

		done := make(chan error, 1)
		go func() { done <- svr.Stop() }()

		select {
		case err := <-done:
			if err != nil && !strings.Contains(err.Error(), "context deadline exceeded") {
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
	// start server
	// =========================

	logger.Infof(
		"district service started on %s (health: %s)",
		cfg.Server.Addr,
		cfg.GetHealthAddr(),
	)

	if err := svr.Run(); err != nil {
		if strings.Contains(err.Error(), "context deadline exceeded") {
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
//   - /health  存活探针 — 仅返回进程是否存活，不检查外部依赖
//   - /healthz 就绪探针 — 检查 MySQL + Redis 连通性（内部使用，生产需网络策略限制）
func buildHealthMux(d *data.Data) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		unhealthy := false

		if err := d.DB.PingContext(ctx); err != nil {
			logger.Warnf("healthz: db ping failed: %v", err)
			unhealthy = true
		}

		if !d.Rdb.Ping(ctx) {
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
