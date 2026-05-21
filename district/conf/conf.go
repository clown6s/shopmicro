package conf

import (
	"fmt"
	"strings"

	"shopmicro/pkg/conf"
	"shopmicro/pkg/mysql"
)

type AMapConfig struct {
	Key     string `yaml:"key"`     // 高德地图 Web服务 API Key
	Timeout int    `yaml:"timeout"` // HTTP 请求超时（秒），默认 120
}

type Config struct {
	Server     conf.Server  `yaml:"server"`
	HealthAddr string       `yaml:"healthAddr"`
	Etcd       conf.Etcd    `yaml:"etcd"`
	MySQL      mysql.Config `yaml:"mysql"`
	AMap       AMapConfig   `yaml:"amap"`
	Mode       string       `yaml:"mode"`
}

// GetHealthAddr 返回健康检查监听地址，默认为 :8898（避免与 user 的 :8899 冲突）。
func (c *Config) GetHealthAddr() string {
	if c.HealthAddr == "" {
		return ":8898"
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
	if v := conf.Env("AMAP_KEY", ""); v != "" {
		c.AMap.Key = v
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
	return nil
}
