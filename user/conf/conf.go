package conf

import (
	"fmt"
	"strings"

	"shopmicro/pkg/conf"
	"shopmicro/pkg/mysql"
	"shopmicro/pkg/redis"
	"shopmicro/pkg/secrets"
)

type JWT struct {
	Secret     string `yaml:"secret"`
	Expiration int    `yaml:"expiration"`
	Issuer     string `yaml:"issuer"`
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

// GetHealthAddr returns the health check listen address, defaulting to :8899.
func (c *Config) GetHealthAddr() string {
	if c.HealthAddr == "" {
		return ":8899"
	}
	return c.HealthAddr
}

// ApplyEnv 从环境变量覆盖敏感配置
func (c *Config) ApplyEnv() {
	// Initialise secrets provider (Vault if configured, fallback to env)
	if c.Vault.Address != "" {
		if token := conf.Env("VAULT_TOKEN", c.Vault.Token); token != "" {
			c.Vault.Token = token
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
	// R4-P3-01: Mode 校验，与 Gateway 保持一致
	mode := strings.ToLower(c.Mode)
	if mode != "dev" && mode != "prod" && mode != "staging" {
		return fmt.Errorf("invalid mode %q, expected dev/prod/staging", c.Mode)
	}
	return nil
}
