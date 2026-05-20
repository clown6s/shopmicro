package secrets

import (
	"os"
	"sync/atomic"
)

// Provider 抽象了密钥来源（环境变量等）。
type Provider interface {
	// Get 根据 key 检索密钥值。
	// 如果未找到则返回 ("", false)。
	Get(key string) (string, bool)
	// Name 返回 provider 标识符（例如 "env"）。
	Name() string
}

// EnvProvider 从环境变量读取密钥。
// 这是默认 provider — 不需要外部依赖。
type EnvProvider struct{}

func (e *EnvProvider) Get(key string) (string, bool) {
	v := os.Getenv(key)
	return v, v != ""
}

func (e *EnvProvider) Name() string { return "env" }

// defaultProviderPtr 以 atomic.Value 安全的值存储当前 provider。
// 使用 loadDefaultProvider/storeDefaultProvider 进行并发安全访问。
var defaultProvider atomic.Value // 存储 Provider

func init() {
	defaultProvider.Store(EnvProviderInstance)
}

// EnvProviderInstance 是共享的单例 EnvProvider。
var EnvProviderInstance Provider = &EnvProvider{}

// loadDefaultProvider 以竞态安全的方式返回当前密钥 provider。
func loadDefaultProvider() Provider {
	if v := defaultProvider.Load(); v != nil {
		if p, ok := v.(Provider); ok {
			return p
		}
	}
	return EnvProviderInstance
}

// storeDefaultProvider 以竞态安全的方式设置全局密钥 provider。
func storeDefaultProvider(p Provider) {
	defaultProvider.Store(p)
}

// Get 使用当前默认 provider 检索密钥值。
func Get(key string) (string, bool) {
	return loadDefaultProvider().Get(key)
}
