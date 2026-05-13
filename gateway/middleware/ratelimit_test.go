package middleware

import (
	"encoding/base64"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/test/assert"
)

func TestBase64URLDecode(t *testing.T) {
	encoded := base64.RawURLEncoding.EncodeToString([]byte("hello"))
	decoded, err := base64urlDecode(encoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(decoded) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(decoded))
	}
}

func TestExtractUserIDFromToken_MissingHeader(t *testing.T) {
	c := &app.RequestContext{}
	uid := extractUserIDFromToken(c)
	if uid != "" {
		t.Fatalf("expected empty uid, got %q", uid)
	}
}

func TestExtractUserIDFromToken_ValidPayload(t *testing.T) {
	payload := `{"user_id":42}`
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	fakeToken := "eyJhbGciOiJIUzI1NiJ9." + encoded + ".fakesig"

	c := &app.RequestContext{}
	c.Request.Header.Set("Authorization", "Bearer "+fakeToken)
	uid := extractUserIDFromToken(c)
	if uid != "42" {
		t.Fatalf("expected uid '42', got %q", uid)
	}
}

func TestExtractUserIDFromToken_NoBearerScheme(t *testing.T) {
	c := &app.RequestContext{}
	c.Request.Header.Set("Authorization", "Token sometoken")
	uid := extractUserIDFromToken(c)
	if uid != "" {
		t.Fatalf("expected empty uid, got %q", uid)
	}
}

func TestExtractUserIDFromToken_EmptyBearer(t *testing.T) {
	c := &app.RequestContext{}
	c.Request.Header.Set("Authorization", "Bearer ")
	uid := extractUserIDFromToken(c)
	if uid != "" {
		t.Fatalf("expected empty uid, got %q", uid)
	}
}

func TestExtractUserIDFromToken_MalformedJWT(t *testing.T) {
	c := &app.RequestContext{}
	c.Request.Header.Set("Authorization", "Bearer not-valid")
	uid := extractUserIDFromToken(c)
	if uid != "" {
		t.Fatalf("expected empty uid, got %q", uid)
	}
}

func TestExtractUserIDFromToken_WithPhone(t *testing.T) {
	// Verify that phone is NOT extracted (only user_id)
	payload := `{"user_id":12345,"phone":"13800138000"}`
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	token := "eyJhbGciOiJIUzI1NiJ9." + encoded + ".sig"

	c := &app.RequestContext{}
	c.Request.Header.Set("Authorization", "Bearer "+token)
	uid := extractUserIDFromToken(c)
	if uid != "12345" {
		t.Fatalf("expected uid '12345', got %q", uid)
	}
}

func TestIsAllowedOrigin(t *testing.T) {
	tests := []struct {
		origin  string
		allowed []string
		want    bool
	}{
		{"http://localhost:3000", []string{"http://localhost:3000", "http://127.0.0.1:3000"}, true},
		{"http://localhost:3000", []string{"http://127.0.0.1:3000"}, false},
		{"http://evil.com", []string{}, false},
		{"", []string{"http://localhost:3000"}, false},
	}
	for _, tt := range tests {
		got := isAllowedOrigin(tt.origin, tt.allowed)
		assert.DeepEqual(t, got, tt.want)
	}
}

// Compile-time check
var _ interface {
	GetHeader(string) []byte
} = (*app.RequestContext)(nil)
