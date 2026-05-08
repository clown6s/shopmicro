package conf

import (
	"fmt"

	"shopmicro/pkg/conf"
)

type ServiceRoute struct {
	Name      string `yaml:"name"`      // 下游服务名
	HostPorts string `yaml:"hostPorts"` // 直连地址（服务发现失败时使用，空则仅走服务发现）
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
	Secret string `yaml:"secret"`
}

type Config struct {
	Server        conf.Server    `yaml:"server"`
	Etcd          conf.Etcd      `yaml:"etcd"`
	CORS          CORS           `yaml:"cors"`
	Redis         Redis          `yaml:"redis"`
	JWT           JWT            `yaml:"jwt"`
	ServiceRoutes []ServiceRoute `yaml:"serviceRoutes"`
	Mode          string         `yaml:"mode"`
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

// GetHostPorts 获取服务直连地址
func (c *Config) GetHostPorts(serviceName string) string {
	for _, s := range c.ServiceRoutes {
		if s.Name == serviceName {
			return s.HostPorts
		}
	}
	return ""
}
