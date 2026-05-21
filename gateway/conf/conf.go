package conf

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"shopmicro/pkg/conf"
	"shopmicro/pkg/redis"
)

type Config struct {
	Server    conf.Server  `yaml:"server"`
	Etcd      conf.Etcd    `yaml:"etcd"`
	CORS      CORS         `yaml:"cors"`
	Redis     redis.Config `yaml:"redis"`
	JWT       JWT          `yaml:"jwt"`
	ProtoBase string       `yaml:"protoBase"`
	Mode      string       `yaml:"mode"`
	RateLimit RateLimit    `yaml:"rateLimit"`
	OTEL      OTEL         `yaml:"otel"`
	// TrustedProxies 列出可信反向代理的 CIDR 或 IP 列表。
	// X-Forwarded-For 从右往左解析；第一个不可信 IP 即为真实客户端。
	TrustedProxies []string `yaml:"trustedProxies"`
	// HTTPServer 服务器超时与限制配置
	HTTPServer HTTPServer `yaml:"httpServer"`
	// RPC 代理 RPC 客户端超时与重试配置
	RPC RPCConfig `yaml:"rpc"`
	// CircuitBreaker 熔断器配置
	CircuitBreaker CircuitBreakerConfig `yaml:"circuitBreaker"`
	// PreGzip 预压缩缓存配置
	PreGzip PreGzipConfig `yaml:"preGzip"`
	// AccessLog 访问日志配置
	AccessLog AccessLogConfig `yaml:"accessLog"`
}

type HTTPServer struct {
	ReadTimeout  int `yaml:"readTimeoutSec"`  // 读超时秒 (default: 10)
	WriteTimeout int `yaml:"writeTimeoutSec"` // 写超时秒 (default: 30)
	IdleTimeout  int `yaml:"idleTimeoutSec"`  // 空闲超时秒 (default: 60)
	MaxBodySize  int `yaml:"maxBodySizeKB"`   // 最大请求体 KB (default: 1024, 即 1MB)
}

type RPCConfig struct {
	RPCTimeout     int `yaml:"rpcTimeoutSec"`     // RPC 调用超时秒 (default: 5)
	ConnectTimeout int `yaml:"connectTimeoutSec"` // 连接建立超时秒 (default: 3)
	MaxRetries     int `yaml:"maxRetries"`        // 瞬态错误最大重试次数 (default: 2)
}

type CircuitBreakerConfig struct {
	ConsecutiveFailures int `yaml:"consecutiveFailures"` // 连续失败触发熔断辅助条件 (default: 5)
	IntervalSec         int `yaml:"intervalSec"`         // Closed 状态计数重置间隔秒 (default: 30)
	TimeoutSec          int `yaml:"timeoutSec"`          // Open→HalfOpen 超时秒 (default: 5)
	MaxRequests         int `yaml:"maxRequests"`         // HalfOpen 状态最大探测请求数 (default: 10)
}

type PreGzipConfig struct {
	TTLSec          int `yaml:"ttlSec"`             // 缓存有效期秒 (default: 3600)
	CleanupInterval int `yaml:"cleanupIntervalSec"` // 清理间隔秒 (default: 1800)
	MinSizeKB       int `yaml:"minSizeKB"`          // 最小缓存响应 KB (default: 100)
	MaxEntries      int `yaml:"maxEntries"`         // 最大缓存条目数 (default: 1000)
	MaxQueryLen     int `yaml:"maxQueryLen"`        // 最大 query string 长度 (default: 2048)
	MaxKeyLen       int `yaml:"maxKeyLen"`          // 最大缓存键长度 (default: 4096)
}

// AccessLogConfig 访问日志配置
// Level: "all" 记录所有请求, "errorOnly" 只记录非 2xx/3xx 响应
type AccessLogConfig struct {
	Level string `yaml:"level"`
}

type OTEL struct {
	Endpoint   string  `yaml:"endpoint"`   // OTLP collector 地址
	SampleRate float64 `yaml:"sampleRate"` // 采样率 0.0-1.0
}

type CORS struct {
	AllowOrigins     []string `yaml:"allowOrigins"`
	AllowCredentials bool     `yaml:"allowCredentials"`
	AllowHeaders     []string `yaml:"allowHeaders"` // 允许的请求头列表，为空时使用默认值
	MaxAgeSec        int      `yaml:"maxAgeSec"`    // preflight 缓存秒数 (default: 3600)
}

type JWT struct {
	Secret string `yaml:"secret"`
	Issuer string `yaml:"issuer"`
}

// RateLimit 分布式限流配置 (Redis 滑动窗口)
type RateLimit struct {
	Limit     int `yaml:"limit"`     // 窗口内最大请求数 (default: 100)
	WindowSec int `yaml:"windowSec"` // 滑动窗口时长（秒） (default: 60)
	// P1-15: 敏感操作的按端点限流配置
	Endpoints map[string]EndpointRate `yaml:"endpoints"`
}

// EndpointRate 定义特定端点的限流配置。
// 键格式: "service/method"（例如 "user/Login"）
type EndpointRate struct {
	Limit     int `yaml:"limit"`     // 窗口内最大请求数
	WindowSec int `yaml:"windowSec"` // 窗口时长（秒）(default: 全局窗口)
}

// SetDefaults 为零值字段填充合理的默认值。
func (c *Config) SetDefaults() {
	c.Mode = strings.ToLower(c.Mode)

	rl := &c.RateLimit
	if rl.Limit <= 0 {
		rl.Limit = 100
	}
	if rl.WindowSec <= 0 {
		rl.WindowSec = 60
	}

	// R5-P2-12: OTEL 采样率根据运行模式设置合理默认值
	if c.OTEL.Endpoint == "" {
		c.OTEL.Endpoint = "localhost:4317"
	}
	// sampleRate == 0：显式关闭采样
	// sampleRate < 0：未设置，使用默认值
	if c.OTEL.SampleRate < 0 {
		if c.Mode == "dev" {
			c.OTEL.SampleRate = 1.0
		} else {
			c.OTEL.SampleRate = 0.1
		}
	}

	// HTTP 服务器超时
	hs := &c.HTTPServer
	if hs.ReadTimeout <= 0 {
		hs.ReadTimeout = 10
	}
	if hs.WriteTimeout <= 0 {
		hs.WriteTimeout = 30
	}
	if hs.IdleTimeout <= 0 {
		hs.IdleTimeout = 60
	}
	if hs.MaxBodySize <= 0 {
		hs.MaxBodySize = 1024
	}

	// RPC 配置
	rpc := &c.RPC
	if rpc.RPCTimeout <= 0 {
		rpc.RPCTimeout = 5
	}
	if rpc.ConnectTimeout <= 0 {
		rpc.ConnectTimeout = 3
	}
	if rpc.MaxRetries <= 0 {
		rpc.MaxRetries = 2
	}
	if rpc.MaxRetries > 10 {
		rpc.MaxRetries = 10
	}

	// 熔断器
	cb := &c.CircuitBreaker
	if cb.ConsecutiveFailures <= 0 {
		cb.ConsecutiveFailures = 5
	}
	if cb.IntervalSec <= 0 {
		cb.IntervalSec = 30
	}
	if cb.TimeoutSec <= 0 {
		cb.TimeoutSec = 5
	}
	if cb.MaxRequests <= 0 {
		cb.MaxRequests = 10
	}

	// PreGzip
	pg := &c.PreGzip
	if pg.TTLSec <= 0 {
		pg.TTLSec = 3600
	}
	if pg.CleanupInterval <= 0 {
		pg.CleanupInterval = 1800
	}
	if pg.MinSizeKB <= 0 {
		pg.MinSizeKB = 100
	}
	if pg.MaxEntries <= 0 {
		pg.MaxEntries = 1000
	}
	if pg.MaxQueryLen <= 0 {
		pg.MaxQueryLen = 2048
	}
	if pg.MaxKeyLen <= 0 {
		pg.MaxKeyLen = 4096
	}

	// AccessLog：dev 全量记录，prod/staging 只记非 2xx
	if c.AccessLog.Level == "" {
		if c.Mode == "dev" {
			c.AccessLog.Level = "all"
		} else {
			c.AccessLog.Level = "errorOnly"
		}
	}
}

func (c *Config) ApplyEnv() {
	if v := conf.Env("MODE", ""); v != "" {
		c.Mode = strings.ToLower(v)
	}
	if v := conf.Env("REDIS_ADDR", ""); v != "" {
		c.Redis.Addr = v
	}
	if v := conf.Env("REDIS_PASSWORD", ""); v != "" {
		c.Redis.Password = v
	}
	if v := conf.Env("JWT_SECRET", ""); v != "" {
		c.JWT.Secret = v
	}
	// P2-09: Docker/K8s 环境下覆盖 proto 基础路径
	if v := conf.Env("PROTO_BASE", ""); v != "" {
		c.ProtoBase = v
	}
	if v := conf.Env("ETCD_ADDRS", ""); v != "" {
		addrs := strings.Split(v, ",")
		cleaned := make([]string, 0, len(addrs))
		for _, a := range addrs {
			a = strings.TrimSpace(a)
			if a != "" {
				cleaned = append(cleaned, a)
			}
		}
		if len(cleaned) > 0 {
			c.Etcd.Addrs = cleaned
		}
	}
	// P2-10: 支持通过 SERVER_ADDR 覆盖配置，用于容器部署（端口绑定）
	if v := conf.Env("SERVER_ADDR", ""); v != "" {
		c.Server.Addr = v
	}
	// OTEL 配置覆盖
	if v := conf.Env("OTEL_EXPORTER_OTLP_ENDPOINT", ""); v != "" {
		c.OTEL.Endpoint = v
	}
}

func (c *Config) Validate() error {
	// c.Mode 已在 SetDefaults() 中 toLower，此处直接使用

	// JWT 密钥验证：HMAC-SHA256 安全要求至少 64 字符
	if len(c.JWT.Secret) < 64 {
		return fmt.Errorf("jwt secret must be at least 64 characters, got %d", len(c.JWT.Secret))
	}
	if c.Redis.Addr == "" {
		return fmt.Errorf("redis addr is required, set REDIS_ADDR env or config.yaml")
	}
	if err := c.Redis.Validate(); err != nil {
		return fmt.Errorf("redis config invalid: %w", err)
	}
	if len(c.Etcd.Addrs) == 0 {
		return fmt.Errorf("etcd addresses are required, set ETCD_ADDRS env or config.yaml")
	}
	if c.Mode != "dev" && c.Mode != "prod" && c.Mode != "staging" {
		return fmt.Errorf("invalid mode %q, expected dev/prod/staging", c.Mode)
	}
	// P2-08: 生产环境要求 Redis 密码
	if c.Mode == "prod" && c.Redis.Password == "" {
		return fmt.Errorf("redis password is required in production mode, set REDIS_PASSWORD env")
	}
	if c.OTEL.SampleRate < 0 || c.OTEL.SampleRate > 1.0 {
		return fmt.Errorf("otel sampleRate must be between 0.0 and 1.0, got %v", c.OTEL.SampleRate)
	}
	// 校验按端点限流配置：Limit 和 WindowSec 必须为正数
	for epKey, epCfg := range c.RateLimit.Endpoints {
		if epCfg.Limit < 1 {
			return fmt.Errorf("rate limit endpoint %q: limit must be >= 1, got %d", epKey, epCfg.Limit)
		}
		if epCfg.WindowSec < 0 {
			return fmt.Errorf("rate limit endpoint %q: windowSec must be >= 1, got %d", epKey, epCfg.WindowSec)
		}
	}
	if c.JWT.Issuer == "" && c.Mode == "prod" {
		return fmt.Errorf("jwt issuer is required in production mode to prevent accepting tokens from any issuer")
	}

	// CORS 校验：AllowOrigins 必须为合法 HTTP(S) origin
	for i, origin := range c.CORS.AllowOrigins {
		if origin == "*" {
			if c.CORS.AllowCredentials {
				return fmt.Errorf("cors: AllowOrigins contains '*' with AllowCredentials=true — browsers will reject responses")
			}
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("cors: AllowOrigins[%d] %q is not a valid HTTP(S) origin (expected http:// or https://host)", i, origin)
		}
	}

	// TrustedProxies 校验：每条必须是合法 CIDR 或 IP
	for i, p := range c.TrustedProxies {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(p); err == nil {
			continue
		}
		if ip := net.ParseIP(p); ip != nil {
			continue
		}
		return fmt.Errorf("trustedProxies[%d] %q is not a valid CIDR or IP address", i, p)
	}

	return nil
}
