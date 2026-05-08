package conf

import (
	"fmt"

	"shopmicro/pkg/conf"
)

type MySQL struct {
	DSN string `yaml:"dsn"`
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

type Config struct {
	Server conf.Server `yaml:"server"`
	Etcd   conf.Etcd   `yaml:"etcd"`
	MySQL  MySQL       `yaml:"mysql"`
	Redis  Redis       `yaml:"redis"`
	JWT    JWT         `yaml:"jwt"`
	Mode   string      `yaml:"mode"`
}

// ApplyEnv 从环境变量覆盖敏感配置
func (c *Config) ApplyEnv() {
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
	return nil
}
