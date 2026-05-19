package routes

import (
	"testing"
)

// ==================== isPrivateIP ====================

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		// RFC 1918 私有地址
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.0.1", true},
		{"192.168.255.255", true},
		// 环回地址
		{"127.0.0.1", true},
		{"127.255.255.255", true},
		// 链路本地
		{"169.254.1.1", true},
		{"169.254.255.255", true},
		// CGNAT RFC 6598
		{"100.64.0.1", true},
		{"100.127.255.255", true},
		// 公网地址
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"203.0.113.1", false},
		// 边界值 — 不在私有范围内
		{"172.15.255.255", false},
		{"172.32.0.0", false},
		{"192.169.0.0", false},
		// IPv6
		{"::1", true},
		{"fc00::1", true},
		{"fe80::1", true},
		{"2001:4860:4860::8888", false}, // Google DNS
		// 无效 IP
		{"", false},
		{"not-an-ip", false},
		{"256.1.1.1", false},
	}
	for _, tt := range tests {
		got := isPrivateIP(tt.ip)
		if got != tt.want {
			t.Errorf("isPrivateIP(%q) = %v, want %v", tt.ip, got, tt.want)
		}
	}
}
