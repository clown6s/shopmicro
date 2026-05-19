package middleware

import "testing"

// ==================== requestIDRegex ====================

func TestRequestIDRegex(t *testing.T) {
	tests := []struct {
		id    string
		valid bool
	}{
		{"abc123", true},
		{"ABC-123_def", true},
		{"req-001", true},
		{"a", true},                       // 最短
		{string(make([]byte, 64)), false}, // 64个0x00 — 不全是有效字符
		// 64个有效字符
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},
		// 65个字符 — 超长
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},
		{"", false},                 // 空
		{"hello world", false},      // 空格
		{"hello\nworld", false},     // 换行
		{"hello\tworld", false},     // 制表符
		{"id=1&x=2", false},         // 特殊字符
		{"user@example.com", false}, // @
		{"id:123", false},           // 冒号
	}
	for _, tt := range tests {
		got := requestIDRegex.MatchString(tt.id)
		if got != tt.valid {
			t.Errorf("requestIDRegex.MatchString(%q) = %v, want %v", tt.id, got, tt.valid)
		}
	}
}
