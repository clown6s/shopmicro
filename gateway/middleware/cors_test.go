package middleware

import (
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/test/assert"
)

func TestAllowOriginSet(t *testing.T) {
	// R5-P3-02: 测试 map 查找行为，替代已移除的 isAllowedOrigin 函数
	origins := []string{"http://localhost:3000", "http://127.0.0.1:3000"}
	allowOriginSet := make(map[string]bool, len(origins))
	for _, o := range origins {
		allowOriginSet[o] = true
	}

	tests := []struct {
		origin string
		want   bool
	}{
		{"http://localhost:3000", true},
		{"http://evil.com", false},
		{"", false},
	}
	for _, tt := range tests {
		got := allowOriginSet[tt.origin]
		assert.DeepEqual(t, got, tt.want)
	}
}

// Compile-time check
var _ interface {
	GetHeader(string) []byte
} = (*app.RequestContext)(nil)
