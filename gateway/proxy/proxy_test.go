package proxy

import (
	"errors"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestShouldRetryGRPC_NilError(t *testing.T) {
	if shouldRetryGRPC(nil) {
		t.Fatal("nil error should not be retryable")
	}
}

func TestShouldRetryGRPC_NonGRPCError(t *testing.T) {
	// Non-gRPC errors (network errors) are retryable
	if !shouldRetryGRPC(errors.New("connection refused")) {
		t.Fatal("non-gRPC error should be retryable")
	}
}

func TestShouldRetryGRPC_RetryableCodes(t *testing.T) {
	retryable := []codes.Code{
		codes.DeadlineExceeded,
		codes.Unavailable,
		codes.ResourceExhausted,
	}
	for _, code := range retryable {
		err := status.Error(code, "test")
		if !shouldRetryGRPC(err) {
			t.Fatalf("code %v should be retryable", code)
		}
	}
}

func TestShouldRetryGRPC_NonRetryableCodes(t *testing.T) {
	nonRetryable := []codes.Code{
		codes.Internal,
		codes.InvalidArgument,
		codes.NotFound,
		codes.PermissionDenied,
		codes.Unauthenticated,
		codes.AlreadyExists,
		codes.FailedPrecondition,
		codes.Aborted,
		codes.OutOfRange,
		codes.Unimplemented,
		codes.DataLoss,
		codes.Canceled,
		codes.Unknown,
	}
	for _, code := range nonRetryable {
		err := status.Error(code, "test")
		if shouldRetryGRPC(err) {
			t.Fatalf("code %v should NOT be retryable", code)
		}
	}
}

func TestBearerToken_Valid(t *testing.T) {
	token, ok := bearerToken("Bearer abc123")
	if !ok || token != "abc123" {
		t.Fatalf("expected (abc123, true), got (%q, %v)", token, ok)
	}
}

func TestBearerToken_ValidLowerCase(t *testing.T) {
	token, ok := bearerToken("bearer abc123")
	if !ok || token != "abc123" {
		t.Fatalf("expected (abc123, true), got (%q, %v)", token, ok)
	}
}

func TestBearerToken_Empty(t *testing.T) {
	_, ok := bearerToken("")
	if ok {
		t.Fatal("empty header should fail")
	}
}

func TestBearerToken_NoBearer(t *testing.T) {
	_, ok := bearerToken("Token abc123")
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

func TestBearerToken_Whitespace(t *testing.T) {
	token, ok := bearerToken("  Bearer abc123  ")
	if !ok || token != "abc123" {
		t.Fatalf("expected (abc123, true), got (%q, %v)", token, ok)
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
