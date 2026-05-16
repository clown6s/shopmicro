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
	// P1-15: Per-endpoint rate limits for sensitive operations
	Endpoints map[string]EndpointRate `yaml:"endpoints"`
}

// EndpointRate defines rate limiting for a specific endpoint.
// Key format: "service/method" (e.g. "user/Login")
type EndpointRate struct {
	Limit   int `yaml:"limit"`     // max requests per window
	WindowS int `yaml:"windowSec"` // window duration in seconds (default: global window)
}

// CircuitBreakerCfg 熔断器配置
type CircuitBreakerCfg struct {
	MaxRequests         int `yaml:"maxRequests"`         // 半开状态最大请求数 (default: 5)
	IntervalS           int `yaml:"intervalSec"`         // 统计间隔秒数 (default: 60)
	TimeoutS            int `yaml:"timeoutSec"`          // 熔断恢复超时秒数 (default: 10)
	ConsecutiveFailures int `yaml:"consecutiveFailures"` // 连续失败触发熔断 (default: 5)
}

// SetDefaults fills zero-value fields with sensible defaults.
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
}

func (c *Config) ApplyEnv() {
	// Initialise secrets provider (Vault if configured, fallback to env)
	// P2-11 fix: Never use Vault.Token from config.yaml as a default — that would
	// allow the token to be committed to source control. Only the env var can set it.
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
	// P2-10: Support SERVER_ADDR override for container deployments (port binding)
	if v := conf.Env("SERVER_ADDR", ""); v != "" {
		c.Server.Addr = v
	}
}

func (c *Config) Validate() error {
	// P1-SEC-01 fix: JWT 密钥最小长度从 32 提高到 64 字符
	if len(c.JWT.Secret) < 64 {
		return fmt.Errorf("jwt secret must be at least 64 characters, got %d", len(c.JWT.Secret))
	}
	if c.Redis.Addr == "" {
		return fmt.Errorf("redis addr is required, set REDIS_ADDR env or config.yaml")
	}
	if len(c.Etcd.Addrs) == 0 {
		return fmt.Errorf("etcd addresses are required, set ETCD_ADDRS env or config.yaml")
	}
	mode := strings.ToLower(c.Mode)
	if mode != "dev" && mode != "prod" && mode != "staging" {
		return fmt.Errorf("invalid mode %q, expected dev/prod/staging", c.Mode)
	}
	// P2-08: Production requires Redis password
	if mode == "prod" && c.Redis.Password == "" {
		return fmt.Errorf("redis password is required in production mode, set REDIS_PASSWORD env")
	}
	return nil
}

// CircuitBreakerDurations returns the parsed interval and timeout durations.
func (c *Config) CircuitBreakerDurations() (interval, timeout time.Duration) {
	return time.Duration(c.CircuitBreaker.IntervalS) * time.Second,
		time.Duration(c.CircuitBreaker.TimeoutS) * time.Second
}
