package health

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"shopmicro/pkg/logger"
)

// DBPinger 是数据库健康检查所需的接口。
// database/sql.(*DB) 隐式满足此接口。
type DBPinger interface {
	PingContext(ctx context.Context) error
}

// RedisPinger 是 Redis 健康检查所需的接口。
// shopmicro/pkg/redis.(*Client) 满足此接口。
type RedisPinger interface {
	PingErr(ctx context.Context) error
}

// Dependencies 定义健康检查所需的依赖接口。
// 各服务的 data.Data 都可适配此接口。
type Dependencies struct {
	DB  DBPinger
	Rdb RedisPinger
}

// BuildHealthMux 构建健康检查 HTTP 路由。
//   - /health  存活探针 — 返回最小信息，不泄露内部依赖细节
//   - /healthz 就绪探针 — 检查 MySQL + Redis 连通性（内部使用，生产需网络策略限制）
func BuildHealthMux(deps Dependencies, healthy *atomic.Bool) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"status":"degraded"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		unhealthy := false

		if deps.DB == nil {
			logger.Warn("healthz: db is nil")
			unhealthy = true
		} else if err := deps.DB.PingContext(ctx); err != nil {
			logger.Warnf("healthz: dependency check failed: %v", err)
			unhealthy = true
		}

		if deps.Rdb == nil {
			logger.Warn("healthz: rdb is nil")
			unhealthy = true
		} else if err := deps.Rdb.PingErr(ctx); err != nil {
			logger.Warnf("healthz: redis ping failed: %v", err)
			unhealthy = true
		}

		if unhealthy {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"status":"unhealthy"}`)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	return mux
}
