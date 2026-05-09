package secrets

import (
	"os"
	"sync/atomic"

	"shopmicro/pkg/logger"
)

// Provider abstracts a secret source (env, Vault, AWS SM, etc.).
type Provider interface {
	// Get retrieves a secret value by key.
	// Returns ("", false) if the key is not found.
	Get(key string) (string, bool)
	// Name returns the provider identifier (e.g., "env", "vault", "awssm").
	Name() string
}

// EnvProvider reads secrets from environment variables.
// This is the default provider — no external dependencies required.
type EnvProvider struct{}

func (e *EnvProvider) Get(key string) (string, bool) {
	v := os.Getenv(key)
	return v, v != ""
}

func (e *EnvProvider) Name() string { return "env" }

// defaultProviderPtr stores the current provider as an atomic.Pointer-safe value.
// Use loadDefaultProvider/storeDefaultProvider for concurrent-safe access.
var defaultProvider atomic.Value // stores Provider

func init() {
	defaultProvider.Store(EnvProviderInstance)
}

// EnvProviderInstance is the shared singleton EnvProvider.
var EnvProviderInstance Provider = &EnvProvider{}

// loadDefaultProvider returns the current secret provider in a race-safe manner.
func loadDefaultProvider() Provider {
	return defaultProvider.Load().(Provider)
}

// storeDefaultProvider sets the global secret provider in a race-safe manner.
func storeDefaultProvider(p Provider) {
	defaultProvider.Store(p)
}

// Get retrieves a secret value using the current default provider.
// This is a convenience function that reads from the active provider chain.
func Get(key string) (string, bool) {
	return loadDefaultProvider().Get(key)
}

// InitProvider attempts to initialise a VaultProvider with the given config.
// On success, DefaultProvider is set to the VaultProvider.
// On failure, logs a warning and keeps the EnvProvider as default (graceful degradation).
// This function must be called during single-threaded startup (before concurrent access).
func InitProvider(cfg VaultConfig) {
	vp, err := NewVaultProvider(cfg)
	if err != nil {
		logger.Warnf("secrets: vault unavailable (%v), falling back to env provider", err)
		return
	}
	storeDefaultProvider(vp)
}
