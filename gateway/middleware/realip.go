package middleware

import (
	"context"
	"net"
	"strings"
	"sync"

	"github.com/cloudwego/hertz/pkg/app"

	"shopmicro/pkg/logger"
)

// contextKeyRealIP 是 Hertz 上下文中存储解析后的真实客户端 IP 的键名。
const contextKeyRealIP = "_real_ip"

// TrustedProxies 保存可信代理的 CIDR 或 IP 列表。
// 来自这些代理的请求是可信的；会解析其 X-Forwarded-For 头。
type TrustedProxies []string

// trustedNets 是解析后的可信代理网络列表（首次解析后缓存）。
var trustedNets []net.IPNet

// initOnce 确保 trustedNets 只初始化一次。
var initOnce sync.Once

// parseTrustedNets 将可信代理的 CIDR/IP 解析为 net.IPNet 切片。
// 支持 CIDR 表示法（例如 "10.0.0.0/8"）和纯 IP（视为 /32 或 /128）。
func parseTrustedNets(proxies []string) []net.IPNet {
	nets := make([]net.IPNet, 0, len(proxies))
	for _, p := range proxies {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// 先尝试 CIDR，失败则回退到纯 IP（/32）
		if _, network, err := net.ParseCIDR(p); err == nil {
			nets = append(nets, *network)
			continue
		}
		ip := net.ParseIP(p)
		if ip == nil {
			logger.Warnf("realip: invalid trusted proxy entry %q, skipping", p)
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			nets = append(nets, net.IPNet{IP: ip4, Mask: net.IPv4Mask(255, 255, 255, 255)})
		} else {
			nets = append(nets, net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)})
		}
	}
	return nets
}

// InitTrustedProxies 解析并缓存可信代理网络。
// 在启动时调用一次。由于原子性特性（在服务之前设置），线程安全。
func InitTrustedProxies(proxies []string) {
	initOnce.Do(func() {
		trustedNets = parseTrustedNets(proxies)
	})
}

// isTrusted 检查 IP 是否属于可信代理网络。
func isTrusted(ip net.IP) bool {
	if len(trustedNets) == 0 {
		return false // 未配置可信代理 → 不信任任何 IP
	}
	for _, network := range trustedNets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// GetRealIP 使用信任链从 X-Forwarded-For 中提取真实客户端 IP。
// 算法：从右到左遍历 XFF；跳过可信代理 IP；
// 第一个不可信 IP 即为真实客户端。回退到 TCP 层 IP。
func GetRealIP(c *app.RequestContext) string {
	xff := string(c.GetHeader("X-Forwarded-For"))
	if xff == "" || len(trustedNets) == 0 {
		return c.ClientIP()
	}

	// XFF 格式: client, proxy1, proxy2, ...（最左侧 = 原始客户端）
	parts := strings.Split(xff, ",")
	// 从右到左遍历，跳过可信代理
	for i := len(parts) - 1; i >= 0; i-- {
		ipStr := strings.TrimSpace(parts[i])
		if ipStr == "" {
			continue
		}
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if !isTrusted(ip) {
			return ipStr // 从右侧找到的第一个不可信 IP = 真实客户端
		}
		// 这是一个可信代理，继续向左遍历
	}

	// XFF 中的所有 IP 都是可信的（或 XFF 仅包含代理）→ 使用 TCP 层 IP
	return c.ClientIP()
}

// RealIPMiddleware 从 X-Forwarded-For 中提取真实客户端 IP，
// 并存储到 Hertz 上下文中供下游中间件/处理器使用。
func RealIPMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Set(contextKeyRealIP, GetRealIP(c))
		c.Next(ctx)
	}
}

// ClientRealIP 从 Hertz 上下文中返回真实客户端 IP。
// 如果 RealIPMiddleware 未应用，则回退到 TCP 层 IP。
func ClientRealIP(c *app.RequestContext) string {
	if v, ok := c.Get(contextKeyRealIP); ok {
		if ip, ok := v.(string); ok && ip != "" {
			return ip
		}
	}
	return c.ClientIP()
}
