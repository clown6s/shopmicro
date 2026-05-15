package middleware

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
)

// SecurityHeadersMiddleware adds production-grade security HTTP headers.
// HSTS is only enabled in "prod" mode to avoid breaking local development over HTTP.
func SecurityHeadersMiddleware(mode string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
		c.Header("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")

		if mode == "prod" {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		c.Next(ctx)
	}
}
