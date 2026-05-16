package health

import (
	"context"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	clientv3 "go.etcd.io/etcd/client/v3"

	"shopmicro/pkg/redis"
)

// Handler returns a simple liveness probe — always returns 200.
// K8s livenessProbe should use this endpoint.
func Handler() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.JSON(200, map[string]interface{}{
			"status": "ok",
		})
	}
}

// ReadyHandler returns a readiness probe that checks dependency health.
// K8s readinessProbe should use this endpoint.
// P2-05 fix: 不返回具体依赖名称，防止架构信息泄露
// P2-08: Checks both Redis and Etcd connectivity.
func ReadyHandler(rdb *redis.Client, etcdClient *clientv3.Client) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		failed := false

		// Check Redis — with independent timeout to avoid blocking on dead connections
		if rdb != nil {
			pingCtx, pingCancel := context.WithTimeout(context.Background(), 2*time.Second)
			if !rdb.Ping(pingCtx) {
				failed = true
			}
			pingCancel() // P2-08 fix: cancel immediately after use — not deferred
		} else {
			failed = true
		}

		// Check Etcd — lightweight Get with short timeout
		if etcdClient != nil {
			etcdCtx, etcdCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, err := etcdClient.Get(etcdCtx, "health-check-key", clientv3.WithCountOnly())
			if err != nil {
				failed = true
			}
			etcdCancel() // P2-08 fix: cancel immediately after use — not deferred
		}

		if failed {
			c.JSON(503, map[string]interface{}{
				"status": "not_ready",
			})
			return
		}

		// All checks passed
		c.JSON(200, map[string]interface{}{
			"status": "ready",
		})
	}
}
