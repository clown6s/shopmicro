package middleware

import (
	"context"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"shopmicro/pkg/logger"
	"shopmicro/pkg/response"
)

// stackTracer is wired by recovery_debug.go via build tags.
// In production builds (-tags prod) this stays nil and
// runtime/debug is never linked into the binary.
var stackTracer func() string

// RecoveryMiddleware catches panics, logs the error, and returns a unified
// error response. In production mode the full stack trace is suppressed to
// prevent internal architecture details from leaking into logs.
//
// When compiled with -tags prod, debug.Stack() code is excluded entirely
// (via recovery_debug.go build tag). The runtime mode check serves as an
// additional defence-in-depth layer.
func RecoveryMiddleware(mode string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		defer func() {
			if err := recover(); err != nil {
				if stackTracer != nil && !strings.EqualFold(mode, "prod") {
					logger.Errorf(
						"[PANIC] method=%s path=%s err=%v stack=%s",
						string(c.Method()), string(c.Path()),
						err,
						stackTracer(),
					)
				} else {
					logger.Errorf(
						"[PANIC] method=%s path=%s err=%v",
						string(c.Method()), string(c.Path()),
						err,
					)
				}
				// P2-17 fix: abort chain first to prevent downstream handlers
				// writing after the error response is committed.
				c.Abort()
				response.InternalError(c)
			}
		}()
		c.Next(ctx)
	}
}
