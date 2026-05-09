package middleware

import (
	"context"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/google/uuid"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

func TraceMiddleware() app.HandlerFunc {

	tracer := otel.Tracer("gateway")

	return func(ctx context.Context, c *app.RequestContext) {

		traceID := string(
			c.GetHeader("X-Trace-Id"),
		)

		if traceID == "" {
			traceID = uuid.NewString()
		}

		// response header
		c.Response.Header.Set(
			"X-Trace-Id",
			traceID,
		)

		// rpc metadata
		ctx = metainfo.WithPersistentValue(
			ctx,
			"trace_id",
			traceID,
		)

		// otel span
		ctx, span := tracer.Start(
			ctx,
			string(c.Path()),
		)

		span.SetAttributes(

			attribute.String(
				"http.method",
				string(c.Method()),
			),

			attribute.String(
				"http.path",
				string(c.Path()),
			),

			attribute.String(
				"trace.id",
				traceID,
			),
		)

		defer span.End()

		c.Next(ctx)

		span.SetAttributes(
			attribute.Int(
				"http.status_code",
				c.Response.StatusCode(),
			),
		)
	}
}
