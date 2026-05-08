package conf

import (
	"fmt"

	"shopmicro/pkg/conf"
)

type Config struct {
	Server    conf.Server `yaml:"server"`
	Etcd      conf.Etcd   `yaml:"etcd"`
	CORS      CORS        `yaml:"cors"`
	Redis     Redis       `yaml:"redis"`
	JWT       JWT         `yaml:"jwt"`
	ProtoBase string      `yaml:"protoBase"` // proto 文件基础路径，默认 "../"
	Mode      string      `yaml:"mode"`
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
