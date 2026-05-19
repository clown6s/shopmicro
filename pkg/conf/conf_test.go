package conf

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoad_YAML loads a valid YAML file and verifies values are unmarshalled.
func TestLoad_YAML(t *testing.T) {
	dir := t.TempDir()

	cfgPath := filepath.Join(dir, "config.yaml")
	content := []byte("mode: dev\nserver:\n  addr: :8080\netcd:\n  addrs:\n    - localhost:2379\n")
	if err := os.WriteFile(cfgPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	var cfg struct {
		Mode   string `yaml:"mode"`
		Server struct {
			Addr string `yaml:"addr"`
		} `yaml:"server"`
	}
	if err := Load(cfgPath, &cfg); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Mode != "dev" {
		t.Errorf("Mode = %q, want %q", cfg.Mode, "dev")
	}
	if cfg.Server.Addr != ":8080" {
		t.Errorf("Server.Addr = %q, want %q", cfg.Server.Addr, ":8080")
	}
}

// TestLoad_MissingFile returns error when config file does not exist.
func TestLoad_MissingFile(t *testing.T) {
	var cfg struct{}
	err := Load("/nonexistent/path/config.yaml", &cfg)
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

// TestLoad_EmptyPath returns error.
func TestLoad_EmptyPath(t *testing.T) {
	var cfg struct{}
	err := Load("", &cfg)
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

// TestLoad_NilCfg returns error.
func TestLoad_NilCfg(t *testing.T) {
	err := Load("/some/path.yaml", nil)
	if err == nil {
		t.Fatal("expected error for nil cfg")
	}
}

// TestLoad_DotEnvIgnored verifies .env is optional (non-fatal when missing).
func TestLoad_DotEnvIgnored(t *testing.T) {
	dir := t.TempDir()

	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("mode: prod\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var cfg struct {
		Mode string `yaml:"mode"`
	}
	if err := Load(cfgPath, &cfg); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Mode != "prod" {
		t.Errorf("Mode = %q, want %q", cfg.Mode, "prod")
	}
}

// TestEnv_EnvironmentVariable reads value from environment variable.
func TestEnv_EnvironmentVariable(t *testing.T) {
	t.Setenv("TEST_CONF_KEY", "env_value")
	got := Env("TEST_CONF_KEY", "default")
	if got != "env_value" {
		t.Errorf("Env() = %q, want %q", got, "env_value")
	}
}

// TestEnv_Fallback returns fallback when key is not set.
func TestEnv_Fallback(t *testing.T) {
	got := Env("NONEXISTENT_CONF_KEY_XYZ", "fallback_val")
	if got != "fallback_val" {
		t.Errorf("Env() = %q, want %q", got, "fallback_val")
	}
}

// TestEnv_EmptyEnvWithFallback: os.LookupEnv returns true for empty string,
// so Env returns "" (the actual value) not the fallback.
func TestEnv_EmptyEnvWithFallback(t *testing.T) {
	t.Setenv("EMPTY_CONF_KEY_XYZ", "")
	got := Env("EMPTY_CONF_KEY_XYZ", "fallback")
	if got != "" {
		t.Errorf("Env() = %q, want %q (empty string when env is set but empty)", got, "")
	}
}
