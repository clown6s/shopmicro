package conf

import (
	"fmt"
	"strings"

	"shopmicro/pkg/conf"
	"shopmicro/pkg/redis"
	"shopmicro/pkg/secrets"
)

type MySQL struct {
	DSN string `yaml:"dsn"`
}

type JWT struct {
	Secret     string `yaml:"secret"`
	Expiration int    `yaml:"expiration"`
}

type Config struct {
	Server     conf.Server         `yaml:"server"`
	HealthAddr string              `yaml:"healthAddr"`
	Etcd       conf.Etcd           `yaml:"etcd"`
	MySQL      MySQL               `yaml:"mysql"`
	Redis      redis.Config        `yaml:"redis"`
	JWT        JWT                 `yaml:"jwt"`
	Mode       string              `yaml:"mode"`
	Vault      secrets.VaultConfig `yaml:"vault"`
}

// GetHealthAddr returns the health check listen address, defaulting to :8899.
func (c *Config) GetHealthAddr() string {
	if c.HealthAddr == "" {
		// Derive from service addr: if service is 127.0.0.1:8888, health is :8899
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
}

// Validate 校验必填的敏感配置
func (c *Config) Validate() error {
	if c.MySQL.DSN == "" {
		return fmt.Errorf("mysql dsn is required, set MYSQL_DSN env or config.yaml")
	}
	if c.JWT.Secret == "" {
		return fmt.Errorf("jwt secret is required, set JWT_SECRET env or config.yaml")
	}
	// P1-17: Ensure DSN has required parameters
	allParams := []struct{ key, defaultVal string }{
		{"parseTime", "true"},
		{"charset", "utf8mb4"},
	}
	for _, p := range allParams {
		if !strings.Contains(c.MySQL.DSN, p.key+"=") {
			c.MySQL.DSN += "&" + p.key + "=" + p.defaultVal
		}
	}
	// Ensure timeout params are present for production safety
	for _, p := range []string{"readTimeout", "writeTimeout", "timeout"} {
		if !strings.Contains(c.MySQL.DSN, p+"=") {
			c.MySQL.DSN += "&" + p + "=5s"
		}
	}
	return nil
}
