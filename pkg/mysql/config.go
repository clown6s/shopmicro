package mysql

import (
	"fmt"
	"strings"
)

// Config holds MySQL connection configuration.
// This struct is designed to be embedded in service-level Config structs.
type Config struct {
	DSN string `yaml:"dsn"`
}

// Validate checks that required fields are present and adds safe defaults.
func (c *Config) Validate() error {
	if c.DSN == "" {
		return fmt.Errorf("mysql dsn is required")
	}

	// P1-17: Ensure DSN has required parameters
	allParams := []struct{ key, defaultVal string }{
		{"parseTime", "true"},
		{"charset", "utf8mb4"},
	}
	for _, p := range allParams {
		if !strings.Contains(c.DSN, p.key+"=") {
			c.DSN += "&" + p.key + "=" + p.defaultVal
		}
	}

	// Ensure timeout params are present for production safety
	for _, p := range []string{"readTimeout", "writeTimeout", "timeout"} {
		if !strings.Contains(c.DSN, p+"=") {
			c.DSN += "&" + p + "=5s"
		}
	}

	return nil
}
