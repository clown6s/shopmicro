package middleware

import (
	"context"
	"runtime/debug"

	"github.com/cloudwego/hertz/pkg/app"

	"shopmicro/pkg/logger"
	"shopmicro/pkg/response"
)

// RecoveryMiddleware catches panics, logs the stack trace, and returns a
// unified error response. This replaces hertz's built-in hrecovery.
func RecoveryMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		defer func() {
			if err := recover(); err != nil {
				logger.Errorf(
					"[PANIC] method=%s path=%s err=%v stack=%s",
					string(c.Method()), string(c.Path()),
					err,
					string(debug.Stack()),
				)
				// P2-17 fix: abort chain first to prevent downstream handlers
				// writing after the error response is committed.
				c.Abort()
				response.InternalError(c)
			}
		}()
		c.Next(ctx)
	}
}
