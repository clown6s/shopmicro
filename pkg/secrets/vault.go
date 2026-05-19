package secrets

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/vault/api"

	"shopmicro/pkg/logger"
)

// VaultConfig 保存 HashiCorp Vault 的连接参数。
type VaultConfig struct {
	// Address 是 Vault 服务器 URL（例如 "http://10.211.55.5:8200"）。
	Address string `yaml:"address"`
	// Token 是 Vault 认证令牌。
	Token string `yaml:"token"`
	// MountPath 是 KV v2 密钥引擎的挂载路径（默认："secret"）。
	MountPath string `yaml:"mountPath"`
	// Namespace 是可选的 Vault 命名空间（用于 Vault Enterprise）。
	Namespace string `yaml:"namespace"`
}

// Validate 检查 Vault key 的合法性，防止路径穿越攻击。
func validateVaultKey(key string) error {
	if key == "" {
		return fmt.Errorf("vault: key must not be empty")
	}
	if strings.Contains(key, "..") || strings.Contains(key, "\x00") {
		return fmt.Errorf("vault: key contains invalid characters (path traversal detected)")
	}
	if key[0] == '/' {
		return fmt.Errorf("vault: key must not start with /")
	}
	return nil
}

// VaultProvider 实现了基于 Vault KV v2 的 Provider 接口。
type VaultProvider struct {
	client    *api.Client
	mountPath string
	httpC     *http.Client // 缓存的引用，用于清理
	closeOnce sync.Once
}

// NewVaultProvider 创建新的 VaultProvider 并验证连通性。
// 如果 Vault 健康检查或令牌查询失败则返回错误。
//
// 注意：Vault token 无自动续期机制。token 过期后 Get() 会返回 ("", false)。
// 请使用长期有效的 periodic token（而非一次性 token），并确保 TTL 足够长。
func NewVaultProvider(cfg VaultConfig) (*VaultProvider, error) {
	if cfg.Address == "" {
		return nil, fmt.Errorf("vault: address is required")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("vault: token is required")
	}

	vaultCfg := api.DefaultConfig()
	vaultCfg.Address = cfg.Address

	// 配置超时以防止启动期间阻塞
	vaultCfg.Timeout = 5 * time.Second

	client, err := api.NewClient(vaultCfg)
	if err != nil {
		return nil, fmt.Errorf("vault: failed to create client: %w", err)
	}

	client.SetToken(cfg.Token)

	if cfg.Namespace != "" {
		client.SetNamespace(cfg.Namespace)
	}

	// 验证 Vault 可达且令牌有效
	health, err := client.Sys().Health()
	if err != nil {
		return nil, fmt.Errorf("vault: health check failed (address=%s): %w", cfg.Address, err)
	}
	if !health.Initialized {
		return nil, fmt.Errorf("vault: server is not initialized")
	}
	if health.Sealed {
		return nil, fmt.Errorf("vault: server is sealed")
	}

	mountPath := cfg.MountPath
	if mountPath == "" {
		mountPath = "secret"
	}

	logger.Infof("secrets: vault provider initialised (address=%s, mount=%s, namespace=%s)",
		cfg.Address, mountPath, cfg.Namespace)

	return &VaultProvider{
		client:    client,
		mountPath: mountPath,
		httpC:     vaultCfg.HttpClient,
	}, nil
}

// Get 从 Vault KV v2 根据 key 检索密钥值。
// key 是相对于挂载路径的路径，例如 "data/mysql" → 读取 "secret/data/mysql"。
// 如果未找到 key 或发生任何错误则返回 ("", false)。
func (v *VaultProvider) Get(key string) (string, bool) {
	if err := validateVaultKey(key); err != nil {
		logger.Warnf("secrets: %v (key=%q)", err, key)
		return "", false
	}
	secretPath := v.mountPath + "/data/" + key

	secret, err := v.client.Logical().Read(secretPath)
	if err != nil {
		logger.Warnf("secrets: vault read failed (path=%s): %v", secretPath, err)
		return "", false
	}
	if secret == nil || secret.Data == nil {
		return "", false
	}

	// KV v2 将数据存储在 secret.Data["data"] 下 (map[string]interface{})
	dataRaw, ok := secret.Data["data"]
	if !ok {
		return "", false
	}
	data, ok := dataRaw.(map[string]interface{})
	if !ok {
		return "", false
	}

	// 从 key 中提取字段名："mysql/dsn" → "dsn"，"db/password" → "password"。
	// 完整路径用于 Vault API 调用；仅最后一段
	// 用于在 data map 中查找值（KV v2 将密钥
	// 嵌套在 secret.Data["data"]["fieldName"] 下）。
	parts := strings.Split(key, "/")
	fieldName := parts[len(parts)-1]

	val, ok := data[fieldName]
	if ok {
		if s, ok := val.(string); ok {
			return s, true
		}
		// P1-11: 非字符串值（number、bool、map）被 fmt.Sprintf 强制转换，
		// 记录警告以便发现 Vault 中的数据类型异常。
		logger.Warnf("secrets: vault value for key=%q field=%q is %T (expected string), coercing with %%v",
			key, fieldName, val)
		return fmt.Sprintf("%v", val), true
	}

	// 回退：尝试 "value" 作为默认字段名（处理扁平密钥路径）
	if val, ok := data["value"]; ok {
		if s, ok := val.(string); ok {
			return s, true
		}
		logger.Warnf("secrets: vault value for key=%q field='value' is %T (expected string), coercing with %%v",
			key, val)
		return fmt.Sprintf("%v", val), true
	}

	return "", false
}

// Name 返回 provider 标识符。
func (v *VaultProvider) Name() string { return "vault" }

// Close 清理 Vault 客户端资源（清除令牌并关闭空闲 HTTP 连接）。
// 线程安全：多次调用只执行一次。
func (v *VaultProvider) Close() {
	v.closeOnce.Do(func() {
		v.client.SetToken("")
		if v.httpC != nil {
			v.httpC.CloseIdleConnections()
		}
		logger.Info("secrets: vault provider closed")
	})
}
