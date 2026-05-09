package middleware

import (
	"context"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"go.opentelemetry.io/otel/trace"

	"shopmicro/pkg/logger"
)

// AccessLogMiddleware records request latency and trace ID for every request.
// It extracts the trace ID from the OpenTelemetry SpanContext set by
// hertztracing.ServerMiddleware — no custom TraceMiddleware needed.
func AccessLogMiddleware() app.HandlerFunc {

	return func(ctx context.Context, c *app.RequestContext) {

		start := time.Now()

		c.Next(ctx)

		latency := time.Since(start)

		// Extract trace ID from the otel SpanContext injected by hertztracing.
		traceID := ""
		span := trace.SpanFromContext(ctx)
		if span.SpanContext().IsValid() {
			traceID = span.SpanContext().TraceID().String()
		}

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
