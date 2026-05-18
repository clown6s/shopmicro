package conf

import (
	"fmt"
	"strings"

	"shopmicro/pkg/conf"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/mysql"
	"shopmicro/pkg/redis"
	"shopmicro/pkg/secrets"
)

type JWT struct {
	Secret        string `yaml:"secret"`
	RefreshSecret string `yaml:"refreshSecret"` // 可选：刷新令牌的独立密钥（回退使用 Secret）
	Expiration    int    `yaml:"expiration"`
	Issuer        string `yaml:"issuer"`
}

type Config struct {
	Server     conf.Server         `yaml:"server"`
	HealthAddr string              `yaml:"healthAddr"`
	Etcd       conf.Etcd           `yaml:"etcd"`
	MySQL      mysql.Config        `yaml:"mysql"`
	Redis      redis.Config        `yaml:"redis"`
	JWT        JWT                 `yaml:"jwt"`
	Mode       string              `yaml:"mode"`
	Vault      secrets.VaultConfig `yaml:"vault"`
}

// GetHealthAddr 返回健康检查监听地址，默认为 :8899。
func (c *Config) GetHealthAddr() string {
	if c.HealthAddr == "" {
		return ":8899"
	}
	return c.HealthAddr
}

// ApplyEnv 从环境变量覆盖敏感配置
func (c *Config) ApplyEnv() {
	// P2-11 修复：绝不使用 config.yaml 中的 Vault.Token 作为默认值。
	if c.Vault.Address != "" {
		if token := conf.Env("VAULT_TOKEN", ""); token != "" {
			c.Vault.Token = token
		}
		if c.Vault.Token == "" {
			logger.Warnf("vault address is set but VAULT_TOKEN env var is missing — vault init will fail")
		}
		secrets.InitProvider(c.Vault)
	}

	if v := conf.Env("MYSQL_DSN", ""); v != "" {
		c.MySQL.DSN = v
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
	if v := conf.Env("JWT_REFRESH_SECRET", ""); v != "" {
		c.JWT.RefreshSecret = v
	}
	if v := conf.Env("SERVER_ADDR", ""); v != "" {
		c.Server.Addr = v
	}
	if v := conf.Env("ETCD_ADDRS", ""); v != "" {
		c.Etcd.Addrs = strings.Split(v, ",")
	}
}

// Validate 校验必填的敏感配置
func (c *Config) Validate() error {
	if err := c.MySQL.Validate(); err != nil {
		return err
	}
	if c.JWT.Secret == "" {
		return fmt.Errorf("jwt secret is required, set JWT_SECRET env or config.yaml")
	}
	// P1-01 修复：JWT 密钥长度与 gateway/pkg/jwt 保持一致
	// pkg/jwt/jwt.go NewGenerator/NewParser 要求 64 字符
	minSecretLen := 64
	if len(c.JWT.Secret) < minSecretLen {
		return fmt.Errorf("jwt secret must be at least %d characters, got %d", minSecretLen, len(c.JWT.Secret))
	}
	// P1-01 修复：Refresh token secret 同样校验
	if c.JWT.RefreshSecret != "" && len(c.JWT.RefreshSecret) < minSecretLen {
		return fmt.Errorf("jwt refresh secret must be at least %d characters, got %d", minSecretLen, len(c.JWT.RefreshSecret))
	}
	// P1-01 修复：RefreshSecret 回退密钥安全 — 如果使用回退逻辑，实际使用的是主密钥，
	// 长度已在上面校验过了，无需额外处理
	mode := strings.ToLower(c.Mode)
	if mode != "dev" && mode != "prod" && mode != "staging" {
		return fmt.Errorf("invalid mode %q, expected dev/prod/staging", c.Mode)
	}
	// P2-08：生产环境要求 Redis 密码
	if mode == "prod" && c.Redis.Password == "" {
		return fmt.Errorf("redis password is required in production mode, set REDIS_PASSWORD env")
	}
	return nil
}
