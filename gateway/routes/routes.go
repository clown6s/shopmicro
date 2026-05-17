package routes

import (
	"net/http"

	"github.com/cloudwego/hertz/pkg/app/server"
	clientv3 "go.etcd.io/etcd/client/v3"

	"shopmicro/gateway/health"
	"shopmicro/gateway/proxy"
	pkgredis "shopmicro/pkg/redis"
)

// ProxyOptions 返回所有代理层的路由相关配置选项，
// 包括：公开方法白名单、RPC 方法白名单、HTTP 方法映射。
func ProxyOptions() []proxy.Option {
	return []proxy.Option{
		// 公开方法 —— 不需要 JWT 认证
		proxy.WithPublicMethods("user",
			"RegisterUser",
			"Login",
			"RefreshToken",
		),
		proxy.WithPublicMethods("district",
			"GetProvinceTree",
		),

		// RPC 方法白名单
		proxy.WithAllowedMethods("user",
			"RegisterUser",
			"Login",
			"RefreshToken",
			"Logout",
			"UpdateUser",
			"DeleteUser",
			"ListUser",
		),
		proxy.WithAllowedMethods("district",
			"GetProvinceTree",
		),

		// HTTP 方法映射 —— 限制每个 RPC 方法允许的 HTTP 请求方法
		proxy.WithHTTPMethod("user", "RegisterUser", http.MethodPost),
		proxy.WithHTTPMethod("user", "Login", http.MethodPost),
		proxy.WithHTTPMethod("user", "RefreshToken", http.MethodPost),
		proxy.WithHTTPMethod("user", "Logout", http.MethodPost),
		proxy.WithHTTPMethod("user", "UpdateUser", http.MethodPut),
		proxy.WithHTTPMethod("user", "DeleteUser", http.MethodDelete),
		proxy.WithHTTPMethod("user", "ListUser", http.MethodGet),
		proxy.WithHTTPMethod("district", "GetProvinceTree", http.MethodGet),
	}
}

// Register 注册健康检查端点和业务 API 路由。
func Register(
	h *server.Hertz,
	p *proxy.Proxy,
	rdb *pkgredis.Client,
	etcdClient *clientv3.Client,
) {
	// 健康检查端点
	h.GET("/health", health.Handler())
	h.GET("/ready", health.ReadyHandler(rdb, etcdClient))

	// 业务路由 — HTTP 方法校验在 proxy.Handler() 内完成
	api := h.Group("/api/v1")
	handler := p.Handler()
	api.POST("/:service/:method", handler)
	api.GET("/:service/:method", handler)
	api.PUT("/:service/:method", handler)
	api.DELETE("/:service/:method", handler)
}
