package mysql

import (
	"fmt"
	"strings"
)

// Config holds MySQL connection configuration.
// This struct is designed to be embedded in service-level Config structs.
type Config struct {
	DSN string `yaml:"dsn"`
	// Pool parameters (zero-value falls back to sensible defaults)
	MaxIdleConns    int `yaml:"maxIdleConns"`
	MaxOpenConns    int `yaml:"maxOpenConns"`
	ConnMaxLifetime int `yaml:"connMaxLifetimeSec"`
	ConnMaxIdleTime int `yaml:"connMaxIdleTimeSec"`
}

// Validate checks that required fields are present and adds safe defaults.
func (c *Config) Validate() error {
	if c.DSN == "" {
		return fmt.Errorf("mysql dsn is required")
	}

	sep := "&"
	if !strings.Contains(c.DSN, "?") {
		sep = "?"
	}

	// P1-17: Ensure DSN has required parameters
	allParams := []struct{ key, defaultVal string }{
		{"parseTime", "true"},
		{"charset", "utf8mb4"},
	}
	for _, p := range allParams {
		if !strings.Contains(c.DSN, p.key+"=") {
			c.DSN += sep + p.key + "=" + p.defaultVal
			sep = "&"
		}
	}

	// Ensure timeout params are present for production safety.
	// R4-P1-04: Use boundary-aware check — "readTimeout" contains "timeout"
	// so a naive Contains check would skip adding the "timeout" (connect timeout) param.
	hasDSNParam := func(dsn, param string) bool {
		return strings.Contains(dsn, "?"+param+"=") || strings.Contains(dsn, "&"+param+"=")
	}
	for _, p := range []string{"readTimeout", "writeTimeout", "timeout"} {
		if !hasDSNParam(c.DSN, p) {
			c.DSN += sep + p + "=5s"
			sep = "&"
		}
	}

	// Fill zero-value pool parameters with defaults
	if c.MaxIdleConns <= 0 {
		c.MaxIdleConns = 10
	}
	if c.MaxOpenConns <= 0 {
		c.MaxOpenConns = 100
	}
	if c.ConnMaxLifetime <= 0 {
		c.ConnMaxLifetime = 3600 // 1 hour
	}
	if c.ConnMaxIdleTime <= 0 {
		c.ConnMaxIdleTime = 600 // 10 minutes
	}

	return nil
}
