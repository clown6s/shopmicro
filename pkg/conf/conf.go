package conf

import (
	"fmt"
	"os"

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

// Load 加载配置文件。
// 优先级: 环境变量 > config.yaml（环境变量通过各服务 ApplyEnv() 覆盖）
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

	return nil
}

// Env 读取密钥值，从环境变量获取
func Env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
