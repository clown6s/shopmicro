package mysql

import (
	"fmt"
	"strings"
)

// Config 保存 MySQL 连接配置。
// 该结构体设计为嵌入到服务级别的 Config 结构体中。
// 注意：DSN 字段包含明文密码，String() 方法已脱敏处理。
type Config struct {
	DSN string `yaml:"dsn"`
	// 连接池参数（零值将回退到合理的默认值）
	MaxIdleConns    int `yaml:"maxIdleConns"`
	MaxOpenConns    int `yaml:"maxOpenConns"`
	ConnMaxLifetime int `yaml:"connMaxLifetimeSec"`
	ConnMaxIdleTime int `yaml:"connMaxIdleTimeSec"`
}

// String 返回脱敏后的 DSN（密码替换为 ***），防止日志泄漏。
func (c Config) String() string {
	dsn := c.DSN
	// 脱敏: user:password@ → user:***@
	if atIdx := strings.LastIndex(dsn, "@"); atIdx > 0 {
		if colonIdx := strings.Index(dsn[:atIdx], ":"); colonIdx >= 0 {
			dsn = dsn[:colonIdx+1] + "***" + dsn[atIdx:]
		}
	}
	return dsn
}

// hasDSNParam 检查是否存在带有正确边界感知的参数
// （例如 "?parseTime=" 或 "&parseTime="）以避免因
// 密码或值包含参数名作为子串而产生误判。
func hasDSNParam(dsn, param string) bool {
	return strings.Contains(dsn, "?"+param+"=") || strings.Contains(dsn, "&"+param+"=")
}

// Validate 检查必填字段是否存在并添加安全的默认值。
func (c *Config) Validate() error {
	if c.DSN == "" {
		return fmt.Errorf("mysql dsn is required")
	}

	sep := "&"
	if !strings.Contains(c.DSN, "?") {
		sep = "?"
	}

	// P1-17: 确保 DSN 包含必要参数
	allParams := []struct{ key, defaultVal string }{
		{"parseTime", "true"},
		{"charset", "utf8mb4"},
	}
	for _, p := range allParams {
		if !hasDSNParam(c.DSN, p.key) {
			c.DSN += sep + p.key + "=" + p.defaultVal
			sep = "&"
		}
	}

	// 确保存在超时参数以保障生产环境安全。
	// 默认 10s：在重查询和网络延迟下提供合理余量。
	for _, p := range []string{"readTimeout", "writeTimeout", "timeout"} {
		if !hasDSNParam(c.DSN, p) {
			c.DSN += sep + p + "=10s"
			sep = "&"
		}
	}

	// 用默认值填充零值的连接池参数
	if c.MaxIdleConns <= 0 {
		c.MaxIdleConns = 10
	}
	if c.MaxOpenConns <= 0 {
		c.MaxOpenConns = 100
	}
	// P2-06: 将 MaxIdleConns 限制为不超过 MaxOpenConns，防止配置错误
	if c.MaxIdleConns > c.MaxOpenConns {
		c.MaxIdleConns = c.MaxOpenConns
	}
	if c.ConnMaxLifetime <= 0 {
		c.ConnMaxLifetime = 3600 // 1 小时
	}
	if c.ConnMaxIdleTime <= 0 {
		c.ConnMaxIdleTime = 600 // 10 分钟
	}

	return nil
}
