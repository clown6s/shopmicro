package health

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
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
// P0-07: Returns 200 when all dependencies are reachable.
func ReadyHandler() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		// TODO: Add actual dependency checks when deps are injected:
		//   - Redis Ping
		//   - etcd status check
		// For now, always healthy — wire up real checks as infrastructure matures.
		c.JSON(200, map[string]interface{}{
			"status": "ready",
		})
	}
}
