package proxy

import (
	"path/filepath"
	"testing"
)

func TestBearerToken_Valid(t *testing.T) {
	// R5-P1-05: JWT 必须包含两个点（三段 base64url）
	token, ok := bearerToken("Bearer eyJhbG.eyJzdG.SflKx")
	if !ok || token != "eyJhbG.eyJzdG.SflKx" {
		t.Fatalf("expected (eyJhbG.eyJzdG.SflKx, true), got (%q, %v)", token, ok)
	}
}

func TestBearerToken_ValidLowerCase(t *testing.T) {
	token, ok := bearerToken("bearer eyJhbG.eyJzdG.SflKx")
	if !ok || token != "eyJhbG.eyJzdG.SflKx" {
		t.Fatalf("expected (eyJhbG.eyJzdG.SflKx, true), got (%q, %v)", token, ok)
	}
}

func TestBearerToken_Empty(t *testing.T) {
	_, ok := bearerToken("")
	if ok {
		t.Fatal("empty header should fail")
	}
}

func TestBearerToken_NoBearer(t *testing.T) {
	_, ok := bearerToken("Token eyJhbG.eyJzdG.SflKx")
	if ok {
		t.Fatal("non-Bearer scheme should fail")
	}
}

func TestBearerToken_EmptyToken(t *testing.T) {
	_, ok := bearerToken("Bearer ")
	if ok {
		t.Fatal("empty token should fail")
	}
}

func TestBearerToken_MultipleParts(t *testing.T) {
	_, ok := bearerToken("Bearer abc 123")
	if ok {
		t.Fatal("multiple parts should fail")
	}
}

func TestBearerToken_InvalidFormat(t *testing.T) {
	// R5-P1-05: 不含两个点的字符串应被拒绝
	_, ok := bearerToken("Bearer nodots")
	if ok {
		t.Fatal("token without dots should fail")
	}
	_, ok = bearerToken("Bearer one.dot")
	if ok {
		t.Fatal("token with one dot should fail")
	}
	_, ok = bearerToken("Bearer a.b.c.d")
	if ok {
		t.Fatal("token with three dots should fail")
	}
}

func TestBearerToken_Whitespace(t *testing.T) {
	token, ok := bearerToken("  Bearer eyJhbG.eyJzdG.SflKx  ")
	if !ok || token != "eyJhbG.eyJzdG.SflKx" {
		t.Fatalf("expected (eyJhbG.eyJzdG.SflKx, true), got (%q, %v)", token, ok)
	}
}

func TestToMethodName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"login", "Login"},
		{"Login", "Login"},
		{"registerUser", "RegisterUser"},
		{"", ""},
	}
	for _, tt := range tests {
		got := toMethodName(tt.input)
		if got != tt.want {
			t.Fatalf("toMethodName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractServiceName(t *testing.T) {
	tests := []struct {
		key    string
		prefix string
		want   string
	}{
		{"kitex/registry-etcd/user/127.0.0.1:8888", "kitex/registry-etcd/", "user"},
		{"kitex/registry-etcd/product/10.0.0.1:9000", "kitex/registry-etcd/", "product"},
		{"kitex/registry-etcd/", "kitex/registry-etcd/", ""},
	}
	for _, tt := range tests {
		got := extractServiceName(tt.key, tt.prefix)
		if got != tt.want {
			t.Fatalf("extractServiceName(%q, %q) = %q, want %q", tt.key, tt.prefix, got, tt.want)
		}
	}
}

func TestBuildProtoPath(t *testing.T) {
	got := buildProtoPath("../", "user")
	// filepath.Join uses OS-specific separator
	want := filepath.Join("..", "user", "api", "user.proto")
	if got != want {
		t.Fatalf("buildProtoPath = %q, want %q", got, want)
	}
}
