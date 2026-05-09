package main

import (
	"context"
	"flag"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	server "github.com/cloudwego/kitex/server"

	"github.com/cloudwego/kitex/pkg/rpcinfo"

	kotelkitex "github.com/kitex-contrib/obs-opentelemetry/tracing"

	"github.com/kitex-contrib/registry-etcd"

	"go.uber.org/zap"

	v1 "shopmicro/user/kitex_gen/api/user/v1/userservice"

	pkgconf "shopmicro/pkg/conf"
	"shopmicro/pkg/jwt"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/otel"

	"shopmicro/user/conf"
	"shopmicro/user/data"
	"shopmicro/user/handler"
	"shopmicro/user/middleware"
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

	logger.Init(
		cfg.Mode,
	)

	defer logger.L.Sync()

	// =========================
	// otel tracer
	// =========================

	shutdownTracer := otel.InitTracer(
		"user-service",
	)

	defer shutdownTracer(
		context.Background(),
	)

	// =========================
	// data
	// =========================

	dataProvider, cleanup, err := data.New(
		cfg.MySQL,
		cfg.Redis,
		cfg.Mode,
	)

	if err != nil {
		return err
	}

	defer cleanup()

	// =========================
	// etcd registry
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
	// service addr
	// =========================

	addr, err := net.ResolveTCPAddr(
		"tcp",
		cfg.Server.Addr,
	)

	if err != nil {
		return err
	}

	// =========================
	// kitex server
	// =========================

	svr := v1.NewServer(

		handler.NewUserHandler(
			dataProvider,
			cfg.JWT.Secret,
			cfg.JWT.Expiration,
		),

		server.WithServerBasicInfo(
			&rpcinfo.EndpointBasicInfo{
				ServiceName: "user",
			},
		),

		server.WithRegistry(
			r,
		),

		server.WithServiceAddr(
			addr,
		),

		// auth middleware
		server.WithMiddleware(
			middleware.AuthMiddleware(
				jwt.NewParser(
					cfg.JWT.Secret,
				),
			),
		),

		// otel tracing
		server.WithSuite(
			kotelkitex.NewServerSuite(
				kotelkitex.WithEnableGRPCMetadata(),
			),
		),
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

		if err := svr.Stop(); err != nil {

			// Windows / GoLand
			if strings.Contains(
				err.Error(),
				"context deadline exceeded",
			) {

				logger.L.Warnf(
					"kitex shutdown timeout",
				)

				return
			}

			logger.L.Errorf(
				"kitex shutdown failed: %v",
				err,
			)

			return
		}

		_ = ctx

		logger.L.Infof(
			"user service stopped",
		)
	}()

	// =========================
	// start server
	// =========================

	logger.L.Infof(
		"user service started on %s",
		cfg.Server.Addr,
	)

	if err := svr.Run(); err != nil {

		if strings.Contains(
			err.Error(),
			"context deadline exceeded",
		) {

			logger.L.Warnf(
				"kitex already stopped",
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

		logger.L.Fatal(
			"run user service failed",
			zap.Error(err),
		)
	}
}
