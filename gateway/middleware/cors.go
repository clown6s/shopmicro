package middleware

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	"shopmicro/pkg/logger"
)

// CORSConfig 跨域配置
type CORSConfig struct {
	AllowOrigins     []string // 允许的域名列表，"*" 表示全部（生产环境禁用）
	AllowCredentials bool     // 是否允许携带 Cookie / Authorization 头
}

// CORS 返回一个 Hertz 中间件，用于设置跨域资源共享（CORS）响应头。
// 当 AllowCredentials 为 true 时，AllowOrigins 不能包含 "*"（浏览器会拒绝）。
// 此时中间件会回显请求的 Origin 头（前提是该 Origin 在允许列表中）。
func CORS(cfg *CORSConfig, mode string) app.HandlerFunc {
	if cfg == nil {
		if mode == "prod" {
			// 生产环境：除非显式配置，否则不设置 CORS 头
			cfg = &CORSConfig{AllowOrigins: []string{}}
		} else {
			// 开发/预发环境：允许 localhost 以方便调试
			cfg = &CORSConfig{AllowOrigins: []string{"http://localhost:3000"}}
		}
	}
	allowAll := len(cfg.AllowOrigins) == 1 && cfg.AllowOrigins[0] == "*"
	creds := cfg.AllowCredentials
	// 拷贝切片，避免闭包捕获可变引用（防热更新竞态）
	allowOrigins := append([]string{}, cfg.AllowOrigins...)
	// R5-P3-02: 使用 map 替代线性扫描，O(1) 查找
	allowOriginSet := make(map[string]bool, len(allowOrigins))
	for _, o := range allowOrigins {
		allowOriginSet[o] = true
	}

	// P1-08: 通配符 + credentials 在浏览器中会被拒绝，启动时警告
	if allowAll && creds {
		logger.Warnf("CORS: AllowOrigins=['*'] with AllowCredentials=true is invalid — browsers will reject responses")
	}

	// R4-P3-02: 浏览器按规范会拒绝凭据+通配符的组合（默认安全）。
	// 服务端无需额外防护 —— 如果配置错误，浏览器会直接阻止响应。
	// 已移除仅生产环境的防护（因为是无效代码，isAllowedOrigin 不会匹配 "*"）。

	return func(ctx context.Context, c *app.RequestContext) {
		origin := string(c.GetHeader("Origin"))

		// R5-P0-01: 仅在 Origin 被允许时才设置 CORS 响应头，防止信息泄露
		originAllowed := false
		if allowAll && !creds {
			c.Header("Access-Control-Allow-Origin", "*")
			originAllowed = true
		} else if origin != "" && allowOriginSet[origin] {
			c.Header("Access-Control-Allow-Origin", origin)
			existing := string(c.Response.Header.Peek("Vary"))
			if existing != "" {
				c.Header("Vary", existing+", Origin")
			} else {
				c.Header("Vary", "Origin")
			}
			originAllowed = true
		}

		if originAllowed {
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
			// R5-P2-03: 补充常见自定义请求头
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, Token, X-Request-Id, Accept-Language")
			c.Header("Access-Control-Max-Age", "3600")
			if creds {
				c.Header("Access-Control-Allow-Credentials", "true")
			}
		}

		if string(c.Method()) == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next(ctx)
	}
}
