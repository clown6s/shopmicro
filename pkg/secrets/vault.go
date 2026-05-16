package secrets

import (
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/vault/api"

	"shopmicro/pkg/logger"
)

// VaultConfig holds the connection parameters for HashiCorp Vault.
type VaultConfig struct {
	// Address is the Vault server URL (e.g. "http://10.211.55.5:8200").
	Address string `yaml:"address"`
	// Token is the Vault authentication token.
	Token string `yaml:"token"`
	// MountPath is the KV v2 secrets engine mount path (default: "secret").
	MountPath string `yaml:"mountPath"`
	// Namespace is an optional Vault namespace (for Vault Enterprise).
	Namespace string `yaml:"namespace"`
}

// VaultProvider implements the Provider interface backed by Vault KV v2.
type VaultProvider struct {
	client    *api.Client
	mountPath string
}

// NewVaultProvider creates a new VaultProvider and verifies connectivity.
// Returns an error if the Vault health check or token lookup fails.
func NewVaultProvider(cfg VaultConfig) (*VaultProvider, error) {
	if cfg.Address == "" {
		return nil, fmt.Errorf("vault: address is required")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("vault: token is required")
	}

	vaultCfg := api.DefaultConfig()
	vaultCfg.Address = cfg.Address

	// Configure timeout to prevent blocking during startup
	vaultCfg.Timeout = 5 * time.Second

	client, err := api.NewClient(vaultCfg)
	if err != nil {
		return nil, fmt.Errorf("vault: failed to create client: %w", err)
	}

	client.SetToken(cfg.Token)

	if cfg.Namespace != "" {
		client.SetNamespace(cfg.Namespace)
	}

	// Verify Vault is reachable and token is valid
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
	}, nil
}

// Get retrieves a secret value by key from Vault KV v2.
// key is the path relative to the mount path, e.g. "data/mysql" → reads "secret/data/mysql".
// Returns ("", false) if the key is not found or on any error.
func (v *VaultProvider) Get(key string) (string, bool) {
	secretPath := v.mountPath + "/data/" + key

	secret, err := v.client.Logical().Read(secretPath)
	if err != nil {
		logger.Warnf("secrets: vault read failed (path=%s): %v", secretPath, err)
		return "", false
	}
	if secret == nil || secret.Data == nil {
		return "", false
	}

	// KV v2 stores data under secret.Data["data"] (map[string]interface{})
	dataRaw, ok := secret.Data["data"]
	if !ok {
		return "", false
	}
	data, ok := dataRaw.(map[string]interface{})
	if !ok {
		return "", false
	}

	// Extract field name from key: "mysql/dsn" → "dsn", "db/password" → "password".
	// The full path is used for the Vault API call; only the last segment
	// is used to look up the value inside the data map (KV v2 nests secrets
	// under secret.Data["data"]["fieldName"]).
	parts := strings.Split(key, "/")
	fieldName := parts[len(parts)-1]

	val, ok := data[fieldName]
	if ok {
		if s, ok := val.(string); ok {
			return s, true
		}
		return fmt.Sprintf("%v", val), true
	}

	// Fallback: try "value" as a default field name (handles flat secret paths)
	if val, ok := data["value"]; ok {
		if s, ok := val.(string); ok {
			return s, true
		}
		return fmt.Sprintf("%v", val), true
	}

	return "", false
}

// Name returns the provider identifier.
func (v *VaultProvider) Name() string { return "vault" }

// Close cleans up Vault client resources.
func (v *VaultProvider) Close() {
	v.client.SetToken("")
	logger.Info("secrets: vault provider closed")
}
