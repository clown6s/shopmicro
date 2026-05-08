package main

import (
	"flag"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/kitex-contrib/registry-etcd"

	"shopmicro/gateway/conf"
	"shopmicro/gateway/middleware"
	"shopmicro/gateway/proxy"
	pkgconf "shopmicro/pkg/conf"
	"shopmicro/pkg/logger"
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

	logger.Init(cfg.Mode)
	defer logger.Sync()

	r, err := etcd.NewEtcdResolver(cfg.Etcd.Addrs)
	if err != nil {
		return err
	}

	hlog.SetLevel(hlog.LevelWarn)
	h := server.New(
		server.WithHostPorts(cfg.Server.Addr),
		server.WithMaxRequestBodySize(1<<20), // 1MB
	)
	h.Use(middleware.CORS(&middleware.CORSConfig{AllowOrigins: cfg.CORS.AllowOrigins}))

	// 初始化通用 gRPC 代理
	p := proxy.New(
		proxy.WithJWT(cfg.JWT.Secret),
		proxy.WithPublicMethods("RegisterUser", "Login"),
	)

	// 自动发现 Etcd 中已注册的服务，新增服务零配置
	if err := p.DiscoverAndRegister(r, cfg.Etcd.Addrs, cfg.ProtoBase); err != nil {
		return err
	}

	// 所有 API 路由通过代理转发: /api/v1/:service/:method
	api := h.Group("/api/v1")
	api.GET("/:service/:method", p.Handler())
	api.POST("/:service/:method", p.Handler())
	api.PUT("/:service/:method", p.Handler())
	api.DELETE("/:service/:method", p.Handler())

	h.Spin()
	return nil
}

func main() {
	configPath := flag.String("c", "conf/config.yaml", "config file path")
	flag.Parse()

	if err := Run(*configPath); err != nil {
		logger.Fatalf("gateway run failed: %v", err)
	}
}
