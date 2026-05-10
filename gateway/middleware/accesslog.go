package middleware

import (
	"context"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"shopmicro/pkg/logger"
)

// AccessLogMiddleware records request latency and trace ID for every request.
// It uses zap structured fields to prevent log injection attacks
// (newline/carriage return in method or path).
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

		// P1-02 fix: Use structured fields to prevent log injection.
		logger.C().Desugar().Info("[ACCESS]",
			zap.String("method", string(c.Method())),
			zap.String("path", string(c.Path())),
			zap.Int("status", c.Response.StatusCode()),
			zap.String("latency", latency.String()),
			zap.String("ip", c.ClientIP()),
			zap.String("trace_id", traceID),
		)
	}
}
