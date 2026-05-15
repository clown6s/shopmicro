package middleware

import (
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/test/assert"
)

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
