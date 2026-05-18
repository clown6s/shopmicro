package middleware

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
)

// SecurityHeadersMiddleware 添加生产级安全 HTTP 响应头。
// HSTS 仅在 "prod" 模式下启用，以避免在 HTTP 本地开发时出现问题。
func SecurityHeadersMiddleware(mode string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		// P0-03: 移除全局 no-store Cache-Control。
		// GET 响应由 proxy 层按需设置（public, max-age=3600），
		// preGzip 缓存命中时也不覆盖此头，避免安全策略与业务缓存冲突。

		if mode == "prod" {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		c.Next(ctx)
	}
}
