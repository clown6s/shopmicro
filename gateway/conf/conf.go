package conf

import (
	"fmt"
	"strings"
	"time"

	"shopmicro/pkg/conf"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/redis"
	"shopmicro/pkg/secrets"
)

type Config struct {
	Server         conf.Server         `yaml:"server"`
	Etcd           conf.Etcd           `yaml:"etcd"`
	CORS           CORS                `yaml:"cors"`
	Redis          redis.Config        `yaml:"redis"`
	JWT            JWT                 `yaml:"jwt"`
	ProtoBase      string              `yaml:"protoBase"`
	Mode           string              `yaml:"mode"`
	RateLimit      RateLimit           `yaml:"rateLimit"`
	CircuitBreaker CircuitBreakerCfg   `yaml:"circuitBreaker"`
	Vault          secrets.VaultConfig `yaml:"vault"`
	OTEL           OTEL                `yaml:"otel"`
	// TrustedProxies 列出可信反向代理的 CIDR 或 IP 列表。
	// X-Forwarded-For 从右往左解析；第一个不可信 IP 即为真实客户端。
	TrustedProxies []string `yaml:"trustedProxies"`
}

type OTEL struct {
	Endpoint   string  `yaml:"endpoint"`   // OTLP collector 地址
	SampleRate float64 `yaml:"sampleRate"` // 采样率 0.0-1.0
}

type CORS struct {
	AllowOrigins     []string `yaml:"allowOrigins"`
	AllowCredentials bool     `yaml:"allowCredentials"`
}

type JWT struct {
	Secret string `yaml:"secret"`
	Issuer string `yaml:"issuer"`
}

// RateLimit 分布式限流配置 (Redis 滑动窗口)
type RateLimit struct {
	Limit   int `yaml:"limit"`     // 窗口内最大请求数 (default: 100)
	WindowS int `yaml:"windowSec"` // 滑动窗口时长（秒） (default: 60)
	// P1-15: 敏感操作的按端点限流配置
	Endpoints map[string]EndpointRate `yaml:"endpoints"`
}

// EndpointRate 定义特定端点的限流配置。
// 键格式: "service/method"（例如 "user/Login"）
type EndpointRate struct {
	Limit   int `yaml:"limit"`     // 窗口内最大请求数
	WindowS int `yaml:"windowSec"` // 窗口时长（秒）(default: 全局窗口)
}

// CircuitBreakerCfg 熔断器配置
type CircuitBreakerCfg struct {
	MaxRequests         int `yaml:"maxRequests"`         // 半开状态最大请求数 (default: 5)
	IntervalS           int `yaml:"intervalSec"`         // 统计间隔秒数 (default: 60)
	TimeoutS            int `yaml:"timeoutSec"`          // 熔断恢复超时秒数 (default: 10)
	ConsecutiveFailures int `yaml:"consecutiveFailures"` // 连续失败触发熔断 (default: 5)
}

// SetDefaults 为零值字段填充合理的默认值。
func (c *Config) SetDefaults() {
	rl := &c.RateLimit
	if rl.Limit <= 0 {
		rl.Limit = 100
	}
	if rl.WindowS <= 0 {
		rl.WindowS = 60
	}

	cb := &c.CircuitBreaker
	if cb.MaxRequests <= 0 {
		cb.MaxRequests = 5
	}
	if cb.IntervalS <= 0 {
		cb.IntervalS = 60
	}
	if cb.TimeoutS <= 0 {
		cb.TimeoutS = 10
	}
	if cb.ConsecutiveFailures <= 0 {
		cb.ConsecutiveFailures = 5
	}

	// OTEL 默认值
	if c.OTEL.Endpoint == "" {
		c.OTEL.Endpoint = "localhost:4317"
	}
	if c.OTEL.SampleRate <= 0 {
		c.OTEL.SampleRate = 0.1
	}
}

func (c *Config) ApplyEnv() {
	// 初始化密钥提供者（如配置了 Vault 则使用，否则回退到环境变量）
	// P2-11 修复：绝不使用 config.yaml 中的 Vault.Token 作为默认值 ——
	// 那样会导致 token 被提交到代码仓库。只能通过环境变量设置。
	if c.Vault.Address != "" {
		if token := conf.Env("VAULT_TOKEN", ""); token != "" {
			c.Vault.Token = token
		}
		if c.Vault.Token == "" {
			logger.Warnf("vault address is set but VAULT_TOKEN env var is missing — vault init will fail")
		}
		secrets.InitProvider(c.Vault)
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
		c.Etcd.Addrs = strings.Split(v, ",")
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
	mode := strings.ToLower(c.Mode)

	// JWT 密钥验证：生产环境要求 64 字符，开发环境放宽到 32 字符
	minSecretLen := 64
	if mode == "dev" {
		minSecretLen = 32
	}
	if len(c.JWT.Secret) < minSecretLen {
		return fmt.Errorf("jwt secret must be at least %d characters, got %d", minSecretLen, len(c.JWT.Secret))
	}
	if c.Redis.Addr == "" {
		return fmt.Errorf("redis addr is required, set REDIS_ADDR env or config.yaml")
	}
	if len(c.Etcd.Addrs) == 0 {
		return fmt.Errorf("etcd addresses are required, set ETCD_ADDRS env or config.yaml")
	}
	if mode != "dev" && mode != "prod" && mode != "staging" {
		return fmt.Errorf("invalid mode %q, expected dev/prod/staging", c.Mode)
	}
	// P2-08: 生产环境要求 Redis 密码
	if mode == "prod" && c.Redis.Password == "" {
		return fmt.Errorf("redis password is required in production mode, set REDIS_PASSWORD env")
	}
	return nil
}

// CircuitBreakerDurations 返回解析后的统计间隔和熔断超时时长。
func (c *Config) CircuitBreakerDurations() (interval, timeout time.Duration) {
	return time.Duration(c.CircuitBreaker.IntervalS) * time.Second,
		time.Duration(c.CircuitBreaker.TimeoutS) * time.Second
}
