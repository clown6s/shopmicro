package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

// CORSConfig 跨域配置
type CORSConfig struct {
	AllowOrigins []string // 允许的域名列表，"*" 表示全部
}

// CORS returns a Hertz middleware that applies Cross-Origin Resource Sharing headers.
// P1-05: In production mode, if allowOrigins contains "*", the middleware logs a warning.
// For strict production CORS, set explicit origins in config.
func CORS(cfg *CORSConfig, mode string) app.HandlerFunc {
	if cfg == nil || len(cfg.AllowOrigins) == 0 {
		cfg = &CORSConfig{AllowOrigins: []string{"*"}}
	}
	allowAll := len(cfg.AllowOrigins) == 1 && cfg.AllowOrigins[0] == "*"

	// P1-05: Warn in production if wildcard is used
	if mode == "prod" && allowAll {
		// In strict mode, reject wildcard CORS in production.
		// For now, log a warning — operators should set explicit origins.
		fmt.Printf("[WARN] CORS: wildcard '*' origin is not recommended in production mode\n")
	}

	origins := strings.Join(cfg.AllowOrigins, ", ")

	return func(ctx context.Context, c *app.RequestContext) {
		origin := string(c.GetHeader("Origin"))
		if allowAll {
			c.Header("Access-Control-Allow-Origin", "*")
		} else if origin != "" && isAllowedOrigin(origin, cfg.AllowOrigins) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
		}

		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, Token")
		c.Header("Access-Control-Max-Age", "86400")

		if allowAll {
			_ = origins // 仅用于文档说明，dev 模式允许全部
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
