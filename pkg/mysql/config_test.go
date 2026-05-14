package mysql

import "testing"

func TestValidate_BasicDSN(t *testing.T) {
	cfg := Config{DSN: "user:pass@tcp(127.0.0.1:3306)/mydb"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	// Should have ? separator since original had none
	if cfg.DSN[0:35] != "user:pass@tcp(127.0.0.1:3306)/mydb?" {
		t.Fatalf("DSN missing ? separator: %s", cfg.DSN)
	}
	if !contains(cfg.DSN, "parseTime=true") {
		t.Fatalf("DSN missing parseTime: %s", cfg.DSN)
	}
}

func TestValidate_DSN_WithExistingParams(t *testing.T) {
	cfg := Config{DSN: "user:pass@tcp(127.0.0.1:3306)/mydb?parseTime=true&timeout=10s"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	// Should NOT duplicate parseTime
	if countSubstring(cfg.DSN, "parseTime=") != 1 {
		t.Fatalf("DSN has duplicate parseTime: %s", cfg.DSN)
	}
	// Should add charset since it's missing
	if !contains(cfg.DSN, "charset=utf8mb4") {
		t.Fatalf("DSN missing charset: %s", cfg.DSN)
	}
}

func TestValidate_EmptyDSN(t *testing.T) {
	cfg := Config{}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty DSN")
	}
}

func TestValidate_PoolDefaults(t *testing.T) {
	cfg := Config{DSN: "user:pass@tcp(127.0.0.1:3306)/mydb"}
	cfg.Validate()
	if cfg.MaxIdleConns != 10 {
		t.Fatalf("expected default MaxIdleConns=10, got %d", cfg.MaxIdleConns)
	}
	if cfg.MaxOpenConns != 100 {
		t.Fatalf("expected default MaxOpenConns=100, got %d", cfg.MaxOpenConns)
	}
	if cfg.ConnMaxLifetime != 3600 {
		t.Fatalf("expected default ConnMaxLifetime=3600, got %d", cfg.ConnMaxLifetime)
	}
	if cfg.ConnMaxIdleTime != 600 {
		t.Fatalf("expected default ConnMaxIdleTime=600, got %d", cfg.ConnMaxIdleTime)
	}
}

func TestValidate_PoolCustomValues(t *testing.T) {
	cfg := Config{
		DSN:             "user:pass@tcp(127.0.0.1:3306)/mydb",
		MaxIdleConns:    20,
		MaxOpenConns:    200,
		ConnMaxLifetime: 7200,
		ConnMaxIdleTime: 1800,
	}
	cfg.Validate()
	if cfg.MaxIdleConns != 20 {
		t.Fatalf("expected MaxIdleConns=20, got %d", cfg.MaxIdleConns)
	}
	if cfg.MaxOpenConns != 200 {
		t.Fatalf("expected MaxOpenConns=200, got %d", cfg.MaxOpenConns)
	}
}

// R4-P1-04: "readTimeout" contains "timeout" — boundary-aware check must not skip "timeout"
func TestValidate_DSN_TimeoutNotConfusedByReadTimeout(t *testing.T) {
	cfg := Config{DSN: "user:pass@tcp(127.0.0.1:3306)/mydb?parseTime=true&readTimeout=5s&writeTimeout=5s"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	// Must add "timeout=5s" (connect timeout) — old logic would skip it because
	// "readTimeout" contains the substring "timeout"
	if !contains(cfg.DSN, "&timeout=5s") && !contains(cfg.DSN, "?timeout=5s") {
		t.Fatalf("DSN missing connect timeout (confused by readTimeout): %s", cfg.DSN)
	}
	// Should NOT duplicate readTimeout
	if countSubstring(cfg.DSN, "readTimeout=") != 1 {
		t.Fatalf("DSN has duplicate readTimeout: %s", cfg.DSN)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func countSubstring(s, sub string) int {
	count := 0
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			count++
		}
	}
	return count
}
