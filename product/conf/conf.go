package conf

import (
	"fmt"
	"strings"

	"shopmicro/pkg/conf"
	"shopmicro/pkg/mysql"
)

type Config struct {
	Server     conf.Server  `yaml:"server"`
	HealthAddr string       `yaml:"healthAddr"`
	Etcd       conf.Etcd    `yaml:"etcd"`
	MySQL      mysql.Config `yaml:"mysql"`
	Mode       string       `yaml:"mode"`
	Auth       AuthConfig   `yaml:"auth"`
}

// AuthConfig 鉴权配置。
// JWT: 网关转发客户端 token 时使用（gateway 已校验，product 二次验证）。
// InternalToken: 微服务间直接调用时的 API Key。
type AuthConfig struct {
	JWTSecret     string `yaml:"jwtSecret"`
	InternalToken string `yaml:"internalToken"`
}

// GetHealthAddr 返回健康检查监听地址，默认为 :8897
func (c *Config) GetHealthAddr() string {
	if c.HealthAddr == "" {
		return ":8897"
	}
	return c.HealthAddr
}

// ApplyEnv 从环境变量覆盖敏感配置
func (c *Config) ApplyEnv() {
	if v := conf.Env("MODE", ""); v != "" {
		c.Mode = strings.ToLower(v)
	}
	if v := conf.Env("MYSQL_DSN", ""); v != "" {
		c.MySQL.DSN = v
	}

	if v := conf.Env("SERVER_ADDR", ""); v != "" {
		c.Server.Addr = v
	}
	if v := conf.Env("ETCD_ADDRS", ""); v != "" {
		c.Etcd.Addrs = strings.Split(v, ",")
	}
	if v := conf.Env("JWT_SECRET", ""); v != "" {
		c.Auth.JWTSecret = v
	}
}

// Validate 校验必填配置
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
	return nil
}
