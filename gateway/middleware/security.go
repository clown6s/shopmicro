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
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate")

		if mode == "prod" {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		c.Next(ctx)
	}
}
