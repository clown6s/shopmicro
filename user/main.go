package main

import (
	"flag"
	"net"
	"shopmicro/user/middleware"
	"time"

	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/server"
	"github.com/kitex-contrib/registry-etcd"
	"go.uber.org/zap"

	v1 "shopmicro/user/kitex_gen/api/user/v1/userservice"

	pkgconf "shopmicro/pkg/conf"
	"shopmicro/pkg/jwt"
	"shopmicro/pkg/logger"
	"shopmicro/user/conf"
	"shopmicro/user/data"
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

	dataProvider, cleanup, err := data.New(cfg.MySQL, cfg.Redis, cfg.Mode)
	if err != nil {
		return err
	}
	defer cleanup()

	r, err := etcd.NewEtcdRegistry(cfg.Etcd.Addrs, etcd.WithDialTimeoutOpt(5*time.Second))
	if err != nil {
		return err
	}

	addr, _ := net.ResolveTCPAddr("tcp", cfg.Server.Addr)

	svr := v1.NewServer(
		newUserHandler(dataProvider, cfg.JWT.Secret, cfg.JWT.Expiration),
		server.WithServerBasicInfo(&rpcinfo.EndpointBasicInfo{ServiceName: "user"}),
		server.WithRegistry(r),
		server.WithServiceAddr(addr),
		server.WithMiddleware(middleware.AuthMiddleware(jwt.NewParser(cfg.JWT.Secret))),
	)

	logger.Infof("user service started on %s", cfg.Server.Addr)
	return svr.Run()
}

func main() {
	configPath := flag.String("c", "conf/config.yaml", "config file path")
	flag.Parse()

	if err := Run(*configPath); err != nil {
		logger.L.Info("run service error", zap.Error(err))
	}
}
