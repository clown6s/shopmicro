package middleware

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"shopmicro/pkg/logger"
)

// maskIP 脱敏 IP 地址：保留前两个八位组，隐藏后两部分
// 例如: 192.168.1.100 → 192.168.x.x
// 用于日志记录以满足隐私合规要求 (GDPR)
func maskIP(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "unknown"
	}
	// IPv4
	if v4 := parsed.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.x.x", v4[0], v4[1])
	}
	// IPv6: 保留前两组
	if len(parsed) >= 4 {
		return fmt.Sprintf("%x:%x:%x:%x:x:x:x:x", parsed[0], parsed[1], parsed[2], parsed[3])
	}
	return "masked"
}

// AccessLogMiddleware records request latency and trace ID for every request.
// It uses zap structured fields to prevent log injection attacks
// (newline/carriage return in method or path).
// Health check endpoints (/health, /ready) are skipped to reduce log noise.
func AccessLogMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {

		// Skip health check endpoints to reduce log noise.
		path := string(c.Path())
		if path == "/health" || path == "/ready" {
			c.Next(ctx)
			return
		}

		start := time.Now()

		c.Next(ctx)

		latency := time.Since(start)

		// Extract trace ID from the otel SpanContext injected by hertztracing.
		traceID := ""
		span := trace.SpanFromContext(ctx)
		if span.SpanContext().IsValid() {
			traceID = span.SpanContext().TraceID().String()
		}

		// R4-SEC-03: IP 脱敏以满足隐私合规要求 (GDPR)
		logger.C().Desugar().Info("[ACCESS]",
			zap.String("method", string(c.Method())),
			zap.String("path", string(c.Path())),
			zap.Int("status", c.Response.StatusCode()),
			zap.String("latency", latency.String()),
			zap.String("ip", maskIP(c.ClientIP())),
			zap.String("trace_id", traceID),
		)
	}
}
