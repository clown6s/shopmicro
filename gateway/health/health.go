package health

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"

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
// P2-08: Actually checks Redis connectivity.
func ReadyHandler(rdb *redis.Client) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		// Check Redis
		if rdb == nil || !rdb.Ping(ctx) {
			c.JSON(503, map[string]interface{}{
				"status": "not_ready",
				"failed": "redis",
			})
			return
		}

		// All checks passed
		c.JSON(200, map[string]interface{}{
			"status": "ready",
		})
	}
}
