package conf

import (
	"fmt"
	"log"
	"strings"

	"shopmicro/pkg/conf"
	"shopmicro/pkg/redis"
	"shopmicro/pkg/secrets"
)

type Config struct {
	Server    conf.Server         `yaml:"server"`
	Etcd      conf.Etcd           `yaml:"etcd"`
	CORS      CORS                `yaml:"cors"`
	Redis     redis.Config        `yaml:"redis"`
	JWT       JWT                 `yaml:"jwt"`
	ProtoBase string              `yaml:"protoBase"`
	Mode      string              `yaml:"mode"`
	RateLimit RateLimit           `yaml:"rateLimit"`
	Vault     secrets.VaultConfig `yaml:"vault"`
	OTEL      OTEL                `yaml:"otel"`
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
	if c.OTEL.SampleRate <= 0 {
		if c.Mode == "dev" {
			c.OTEL.SampleRate = 1.0
		} else {
			c.OTEL.SampleRate = 0.1
		}
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
			log.Printf("[WARN] vault address is set but VAULT_TOKEN env var is missing — vault init will fail")
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
		if epCfg.WindowSec < 1 {
			return fmt.Errorf("rate limit endpoint %q: windowSec must be >= 1, got %d", epKey, epCfg.WindowSec)
		}
	}
	if c.JWT.Issuer == "" && c.Mode == "prod" {
		log.Printf("[WARN] jwt issuer is empty in production mode — tokens from any issuer will be accepted")
	}
	return nil
}
