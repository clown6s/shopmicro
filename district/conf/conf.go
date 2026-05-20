package conf

import (
	"fmt"
	"strings"

	"shopmicro/pkg/conf"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/mysql"
	"shopmicro/pkg/secrets"
)

type AMapConfig struct {
	Key     string `yaml:"key"`     // 高德地图 Web服务 API Key
	Timeout int    `yaml:"timeout"` // HTTP 请求超时（秒），默认 120
}

type Config struct {
	Server     conf.Server         `yaml:"server"`
	HealthAddr string              `yaml:"healthAddr"`
	Etcd       conf.Etcd           `yaml:"etcd"`
	MySQL      mysql.Config        `yaml:"mysql"`
	AMap       AMapConfig          `yaml:"amap"`
	Mode       string              `yaml:"mode"`
	Vault      secrets.VaultConfig `yaml:"vault"`
	Auth       AuthConfig          `yaml:"auth"`
}

// AuthConfig 鉴权配置。
// JWT: 网关转发客户端 token 时使用（gateway 已校验，district 二次验证）。
// InternalToken: 微服务间直接调用时的 API Key（比 JWT 更轻量）。
type AuthConfig struct {
	JWTSecret     string `yaml:"jwtSecret"`     // HMAC 密钥（与 gateway/user 共享），空则禁用 JWT 鉴权
	InternalToken string `yaml:"internalToken"` // 服务间 API Key，空则禁用
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
	// MySQL DSN 优先从环境变量获取（.env 或系统环境变量）
	if c.MySQL.DSN == "" {
		c.MySQL.DSN = conf.Env("MYSQL_DSN", "")
	}

	// P2-11 修复：永远不要使用 config.yaml 中的 Vault.Token 作为默认值。
	if c.Vault.Address != "" {
		if token := conf.Env("VAULT_TOKEN", ""); token != "" {
			c.Vault.Token = token
		}
		if c.Vault.Token == "" {
			logger.Warnf("vault address is set but VAULT_TOKEN env var is missing — skipping vault init")
		} else {
			secrets.InitProvider(c.Vault)
		}
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
