package middleware

import (
	"context"
	"fmt"
	"net"
	"strings"
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

// safeClientIP 安全获取客户端真实 IP
// P2-SEC-04 fix: 防止 X-Forwarded-For IP 伪造攻击
// 仅信任来自已知代理的最后一跳 IP（需配合反向代理配置）
func safeClientIP(c *app.RequestContext) string {
	// 优先从标准代理头获取 IP
	xff := string(c.GetHeader("X-Forwarded-For"))
	if xff != "" {
		// X-Forwarded-For 可能包含多个 IP，取最右边的一个（最近的代理）
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			ip := strings.TrimSpace(ips[len(ips)-1])
			// 验证 IP 格式合法性，防止注入攻击
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}

	// 其次尝试 X-Real-IP
	xri := string(c.GetHeader("X-Real-IP"))
	if xri != "" {
		if net.ParseIP(xri) != nil {
			return xri
		}
	}

	// 最后降级到 c.ClientIP()（Hertz 默认行为）
	return c.ClientIP()
}
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
		// P2-SEC-04 fix: 使用安全的 IP 获取方式
		logger.C().Desugar().Info("[ACCESS]",
			zap.String("method", string(c.Method())),
			zap.String("path", string(c.Path())),
			zap.Int("status", c.Response.StatusCode()),
			zap.String("latency", latency.String()),
			zap.String("ip", maskIP(safeClientIP(c))),
			zap.String("trace_id", traceID),
		)
	}
}
