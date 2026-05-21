package conf

import (
	"fmt"
	"strings"

	"shopmicro/pkg/conf"
	"shopmicro/pkg/mysql"
	"shopmicro/pkg/redis"
)

type JWT struct {
	Secret        string `yaml:"secret"`
	RefreshSecret string `yaml:"refreshSecret"` // 可选：刷新令牌的独立密钥（回退使用 Secret）
	Expiration    int    `yaml:"expiration"`
	Issuer        string `yaml:"issuer"`
}

// Security 限流与安全策略配置
type Security struct {
	// 注册限流：按手机号
	RegisterLimitMax    int `yaml:"registerLimitMax"`    // 窗口内最大注册次数 (default: 5)
	RegisterLimitWindow int `yaml:"registerLimitWindow"` // 窗口时长秒 (default: 3600)
	// RefreshToken 限流：按用户 ID
	RefreshLimitMax    int `yaml:"refreshLimitMax"`    // 窗口内最大刷新次数 (default: 10)
	RefreshLimitWindow int `yaml:"refreshLimitWindow"` // 窗口时长秒 (default: 60)
	// 暴力破解保护
	LoginMaxFailures int `yaml:"loginMaxFailures"` // 锁定前最大失败次数 (default: 10)
	LoginLockoutSec  int `yaml:"loginLockoutSec"`  // 锁定时长秒 (default: 900)
	// 密码策略
	PasswordMinLen     int `yaml:"passwordMinLen"`     // 最短密码长度 (default: 8)
	PasswordMaxLen     int `yaml:"passwordMaxLen"`     // 最长密码长度 (default: 128)
	PasswordBcryptCost int `yaml:"passwordBcryptCost"` // bcrypt cost (default: 12)
	// Token rotation grace period：GETDEL 后进程崩溃的恢复窗口秒数 (default: 5)
	TokenRotationGracePeriodSec int `yaml:"tokenRotationGracePeriodSec"`
}

type Config struct {
	Server     conf.Server  `yaml:"server"`
	HealthAddr string       `yaml:"healthAddr"`
	RegistryIP string       `yaml:"registryIP"` // 注册到 etcd 的 IP，为空则 Kitex 自动检测
	Etcd       conf.Etcd    `yaml:"etcd"`
	MySQL      mysql.Config `yaml:"mysql"`
	Redis      redis.Config `yaml:"redis"`
	JWT        JWT          `yaml:"jwt"`
	Mode       string       `yaml:"mode"`
	Security   Security     `yaml:"security"`
}

// GetHealthAddr 返回健康检查监听地址，默认为 :8899。
func (c *Config) GetHealthAddr() string {
	if c.HealthAddr == "" {
		return ":8899"
	}
	return c.HealthAddr
}

// SetDefaults 为零值安全配置字段填充默认值。
func (c *Config) SetDefaults() {
	s := &c.Security
	if s.RegisterLimitMax <= 0 {
		s.RegisterLimitMax = 5
	}
	if s.RegisterLimitWindow <= 0 {
		s.RegisterLimitWindow = 3600
	}
	if s.RefreshLimitMax <= 0 {
		s.RefreshLimitMax = 10
	}
	if s.RefreshLimitWindow <= 0 {
		s.RefreshLimitWindow = 60
	}
	if s.LoginMaxFailures <= 0 {
		s.LoginMaxFailures = 10
	}
	if s.LoginLockoutSec <= 0 {
		s.LoginLockoutSec = 900
	}
	if s.PasswordMinLen <= 0 {
		s.PasswordMinLen = 8
	}
	if s.PasswordMaxLen <= 0 {
		s.PasswordMaxLen = 128
	}
	if s.PasswordBcryptCost <= 0 {
		s.PasswordBcryptCost = 12
	}
	// Token rotation grace period：崩溃恢复窗口，默认 5 秒足够覆盖 99.9% 的 RPC 超时重试
	if s.TokenRotationGracePeriodSec <= 0 {
		s.TokenRotationGracePeriodSec = 5
	}
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
	if v := conf.Env("JWT_REFRESH_SECRET", ""); v != "" {
		c.JWT.RefreshSecret = v
	}
	if v := conf.Env("SERVER_ADDR", ""); v != "" {
		c.Server.Addr = v
	}
	if v := conf.Env("ETCD_ADDRS", ""); v != "" {
		c.Etcd.Addrs = strings.Split(v, ",")
	}
	if v := conf.Env("KITEX_IP_TO_REGISTRY", ""); v != "" {
		c.RegistryIP = v
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
	// Security 参数校验
	s := c.Security
	if s.PasswordBcryptCost < 4 || s.PasswordBcryptCost > 31 {
		return fmt.Errorf("security.passwordBcryptCost must be between 4 and 31, got %d", s.PasswordBcryptCost)
	}
	if s.PasswordMinLen > s.PasswordMaxLen {
		return fmt.Errorf("security.passwordMinLen (%d) must not exceed passwordMaxLen (%d)", s.PasswordMinLen, s.PasswordMaxLen)
	}
	return nil
}
