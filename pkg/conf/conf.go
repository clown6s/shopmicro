package conf

import (
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"

	"shopmicro/pkg/logger"
	"shopmicro/pkg/secrets"
)

type Server struct {
	Addr string `yaml:"addr"`
}

type Etcd struct {
	Addrs []string `yaml:"addrs"`
}

type Base struct {
	Server Server `yaml:"server"`
	Etcd   Etcd   `yaml:"etcd"`
	Mode   string `yaml:"mode"`
}

// Load 加载配置文件，然后从 .env 文件覆盖敏感字段
// 优先级: 环境变量 > .env 文件 > config.yaml
func Load(path string, cfg interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return err
	}

	// M1 fix: Log errors from .env loading instead of silently ignoring them.
	// File-not-found is expected (no .env is fine), but parse errors matter.
	if err := godotenv.Load(); err != nil {
		if !os.IsNotExist(err) {
			logger.Warnf("conf: failed to load .env from working directory: %v", err)
		}
	}
	if err := godotenv.Load(filepath.Join(filepath.Dir(path), ".env")); err != nil {
		if !os.IsNotExist(err) {
			logger.Warnf("conf: failed to load .env from config dir: %v", err)
		}
	}
	return nil
}

// Env 读取密钥值，优先从 DefaultProvider（Vault）获取，回退到环境变量
func Env(key, fallback string) string {
	// Try the configured provider first (Vault if available)
	if v, ok := secrets.Get(key); ok {
		return v
	}
	// Fallback to raw environment variable
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
