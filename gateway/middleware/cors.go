package middleware

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
)

// CORSConfig 跨域配置
type CORSConfig struct {
	AllowOrigins     []string // 允许的域名列表，"*" 表示全部（生产环境禁用）
	AllowCredentials bool     // 是否允许携带 Cookie / Authorization 头
}

// CORS returns a Hertz middleware that applies Cross-Origin Resource Sharing headers.
// When AllowCredentials is true, AllowOrigins must NOT contain "*" (browser rejects it).
// Instead, the middleware echoes back the request Origin if it's in the allowed list.
func CORS(cfg *CORSConfig, mode string) app.HandlerFunc {
	if cfg == nil {
		cfg = &CORSConfig{AllowOrigins: []string{"*"}}
	}
	allowAll := len(cfg.AllowOrigins) == 1 && cfg.AllowOrigins[0] == "*"
	creds := cfg.AllowCredentials

	// Security: credentials + wildcard is rejected by browsers
	if creds && allowAll && mode == "prod" {
		allowAll = false // force explicit origin check in production
	}

	return func(ctx context.Context, c *app.RequestContext) {
		origin := string(c.GetHeader("Origin"))
		if allowAll && !creds {
			c.Header("Access-Control-Allow-Origin", "*")
		} else if origin != "" && isAllowedOrigin(origin, cfg.AllowOrigins) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
		}

		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, Token")
		c.Header("Access-Control-Max-Age", "86400")

		if creds {
			c.Header("Access-Control-Allow-Credentials", "true")
		}

		if string(c.Method()) == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next(ctx)
	}
}

func isAllowedOrigin(origin string, allowed []string) bool {
	for _, a := range allowed {
		if a == origin {
			return true
		}
	}
	return false
}
