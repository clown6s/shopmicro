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
					"[PANIC] err=%v stack=%s",
					err,
					string(debug.Stack()),
				)

				response.InternalError(c)

				c.Abort()
			}
		}()

		c.Next(ctx)
	}
}
