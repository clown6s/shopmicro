package redis

import (
	"testing"
)

func TestConfigSetDefaults(t *testing.T) {
	cfg := Config{}
	cfg.setDefaults()

	if cfg.PoolSize != 100 {
		t.Fatalf("expected PoolSize 100, got %d", cfg.PoolSize)
	}
	if cfg.MinIdleConns != 10 {
		t.Fatalf("expected MinIdleConns 10, got %d", cfg.MinIdleConns)
	}
	if cfg.PoolTimeoutSec != 4 {
		t.Fatalf("expected PoolTimeoutSec 4, got %d", cfg.PoolTimeoutSec)
	}
	if cfg.ConnMaxLifeSec != 1800 {
		t.Fatalf("expected ConnMaxLifeSec 1800, got %d", cfg.ConnMaxLifeSec)
	}
	if cfg.ConnMaxIdleSec != 600 {
		t.Fatalf("expected ConnMaxIdleTimeSec 600, got %d", cfg.ConnMaxIdleSec)
	}
}

func TestConfigSetDefaults_NoOverride(t *testing.T) {
	cfg := Config{PoolSize: 50, MinIdleConns: 5}
	cfg.setDefaults()

	if cfg.PoolSize != 50 {
		t.Fatalf("existing PoolSize should not be overridden")
	}
	if cfg.MinIdleConns != 5 {
		t.Fatalf("existing MinIdleConns should not be overridden")
	}
}
