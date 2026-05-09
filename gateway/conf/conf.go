package conf

import (
	"fmt"
	"time"

	"shopmicro/pkg/conf"
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
	AllowOrigins []string `yaml:"allowOrigins"`
}

type JWT struct {
	Secret     string `yaml:"secret"`
	Expiration int    `yaml:"expiration"`
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
	if c.Vault.Address != "" {
		if token := conf.Env("VAULT_TOKEN", c.Vault.Token); token != "" {
			c.Vault.Token = token
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
}

func (c *Config) Validate() error {
	if c.JWT.Secret == "" {
		return fmt.Errorf("jwt secret is required, set JWT_SECRET env or config.yaml")
	}
	return nil
}

// CircuitBreakerDurations returns the parsed interval and timeout durations.
func (c *Config) CircuitBreakerDurations() (interval, timeout time.Duration) {
	return time.Duration(c.CircuitBreaker.IntervalS) * time.Second,
		time.Duration(c.CircuitBreaker.TimeoutS) * time.Second
}
