package conf

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"

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
	if path == "" {
		return fmt.Errorf("conf: config path is required")
	}
	if cfg == nil {
		return fmt.Errorf("conf: cfg must not be nil")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return err
	}

	// .env 加载是可选的：文件不存在不报错，但解析失败记 warn
	if err := godotenv.Load(); err != nil {
		log.Printf("[conf] .env not found in working directory (non-fatal)")
	}
	if err := godotenv.Load(filepath.Join(filepath.Dir(path), ".env")); err != nil {
		log.Printf("[conf] .env not found in config directory (non-fatal)")
	}
	return nil
}

// Env 读取密钥值，优先从 DefaultProvider（Vault）获取，回退到环境变量
func Env(key, fallback string) string {
	// 先尝试已配置的 provider（如果可用则为 Vault）
	if v, ok := secrets.Get(key); ok {
		return v
	}
	// 回退到原始环境变量
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
