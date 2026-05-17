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

type Config struct {
	Server     conf.Server         `yaml:"server"`
	HealthAddr string              `yaml:"healthAddr"`
	Etcd       conf.Etcd           `yaml:"etcd"`
	MySQL      mysql.Config        `yaml:"mysql"`
	Redis      redis.Config        `yaml:"redis"`
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
	// P2-11 修复：永远不要使用 config.yaml 中的 Vault.Token 作为默认值。
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

	mode := strings.ToLower(c.Mode)
	if mode != "dev" && mode != "prod" && mode != "staging" {
		return fmt.Errorf("invalid mode %q, expected dev/prod/staging", c.Mode)
	}
	if len(c.Etcd.Addrs) == 0 {
		return fmt.Errorf("etcd addresses are required, set ETCD_ADDRS env or config.yaml")
	}
	// P2-08：生产环境需要 Redis 密码
	if mode == "prod" && c.Redis.Password == "" {
		return fmt.Errorf("redis password is required in production mode, set REDIS_PASSWORD env")
	}
	return nil
}
