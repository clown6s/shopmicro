package middleware

import (
	"net"
	"testing"
	"time"
)

func TestMaskIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want string
	}{
		{"ipv4 normal", "192.168.1.100", "192.168.x.x"},
		{"ipv4 another", "10.0.0.1", "10.0.x.x"},
		{"ipv4 loopback", "127.0.0.1", "127.0.x.x"},
		{"invalid", "not-an-ip", "unknown"},
		{"empty", "", "unknown"},
		{"ipv6", "2001:0db8:85a3:0000:0000:8a2e:0370:7334", "20:1:d:b8:x:x:x:x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskIP(tt.ip)
			if got != tt.want {
				t.Errorf("maskIP(%q) = %q, want %q", tt.ip, got, tt.want)
			}
		})
	}
}

func TestUniqueMember(t *testing.T) {
	now := time.Now()
	m1 := uniqueMember(now)
	m2 := uniqueMember(now)

	if m1 == m2 {
		t.Fatal("uniqueMember should return different values for the same timestamp")
	}
	if len(m1) == 0 {
		t.Fatal("uniqueMember should not return empty string")
	}
}

func TestGenerateRequestID(t *testing.T) {
	id1 := generateRequestID()
	id2 := generateRequestID()

	if id1 == id2 {
		t.Fatal("generateRequestID should return unique values")
	}
	if len(id1) != 24 {
		t.Errorf("generateRequestID() length = %d, want 24", len(id1))
	}
}

func TestParseTrustedNets(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		wantN int
	}{
		{"cidr", []string{"10.0.0.0/8", "172.16.0.0/12"}, 2},
		{"plain ip", []string{"192.168.1.1"}, 1},
		{"mixed", []string{"10.0.0.0/8", "192.168.1.1", ""}, 2},
		{"empty input", []string{}, 0},
		{"invalid", []string{"not-an-ip"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nets := parseTrustedNets(tt.input)
			if len(nets) != tt.wantN {
				t.Errorf("parseTrustedNets(%v) got %d nets, want %d", tt.input, len(nets), tt.wantN)
			}
		})
	}
}

func TestIsTrusted_NoNets(t *testing.T) {
	saved := trustedNets
	trustedNets = nil
	defer func() { trustedNets = saved }()

	if isTrusted(net.ParseIP("10.0.0.1")) {
		t.Fatal("isTrusted should return false when no trusted nets configured")
	}
}

func TestIsTrusted_WithNets(t *testing.T) {
	saved := trustedNets
	trustedNets = []net.IPNet{
		{IP: net.ParseIP("10.0.0.0"), Mask: net.IPv4Mask(255, 0, 0, 0)},
	}
	defer func() { trustedNets = saved }()

	if !isTrusted(net.ParseIP("10.1.2.3")) {
		t.Fatal("isTrusted should return true for 10.1.2.3 in 10.0.0.0/8")
	}
	if isTrusted(net.ParseIP("192.168.1.1")) {
		t.Fatal("isTrusted should return false for 192.168.1.1 not in 10.0.0.0/8")
	}
}

func TestSetEndpointLimit(t *testing.T) {
	rl := &RedisRateLimiter{
		endpointOverrides: make(map[string]*endpointLimit),
		limit:             100,
		window:            60 * time.Second,
	}

	rl.SetEndpointLimit("user/Login", 5, 60)
	el := rl.GetEndpointLimit("user/Login")
	if el == nil {
		t.Fatal("GetEndpointLimit should return non-nil")
	}
	if el.limit != 5 {
		t.Errorf("limit = %d, want 5", el.limit)
	}
	if el.window != 60*time.Second {
		t.Errorf("window = %v, want 60s", el.window)
	}

	if rl.GetEndpointLimit("unknown/method") != nil {
		t.Fatal("GetEndpointLimit should return nil for unconfigured endpoint")
	}
}

func TestSetEndpointLimit_DefaultWindow(t *testing.T) {
	rl := &RedisRateLimiter{
		endpointOverrides: make(map[string]*endpointLimit),
		limit:             100,
		window:            30 * time.Second,
	}

	rl.SetEndpointLimit("user/Register", 10, 0)
	el := rl.GetEndpointLimit("user/Register")
	if el.window != 30*time.Second {
		t.Errorf("window = %v, want default 30s", el.window)
	}
}

func TestNewEndpointCheckFn_NoConfig(t *testing.T) {
	rl := &RedisRateLimiter{
		endpointOverrides: make(map[string]*endpointLimit),
	}
	fn := rl.NewEndpointCheckFn()

	if !fn(nil, "user/Login", "1.2.3.4") {
		t.Fatal("NewEndpointCheckFn should return true for unconfigured endpoint")
	}
}

func TestGetRealIP_NoXFF(t *testing.T) {
	// trustedNets 为空时直接返回 ClientIP
	saved := trustedNets
	trustedNets = nil
	defer func() { trustedNets = saved }()

	// getRealIP 需要 *app.RequestContext，无法直接构造，
	// 但可以通过 InitTrustedProxies + isTrusted 测试核心逻辑
	InitTrustedProxies([]string{})
	if len(trustedNets) != 0 {
		t.Fatal("InitTrustedProxies with empty should set nil nets")
	}
}
