package conf

import (
	"fmt"
	"time"

	"shopmicro/pkg/conf"
)

type Config struct {
	Server         conf.Server       `yaml:"server"`
	Etcd           conf.Etcd         `yaml:"etcd"`
	CORS           CORS              `yaml:"cors"`
	Redis          Redis             `yaml:"redis"`
	JWT            JWT               `yaml:"jwt"`
	ProtoBase      string            `yaml:"protoBase"`
	Mode           string            `yaml:"mode"`
	RateLimit      RateLimit         `yaml:"rateLimit"`
	CircuitBreaker CircuitBreakerCfg `yaml:"circuitBreaker"`
}

type CORS struct {
	AllowOrigins []string `yaml:"allowOrigins"`
}

type Redis struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type JWT struct {
	Secret     string `yaml:"secret"`
	Expiration int    `yaml:"expiration"`
}

// RateLimit 限流配置
type RateLimit struct {
	Rate     float64 `yaml:"rate"`       // 每秒允许的请求数 (default: 10)
	Burst    int     `yaml:"burst"`      // 突发上限 (default: 20)
	MaxIPs   int     `yaml:"maxIPs"`     // 最大追踪 IP 数 (default: 100000)
	CleanupS int     `yaml:"cleanupSec"` // 清理间隔秒数 (default: 60)
	IdleS    int     `yaml:"idleSec"`    // IP 空闲过期秒数 (default: 180)
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
	if rl.Rate <= 0 {
		rl.Rate = 10
	}
	if rl.Burst <= 0 {
		rl.Burst = 20
	}
	if rl.MaxIPs <= 0 {
		rl.MaxIPs = 100_000
	}
	if rl.CleanupS <= 0 {
		rl.CleanupS = 60
	}
	if rl.IdleS <= 0 {
		rl.IdleS = 180
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

// RateLimitDuration returns the parsed cleanup and idle durations.
func (c *Config) RateLimitDuration() (cleanup, idle time.Duration) {
	return time.Duration(c.RateLimit.CleanupS) * time.Second,
		time.Duration(c.RateLimit.IdleS) * time.Second
}

// CircuitBreakerDurations returns the parsed interval and timeout durations.
func (c *Config) CircuitBreakerDurations() (interval, timeout time.Duration) {
	return time.Duration(c.CircuitBreaker.IntervalS) * time.Second,
		time.Duration(c.CircuitBreaker.TimeoutS) * time.Second
}
