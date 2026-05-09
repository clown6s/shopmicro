package middleware

import (
	"context"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"shopmicro/pkg/logger"
)

func AccessLogMiddleware() app.HandlerFunc {

	return func(ctx context.Context, c *app.RequestContext) {

		start := time.Now()

		c.Next(ctx)

		latency := time.Since(start)

		traceID := string(
			c.Response.Header.Peek("X-Trace-Id"),
		)

		logger.Infof(
			"[ACCESS] method=%s path=%s status=%d latency=%s ip=%s trace_id=%s",
			string(c.Method()),
			string(c.Path()),
			c.Response.StatusCode(),
			latency.String(),
			c.ClientIP(),
			traceID,
		)
	}
}
