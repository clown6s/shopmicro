package routes

import (
	"context"
	"net"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	clientv3 "go.etcd.io/etcd/client/v3"

	"shopmicro/gateway/health"
	"shopmicro/gateway/proxy"
	pkgredis "shopmicro/pkg/redis"
)

// privateRanges 内网地址段，包级别初始化一次
var privateRanges = []struct {
	network *net.IPNet
}{
	{mustParseCIDR("10.0.0.0/8")},
	{mustParseCIDR("172.16.0.0/12")},
	{mustParseCIDR("192.168.0.0/16")},
	{mustParseCIDR("127.0.0.0/8")},
	{mustParseCIDR("::1/128")},
}

// isPrivateIP 判断 IP 是否属于内网地址（RFC 1918 + 环回地址）
func isPrivateIP(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, r := range privateRanges {
		if r.network.Contains(parsed) {
			return true
		}
	}
	return false
}

func mustParseCIDR(s string) *net.IPNet {
	_, network, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return network
}

// P1-03: metricsHandler 包装 health.MetricsHandler，限制仅内网访问
func metricsHandler() app.HandlerFunc {
	inner := health.MetricsHandler()
	return func(ctx context.Context, c *app.RequestContext) {
		clientIP := c.ClientIP()
		if !isPrivateIP(clientIP) {
			c.String(http.StatusForbidden, "forbidden")
			c.Abort()
			return
		}
		inner(ctx, c)
	}
}

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
			"GetProvinces",
			"GetChildren",
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
			"GetProvinces",
			"GetChildren",
		),

		// HTTP 方法映射 —— 限制每个 RPC 方法允许的 HTTP 请求方法
		proxy.WithHTTPMethod("user", "RegisterUser", http.MethodPost),
		proxy.WithHTTPMethod("user", "Login", http.MethodPost),
		proxy.WithHTTPMethod("user", "RefreshToken", http.MethodPost),
		proxy.WithHTTPMethod("user", "Logout", http.MethodPost),
		proxy.WithHTTPMethod("user", "UpdateUser", http.MethodPut),
		proxy.WithHTTPMethod("user", "DeleteUser", http.MethodDelete),
		proxy.WithHTTPMethod("user", "ListUser", http.MethodGet),
		proxy.WithHTTPMethod("district", "GetProvinces", http.MethodGet),
		proxy.WithHTTPMethod("district", "GetChildren", http.MethodGet),
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

	// P2-02: Prometheus 格式的运行时指标端点（P1-03: 限制内网访问）
	h.GET("/metrics", metricsHandler())

	// 业务路由 — HTTP 方法校验在 proxy.Handler() 内完成
	api := h.Group("/api/v1")
	handler := p.Handler()
	api.POST("/:service/:method", handler)
	api.GET("/:service/:method", handler)
	api.PUT("/:service/:method", handler)
	api.DELETE("/:service/:method", handler)
}
