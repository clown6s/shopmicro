package conf

import (
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
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

	// 尝试加载 .env（工作目录 > config.yaml 同目录，文件不存在时不报错）
	godotenv.Load()                                          // ./  .env (working directory)
	godotenv.Load(filepath.Join(filepath.Dir(path), ".env")) // config dir .env
	return nil
}

// Env 读取环境变量，带默认值
func Env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
