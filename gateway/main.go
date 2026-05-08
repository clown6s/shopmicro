package main

import (
	"flag"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/cloudwego/kitex/client"
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

	logger.Init(cfg.Mode)
	defer logger.Sync()

	r, err := etcd.NewEtcdResolver(cfg.Etcd.Addrs)
	if err != nil {
		return err
	}

	hlog.SetLevel(hlog.LevelWarn)
	h := server.New(server.WithHostPorts(cfg.Server.Addr))
	h.Use(middleware.CORS())

	// 初始化通用 gRPC 代理
	p := proxy.New(
		proxy.WithJWT(cfg.JWT.Secret),
		proxy.WithPublicMethods("RegisterUser", "Login"),
	)

	// 注册下游服务，新增服务只需加一行
	if err := p.RegisterService("user", "../user/api/user.proto",
		client.WithResolver(r),
		client.WithHostPorts("127.0.0.1:8888"),
	); err != nil {
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
