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
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/cloudwego/kitex/pkg/endpoint"
	server "github.com/cloudwego/kitex/server"

	"github.com/cloudwego/kitex/pkg/registry"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"

	kotelkitex "github.com/kitex-contrib/obs-opentelemetry/tracing"

	"github.com/kitex-contrib/registry-etcd"

	v1 "shopmicro/user/kitex_gen/api/user/v1/userservice"

	pkgconf "shopmicro/pkg/conf"
	pkghealth "shopmicro/pkg/health"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/netutil"
	"shopmicro/pkg/otel"

	"shopmicro/user/conf"
	"shopmicro/user/data"
	"shopmicro/user/handler"
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
	cfg.SetDefaults()

	if err := cfg.Validate(); err != nil {
		return err
	}

	// =========================
	// 日志
	// =========================

	logger.Init(cfg.Mode)
	defer logger.C().Sync()

	// =========================
	// otel 链路追踪
	// =========================

	shutdownTracer, err := otel.InitTracer("user-service", cfg.Mode)
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

	dataProvider, cleanup, err := data.New(
		cfg.MySQL,
		cfg.Redis,
	)

	if err != nil {
		return err
	}

	defer cleanup()

	// =========================
	// etcd 注册中心
	// =========================

	r, err := etcd.NewEtcdRegistry(
		cfg.Etcd.Addrs,

		etcd.WithDialTimeoutOpt(
			5*time.Second,
		),
	)

	if err != nil {
		return err
	}

	// =========================
	// 服务地址
	// =========================

	addr, err := net.ResolveTCPAddr(
		"tcp",
		cfg.Server.Addr,
	)

	if err != nil {
		return err
	}

	// =========================
	// kitex 服务端
	// =========================

	// 创建 user handler
	userHandler, err := handler.NewUserHandler(
		dataProvider,
		cfg.JWT.Secret,
		cfg.JWT.Expiration,
		cfg.JWT.Issuer,
		cfg.JWT.RefreshSecret,
		cfg.Security,
	)
	if err != nil {
		return fmt.Errorf("create user handler failed: %w", err)
	}

	// 获取本机真实 IP 用于服务注册，避免 Kitex 自动检测选到虚拟网卡
	localIP, err := netutil.GetLocalIP()
	if err != nil {
		return fmt.Errorf("get local ip: %w", err)
	}
	registryAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("%s:%d", localIP.String(), addr.Port))
	if err != nil {
		return fmt.Errorf("resolve registry addr for %s:%d: %w", localIP.String(), addr.Port, err)
	}

	svr := v1.NewServer(
		userHandler,

		server.WithServerBasicInfo(
			&rpcinfo.EndpointBasicInfo{
				ServiceName: "user",
			},
		),

		server.WithRegistry(r),

		server.WithRegistryInfo(&registry.Info{
			Addr:           registryAddr,
			SkipListenAddr: true,
		}),

		server.WithServiceAddr(addr),

		server.WithMetaHandler(
			transmeta.MetainfoServerHandler,
		),

		// otel 链路追踪
		server.WithSuite(
			kotelkitex.NewServerSuite(
				kotelkitex.WithEnableGRPCMetadata(),
			),
		),

		// P1-02：Panic 恢复 — 防止 handler panic 导致进程崩溃
		server.WithMiddleware(panicRecoveryMiddleware),
	)

	// =========================
	// P0-07：HTTP 健康检查服务端（用于 K8s 探针）
	// =========================

	healthy := &atomic.Bool{}
	healthy.Store(true)

	healthSrv := &http.Server{
		Addr:         cfg.GetHealthAddr(),
		Handler:      pkghealth.BuildHealthMux(pkghealth.Dependencies{DB: dataProvider.DB, Rdb: dataProvider.Rdb}, healthy),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	// 确保 health server 在 Run 返回后被关闭
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		if err := healthSrv.Shutdown(shutdownCtx); err != nil {
			logger.Warnf("health server shutdown failed: %v", err)
		}
	}()
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

		signal.Notify(
			quit,
			syscall.SIGINT,
			syscall.SIGTERM,
		)

		sig := <-quit

		logger.Infof("shutdown signal received: %v", sig)

		// P0-07：标记为不健康，供 K8s 探针使用
		healthy.Store(false)

		// P1-09：使用 goroutine + 硬超时，因为 svr.Stop() 会忽略 context
		done := make(chan error, 1)
		go func() { done <- svr.Stop() }()

		select {
		case err := <-done:
			if err != nil && !isKitexStopped(err) {
				logger.Errorf("kitex shutdown failed: %v", err)
			} else {
				logger.Infof("user service stopped")
			}
		case <-time.After(5 * time.Second):
			logger.Warnf("kitex shutdown timed out after 5s, forcing exit")
		}
	}()

	// =========================
	// 启动服务
	// =========================

	logger.Infof(
		"user service started on %s (health: %s)",
		cfg.Server.Addr,
		cfg.GetHealthAddr(),
	)

	if err := svr.Run(); err != nil {
		if !isKitexStopped(err) {
			return err
		}
	}

	wg.Wait()
	return nil
}

// isKitexStopped 检查错误是否表示 Kitex 服务端已停止。
func isKitexStopped(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// panicRecoveryMiddleware 捕获 kitex handler panic，防止 panic 导致进程崩溃。
func panicRecoveryMiddleware(next endpoint.Endpoint) endpoint.Endpoint {
	return func(ctx context.Context, req, resp interface{}) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf("kitex panic recovered: %v\n%s", r, debug.Stack())
				err = fmt.Errorf("internal error: panic recovered")
			}
		}()
		return next(ctx, req, resp)
	}
}

func main() {

	configPath := flag.String(
		"c",
		"conf/config.yaml",
		"配置文件路径",
	)

	flag.Parse()

	if err := Run(*configPath); err != nil {
		// 日志可能尚未初始化（例如配置加载/校验失败）
		log.Fatalf("run user service failed: %v", err)
	}
}
