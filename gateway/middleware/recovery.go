package middleware

import (
	"context"
	"runtime/debug"

	"shopmicro/pkg/logger"

	"github.com/cloudwego/hertz/pkg/app"
)

func RecoveryMiddleware() app.HandlerFunc {

	return func(ctx context.Context, c *app.RequestContext) {

		defer func() {

			if err := recover(); err != nil {

				logger.Errorf(
					"[PANIC] err=%v stack=%s",
					err,
					string(debug.Stack()),
				)

				c.JSON(500, map[string]interface{}{
					"code": 500,
					"msg":  "internal server error",
				})

				c.Abort()
			}
		}()

		c.Next(ctx)
	}
}
