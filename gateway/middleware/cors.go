package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

// CORSConfig 跨域配置
type CORSConfig struct {
	AllowOrigins []string // 允许的域名列表，"*" 表示全部
}

// CORS 返回跨域中间件
func CORS(cfg *CORSConfig) app.HandlerFunc {
	if cfg == nil || len(cfg.AllowOrigins) == 0 {
		cfg = &CORSConfig{AllowOrigins: []string{"*"}}
	}
	allowAll := len(cfg.AllowOrigins) == 1 && cfg.AllowOrigins[0] == "*"
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
