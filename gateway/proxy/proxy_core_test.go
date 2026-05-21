package proxy

import (
	"testing"
	"time"
)

// infraError 模拟基础设施错误（超时），用于熔断器失败测试
type infraError struct{}

func (e *infraError) Error() string { return "connection timeout" }

// Verify it satisfies error interface
var _ error = (*infraError)(nil)

// ==================== preGzipCacheKey ====================

func TestPreGzipCacheKey_NoQuery(t *testing.T) {
	// 初始化最小配置以设置 maxKeyLen
	preGzipCfg.maxKeyLen = 256
	key := preGzipCacheKey("district", "GetProvinces", nil)
	want := "pregzip:district/GetProvinces"
	if key != want {
		t.Errorf("preGzipCacheKey(no query) = %q, want %q", key, want)
	}
}

func TestPreGzipCacheKey_WithQuery(t *testing.T) {
	preGzipCfg.maxKeyLen = 256
	key := preGzipCacheKey("district", "GetChildren", []byte("adcode=330100"))
	want := "pregzip:district/GetChildren?adcode=330100"
	if key != want {
		t.Errorf("preGzipCacheKey(with query) = %q, want %q", key, want)
	}
}

func TestPreGzipCacheKey_QueryParamOrder(t *testing.T) {
	preGzipCfg.maxKeyLen = 256
	// 参数顺序不同但排序后应一致
	key1 := preGzipCacheKey("svc", "Method", []byte("b=2&a=1"))
	key2 := preGzipCacheKey("svc", "Method", []byte("a=1&b=2"))
	if key1 != key2 {
		t.Errorf("cache keys should be equal after sorting: %q != %q", key1, key2)
	}
}

func TestPreGzipCacheKey_MultiValueParam(t *testing.T) {
	preGzipCfg.maxKeyLen = 256
	key := preGzipCacheKey("svc", "Method", []byte("tag=a&tag=b&tag=c"))
	// 多值参数排序后用逗号连接
	if key != "pregzip:svc/Method?tag=a,b,c" {
		t.Errorf("multi-value param = %q", key)
	}
}

func TestPreGzipCacheKey_TooLong(t *testing.T) {
	preGzipCfg.maxKeyLen = 32
	longQuery := make([]byte, 64)
	for i := range longQuery {
		longQuery[i] = 'a'
	}
	key := preGzipCacheKey("svc", "Method", longQuery)
	if key != "" {
		t.Errorf("too-long key should return empty, got %q", key)
	}
}

// ==================== setPreGzip / getPreGzip / cleanPreGzipCache ====================

func TestSetGetPreGzip(t *testing.T) {
	preGzipCfg.ttl = 10 * time.Second
	preGzipCfg.maxEntries = 100
	preGzipCfg.maxKeyLen = 256

	// 确保计数从0开始
	preGzipCache.Clear()
	preGzipCount.Store(0)

	setPreGzip("test:svc/Method", []byte("compressed-data"))

	data, ok := getPreGzip("test:svc/Method")
	if !ok || string(data) != "compressed-data" {
		t.Fatalf("getPreGzip: ok=%v, data=%q", ok, string(data))
	}
}

func TestGetPreGzip_Miss(t *testing.T) {
	_, ok := getPreGzip("nonexistent-key-xyz")
	if ok {
		t.Fatal("getPreGzip should return false for nonexistent key")
	}
}

func TestGetPreGzip_Expired(t *testing.T) {
	preGzipCfg.ttl = 1 * time.Millisecond
	preGzipCfg.maxEntries = 100
	preGzipCfg.maxKeyLen = 256

	preGzipCache.Clear()
	preGzipCount.Store(0)

	setPreGzip("test:svc/Expired", []byte("data"))

	time.Sleep(5 * time.Millisecond)

	_, ok := getPreGzip("test:svc/Expired")
	if ok {
		t.Fatal("getPreGzip should return false for expired entry")
	}
}

func TestSetPreGzip_Dedup(t *testing.T) {
	preGzipCfg.ttl = 10 * time.Second
	preGzipCfg.maxEntries = 100
	preGzipCfg.maxKeyLen = 256

	preGzipCache.Clear()
	preGzipCount.Store(0)

	// 写入同一个 key 两次，计数应只加1
	setPreGzip("test:svc/Dedup", []byte("v1"))
	setPreGzip("test:svc/Dedup", []byte("v2"))

	if preGzipCount.Load() != 1 {
		t.Errorf("count = %d, want 1 (dedup)", preGzipCount.Load())
	}
	// 第一次写入的值应保留
	data, ok := getPreGzip("test:svc/Dedup")
	if !ok || string(data) != "v1" {
		t.Errorf("expected v1, got %q", string(data))
	}
}

func TestCleanPreGzipCache(t *testing.T) {
	preGzipCfg.ttl = 10 * time.Second
	preGzipCfg.maxEntries = 100
	preGzipCfg.maxKeyLen = 256

	preGzipCache.Clear()
	preGzipCount.Store(0)

	// 写入一个即将过期的条目
	preGzipCache.Store("test:svc/Old", &preGzipEntry{
		data:   []byte("old"),
		expiry: time.Now().Add(-1 * time.Second), // 已过期
	})
	preGzipCount.Store(1)

	cleanPreGzipCache()

	if preGzipCount.Load() != 0 {
		t.Errorf("count after cleanup = %d, want 0", preGzipCount.Load())
	}
}

// ==================== CircuitBreaker ====================

func TestCircuitBreaker_Execute_NilCB(t *testing.T) {
	var cb *CircuitBreaker // nil
	result, err := cb.Execute("test/key", func() (interface{}, error) {
		return "ok", nil
	})
	if err != nil || result != "ok" {
		t.Fatalf("nil circuit breaker should passthrough: result=%v, err=%v", result, err)
	}
}

func TestCircuitBreaker_State_NilCB(t *testing.T) {
	var cb *CircuitBreaker
	if cb.State("test/key") != 0 { // gobreaker.StateClosed = 0
		t.Fatal("nil CB should return Closed state")
	}
}

func TestCircuitBreaker_States_NilCB(t *testing.T) {
	var cb *CircuitBreaker
	if cb.States() != nil {
		t.Fatal("nil CB should return nil states")
	}
}

func TestCircuitBreaker_Execute_Success(t *testing.T) {
	cb := NewCircuitBreaker(3, 30*time.Second, 5*time.Second, 3)
	result, err := cb.Execute("svc/Method", func() (interface{}, error) {
		return 42, nil
	})
	if err != nil || result != 42 {
		t.Fatalf("success: result=%v, err=%v", result, err)
	}
	if cb.State("svc/Method") != 0 {
		t.Error("state should be Closed after success")
	}
}

func TestCircuitBreaker_Execute_Failure(t *testing.T) {
	cb := NewCircuitBreaker(2, 30*time.Second, 5*time.Second, 3)

	// 连续失败 20 次触发熔断（consecutiveFailures=2, 但需要 requests>=20 且 failureRate>=50%）
	for i := 0; i < 20; i++ {
		cb.Execute("svc/Fail", func() (interface{}, error) {
			return nil, &infraError{} // 模拟基础设施错误（timeout）
		})
	}

	// 熔断应已打开（Open=1 或 HalfOpen=2）
	state := cb.State("svc/Fail")
	if state == 0 { // 不应仍是 Closed
		t.Errorf("expected Open or HalfOpen, got Closed (0)")
	}
}

func TestCircuitBreaker_States_AfterMultipleKeys(t *testing.T) {
	cb := NewCircuitBreaker(5, 30*time.Second, 5*time.Second, 3)

	cb.Execute("svc/A", func() (interface{}, error) { return nil, nil })
	cb.Execute("svc/B", func() (interface{}, error) { return nil, nil })

	states := cb.States()
	if len(states) != 2 {
		t.Fatalf("expected 2 states, got %d", len(states))
	}
	if states["svc/A"] != 0 || states["svc/B"] != 0 {
		t.Error("both should be Closed")
	}
}

func TestCircuitBreaker_Execute_NonexistentKey(t *testing.T) {
	cb := NewCircuitBreaker(5, 30*time.Second, 5*time.Second, 3)
	// 查询未创建的 key
	if cb.State("nonexistent") != 0 {
		t.Error("nonexistent key should return Closed")
	}
}

// ==================== Proxy New / Options ====================

func TestNew_WithCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker(5, 30*time.Second, 5*time.Second, 3)
	p := New(WithCircuitBreaker(cb))
	if p.cb != cb {
		t.Error("cb should be set")
	}
}

func TestNew_WithRPCTimeout(t *testing.T) {
	p := New(WithRPCTimeout(10 * time.Second))
	if p.rpcTimeout != 10*time.Second {
		t.Errorf("rpcTimeout = %v, want 10s", p.rpcTimeout)
	}
}

func TestNew_WithRPCTimeout_Zero(t *testing.T) {
	p := New(WithRPCTimeout(0))
	if p.rpcTimeout != 5*time.Second {
		t.Errorf("zero timeout should default to 5s, got %v", p.rpcTimeout)
	}
}

func TestNew_WithConnectTimeout(t *testing.T) {
	p := New(WithConnectTimeout(2 * time.Second))
	if p.connectTimeout != 2*time.Second {
		t.Errorf("connectTimeout = %v, want 2s", p.connectTimeout)
	}
}

func TestNew_WithMaxRetries(t *testing.T) {
	p := New(WithMaxRetries(5))
	if p.maxRetries != 5 {
		t.Errorf("maxRetries = %d, want 5", p.maxRetries)
	}
}

func TestNew_WithMaxRetries_Negative(t *testing.T) {
	p := New(WithMaxRetries(-1))
	if p.maxRetries != 0 {
		t.Errorf("negative retries should be clamped to 0, got %d", p.maxRetries)
	}
}

// ==================== New instanceCount ====================

func TestNew_InstanceCountInitialized(t *testing.T) {
	p := New()
	if p.instanceCount == nil {
		t.Fatal("instanceCount should be initialized")
	}
	if len(p.instanceCount) != 0 {
		t.Fatal("instanceCount should be empty")
	}
}
