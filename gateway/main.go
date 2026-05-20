package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"

	"github.com/hertz-contrib/gzip"

	hertztracing "github.com/hertz-contrib/obs-opentelemetry/tracing"

	"github.com/kitex-contrib/registry-etcd"

	clientv3 "go.etcd.io/etcd/client/v3"

	"shopmicro/gateway/conf"
	"shopmicro/gateway/health"
	"shopmicro/gateway/middleware"
	"shopmicro/gateway/proxy"
	"shopmicro/gateway/routes"

	pkgconf "shopmicro/pkg/conf"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/otel"
	"shopmicro/pkg/redis"
	"shopmicro/pkg/secrets"
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

	// 为零值配置字段填充默认值
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
	// 可信代理 (XFF)
	// =========================

	middleware.InitTrustedProxies(cfg.TrustedProxies)

	// =========================
	// otel 链路追踪
	// =========================

	// P2-03: 将配置文件中的采样率通过环境变量传递给 otel 初始化
	if cfg.OTEL.SampleRate > 0 {
		os.Setenv("OTEL_TRACE_SAMPLE_RATE", strconv.FormatFloat(cfg.OTEL.SampleRate, 'f', -1, 64))
	}

	shutdownTracer, err := otel.InitTracerWithEndpoint(
		"shopmicro-gateway",
		cfg.Mode,
		cfg.OTEL.Endpoint,
	)
	if err != nil {
		return fmt.Errorf("otel init failed: %w", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownTracer(ctx)
	}()

	// =========================
	// etcd 服务发现
	// =========================

	r, err := etcd.NewEtcdResolver(cfg.Etcd.Addrs)
	if err != nil {
		return err
	}

	// 用于健康检查的就绪探测的 Etcd 客户端
	// R4-P3-05: 统一 DialTimeout（5秒）—— 与 proxy.newEtcdClient 一致
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

	// hertz otel 链路追踪
	tracer, traceCfg := hertztracing.NewServerTracer()

	h := server.New(
		tracer,

		server.WithHostPorts(cfg.Server.Addr),

		server.WithMaxRequestBodySize(cfg.HTTPServer.MaxBodySize*1024),

		// P2-SEC-03 修复：服务器超时配置（从 yaml 读取）
		server.WithReadTimeout(time.Duration(cfg.HTTPServer.ReadTimeout)*time.Second),
		server.WithWriteTimeout(time.Duration(cfg.HTTPServer.WriteTimeout)*time.Second),
		server.WithIdleTimeout(time.Duration(cfg.HTTPServer.IdleTimeout)*time.Second),
	)

	// =========================
	// redis（用于分布式限流）
	// =========================

	rdb, redisCleanup, err := redis.New(cfg.Redis)
	if err != nil {
		return err
	}
	defer redisCleanup()

	// =========================
	// 中间件
	// =========================

	h.Use(hertztracing.ServerMiddleware(traceCfg)) // otel 根 span + 链路上下文
	h.Use(middleware.PrometheusMiddleware())       // P2: Prometheus 指标采集（QPS/延迟/并发）
	h.Use(middleware.RecoveryMiddleware(cfg.Mode)) // panic 恢复
	h.Use(middleware.CORS(&middleware.CORSConfig{  // 跨域
		AllowOrigins:     cfg.CORS.AllowOrigins,
		AllowCredentials: cfg.CORS.AllowCredentials,
		AllowHeaders:     cfg.CORS.AllowHeaders,
		MaxAge:           cfg.CORS.MaxAgeSec,
	}, cfg.Mode))
	h.Use(middleware.SecurityHeadersMiddleware(cfg.Mode)) // HSTS、CSP、X-Frame-Options 等
	h.Use(gzip.Gzip(gzip.DefaultCompression))             // gzip 压缩，减少大响应传输量
	h.Use(middleware.AccessLogMiddleware())               // 访问日志
	h.Use(middleware.TraceIDMiddleware())                 // trace-id 响应头
	h.Use(middleware.RealIPMiddleware())                  // X-Forwarded-For 信任链 → _real_ip

	// 分布式限流（Redis 滑动窗口，按 IP）
	rl := setupRateLimiter(rdb, cfg)
	h.Use(middleware.RateLimitMiddleware(rl))

	// =========================
	// 代理
	// =========================

	// 初始化预压缩缓存
	proxy.InitPreGzip(
		time.Duration(cfg.PreGzip.TTLSec)*time.Second,
		time.Duration(cfg.PreGzip.CleanupInterval)*time.Second,
		cfg.PreGzip.MinSizeKB,
		cfg.PreGzip.MaxEntries,
		cfg.PreGzip.MaxQueryLen,
		cfg.PreGzip.MaxKeyLen,
	)

	// 构建代理选项
	opts := []proxy.Option{
		proxy.WithJWT(cfg.JWT.Secret, cfg.JWT.Issuer),
		proxy.WithRedis(rdb),

		// 按端点限流在代理处理器中执行（与方法无关）。
		// 防止通过 GET 请求绕过 Login 的严格限流。
		proxy.WithEndpointRateLimit(rl.NewEndpointCheckFn()),

		// 熔断器：下游服务连续失败后自动打开，间隔后进入半开状态探测恢复
		proxy.WithCircuitBreaker(proxy.NewCircuitBreaker(
			uint32(cfg.CircuitBreaker.ConsecutiveFailures),
			time.Duration(cfg.CircuitBreaker.IntervalSec)*time.Second,
			time.Duration(cfg.CircuitBreaker.TimeoutSec)*time.Second,
			uint32(cfg.CircuitBreaker.MaxRequests),
		)),

		// RPC 超时与重试配置
		proxy.WithRPCTimeout(time.Duration(cfg.RPC.RPCTimeout) * time.Second),
		proxy.WithConnectTimeout(time.Duration(cfg.RPC.ConnectTimeout) * time.Second),
		proxy.WithMaxRetries(cfg.RPC.MaxRetries),
	}
	// 追加路由相关配置：公开方法、白名单、HTTP 方法映射
	opts = append(opts, routes.ProxyOptions()...)

	p := proxy.New(opts...)

	// 发现服务
	if err := p.DiscoverAndRegister(r, cfg.Etcd.Addrs, cfg.ProtoBase); err != nil {
		return err
	}

	// 监听服务 —— 使用生命周期上下文；在收到关闭信号后立即取消，
	// 以便在优雅关闭期间 WatchServices 协程退出，
	// 而不是在 Run() 返回后才退出。
	watchCtx, watchCancel := context.WithCancel(context.Background())
	p.WatchServices(watchCtx, r, cfg.Etcd.Addrs, cfg.ProtoBase)

	// =========================
	// 路由
	// =========================

	routes.Register(h, p, rdb, etcdClient)

	// =========================
	// 优雅关闭
	// =========================

	// P1-13: 使用 WaitGroup 确保 main() 阻塞直到关闭完成
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()

		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

		sig := <-quit
		logger.Infof("shutdown signal received: %v", sig)

		// P2-05: 取消 watchCtx 以便 WatchServices 协程及时退出。
		watchCancel()

		// R4-P1-01: 停止 pregzip 清理 goroutine，避免泄漏
		proxy.StopPreGzipCleaner()
		// R4-P1-02: 停止 metrics 更新 goroutine，避免泄漏
		health.StopMetricsUpdater()

		// 关闭 Vault provider（清理 HTTP 空闲连接）
		secrets.CloseProvider()

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
	// 启动服务
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
	rlWindow := time.Duration(cfg.RateLimit.WindowSec) * time.Second
	rl := middleware.NewRedisRateLimiter(rdb, cfg.RateLimit.Limit, rlWindow)

	// P1-15: 为敏感操作配置按端点限流
	for epKey, epCfg := range cfg.RateLimit.Endpoints {
		rl.SetEndpointLimit(epKey, epCfg.Limit, epCfg.WindowSec)
	}

	return rl
}

// isHertzStopped 判断 hertz 返回的 err 是否属于"引擎已停止"的情况
func isHertzStopped(err error) bool {
	return strings.Contains(err.Error(), "engine is not running")
}

func main() {

	configPath := flag.String(
		"c",
		"conf/config.yaml",
		"配置文件路径",
	)

	flag.Parse()

	if err := Run(*configPath); err != nil {
		logger.Fatalf("gateway run failed: %v", err)
	}
}
