package health

import (
	"context"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	clientv3 "go.etcd.io/etcd/client/v3"

	"shopmicro/pkg/logger"
	"shopmicro/pkg/redis"
)

// Handler 返回一个简单的存活探测端点 —— 始终返回 200。
// K8s livenessProbe 应使用此端点。
func Handler() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.JSON(http.StatusOK, map[string]interface{}{
			"status": "ok",
		})
	}
}

// ReadyHandler 返回一个就绪探测端点，检查依赖服务健康状态。
// K8s readinessProbe 应使用此端点。
// P2-05 修复：不返回具体依赖名称，防止架构信息泄露
// P2-08: 检查 Redis 和 Etcd 连通性。
func ReadyHandler(rdb *redis.Client, etcdClient *clientv3.Client) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		failed := false

		// 检查 Redis —— 使用请求上下文，以便关闭时取消等待中的 ping
		if rdb != nil {
			pingCtx, pingCancel := context.WithTimeout(ctx, 2*time.Second)
			defer pingCancel()
			if !rdb.Ping(pingCtx) {
				logger.Warn("readiness: redis ping failed")
				failed = true
			}
		} else {
			logger.Warn("readiness: redis client is nil")
			failed = true
		}

		// 检查 Etcd —— 带短超时的轻量级 Get 请求
		if etcdClient != nil {
			etcdCtx, etcdCancel := context.WithTimeout(ctx, 2*time.Second)
			defer etcdCancel()
			_, err := etcdClient.Get(etcdCtx, "health-check-key", clientv3.WithCountOnly())
			if err != nil {
				logger.Warnf("readiness: etcd check failed: %v", err)
				failed = true
			}
		} else {
			logger.Warn("readiness: etcd client is nil")
			failed = true
		}

		if failed {
			c.JSON(http.StatusServiceUnavailable, map[string]interface{}{
				"status": "not_ready",
			})
			return
		}

		// 所有检查通过
		c.JSON(http.StatusOK, map[string]interface{}{
			"status": "ready",
		})
	}
}
