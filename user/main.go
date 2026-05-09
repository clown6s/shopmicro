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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/cloudwego/kitex/pkg/endpoint"
	server "github.com/cloudwego/kitex/server"

	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"

	kotelkitex "github.com/kitex-contrib/obs-opentelemetry/tracing"

	"github.com/kitex-contrib/registry-etcd"

	v1 "shopmicro/user/kitex_gen/api/user/v1/userservice"

	pkgconf "shopmicro/pkg/conf"
	"shopmicro/pkg/logger"
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
		cfg.Mode,
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

		server.WithMetaHandler(
			transmeta.MetainfoServerHandler,
		),

		// otel tracing
		server.WithSuite(
			kotelkitex.NewServerSuite(
				kotelkitex.WithEnableGRPCMetadata(),
			),
		),

		// P1-02: Panic recovery — prevent handler panics from crashing the process
		server.WithMiddleware(func(next endpoint.Endpoint) endpoint.Endpoint {
			return func(ctx context.Context, req, resp interface{}) (err error) {
				defer func() {
					if r := recover(); r != nil {
						logger.Errorf("kitex panic recovered: %v", r)
						err = fmt.Errorf("internal error: panic recovered")
					}
				}()
				return next(ctx, req, resp)
			}
		}),
	)

	// =========================
	// P0-07: HTTP health check server (for K8s probes)
	// =========================

	healthy := &atomic.Bool{}
	healthy.Store(true)
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"status":"ok"}`)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"status":"shutting_down"}`)
		}
	})
	healthSrv := &http.Server{
		Addr:    cfg.GetHealthAddr(),
		Handler: healthMux,
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

		logger.Infof(
			"shutdown signal received: %v",
			sig,
		)

		// P0-07: Mark as unhealthy for K8s probes
		healthy.Store(false)

		// P1-09: Use goroutine + hard timeout since svr.Stop() ignores context
		done := make(chan error, 1)
		go func() {
			done <- svr.Stop()
		}()

		select {
		case err := <-done:
			if err != nil && !strings.Contains(err.Error(), "context deadline exceeded") {
				logger.Errorf("kitex shutdown failed: %v", err)
			} else {
				logger.Infof("user service stopped")
			}
		case <-time.After(5 * time.Second):
			logger.Warnf("kitex shutdown timed out after 5s, forcing exit")
		}

		// Shutdown health check server
		healthSrv.Shutdown(context.Background())
	}()

	// =========================
	// start server
	// =========================

	logger.Infof(
		"user service started on %s (health: %s)",
		cfg.Server.Addr,
		cfg.GetHealthAddr(),
	)

	if err := svr.Run(); err != nil {

		if strings.Contains(
			err.Error(),
			"context deadline exceeded",
		) {

			logger.Warnf(
				"kitex already stopped",
			)

			wg.Wait()
			return nil
		}

		wg.Wait()
		return err
	}

	wg.Wait()
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

		// logger may not be initialized yet (e.g. config load/validate failed)
		log.Fatalf("run user service failed: %v", err)
	}
}
