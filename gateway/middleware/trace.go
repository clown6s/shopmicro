package middleware

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"

	"go.opentelemetry.io/otel/trace"
)

// TraceIDMiddleware sets the X-Trace-Id response header so callers can
// correlate responses with distributed traces. It does NOT create its own
// span — hertztracing.ServerMiddleware handles that.
func TraceIDMiddleware() app.HandlerFunc {

	return func(ctx context.Context, c *app.RequestContext) {

		c.Next(ctx)

		// After hertztracing has created the span, extract trace ID.
		span := trace.SpanFromContext(ctx)
		if span.SpanContext().IsValid() {
			c.Response.Header.Set("X-Trace-Id", span.SpanContext().TraceID().String())
		}
	}
}
