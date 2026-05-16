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
// P2-08: Checks both Redis and Etcd connectivity.
func ReadyHandler(rdb *redis.Client, etcdClient *clientv3.Client) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		failed := make([]string, 0)

		// Check Redis — with independent timeout to avoid blocking on dead connections
		if rdb != nil {
			pingCtx, pingCancel := context.WithTimeout(context.Background(), 2*time.Second)
			if !rdb.Ping(pingCtx) {
				failed = append(failed, "redis")
			}
			pingCancel() // P2-08 fix: cancel immediately after use — not deferred
		} else {
			failed = append(failed, "redis (nil)")
		}

		// Check Etcd — lightweight Get with short timeout
		if etcdClient != nil {
			etcdCtx, etcdCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, err := etcdClient.Get(etcdCtx, "health-check-key", clientv3.WithCountOnly())
			if err != nil {
				failed = append(failed, "etcd")
			}
			etcdCancel() // P2-08 fix: cancel immediately after use — not deferred
		}

		if len(failed) > 0 {
			c.JSON(503, map[string]interface{}{
				"status": "not_ready",
				"failed": failed,
			})
			return
		}

		// All checks passed
		c.JSON(200, map[string]interface{}{
			"status": "ready",
		})
	}
}
