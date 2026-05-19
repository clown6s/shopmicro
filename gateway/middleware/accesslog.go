package middleware

import (
	"context"
	"fmt"
	"net"
	"time"
	"unicode/utf8"

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

func AccessLogMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {

		// 跳过健康检查端点以减少日志噪音。
		path := string(c.Path())
		if path == "/health" || path == "/ready" || path == "/metrics" {
			c.Next(ctx)
			return
		}

		start := time.Now()

		c.Next(ctx)

		latency := time.Since(start)

		// 从 hertztracing 注入的 otel SpanContext 中提取 trace ID。
		traceID := ""
		span := trace.SpanFromContext(ctx)
		if span.SpanContext().IsValid() {
			traceID = span.SpanContext().TraceID().String()
		}

		// R5-P2-02: 按字节截断可能切断多字节 UTF-8 序列，
		// 使用 utf8.Valid 找到安全边界
		userAgent := string(c.Request.Header.Peek("User-Agent"))
		if len(userAgent) > 128 {
			userAgent = userAgent[:128]
			if !utf8.ValidString(userAgent) {
				userAgent = string([]rune(userAgent))
			}
		}
		logger.C().Desugar().Info("[ACCESS]",
			zap.String("method", string(c.Method())),
			zap.String("path", string(c.Path())),
			zap.Int("status", c.Response.StatusCode()),
			zap.String("latency", latency.String()),
			zap.String("ip", maskIP(ClientRealIP(c))),
			zap.String("user_id", c.GetString("user_id")),
			zap.String("user_agent", userAgent),
			zap.String("trace_id", traceID),
		)
	}
}
