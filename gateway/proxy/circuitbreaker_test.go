package proxy

import (
	"errors"
	"testing"
	"time"

	"github.com/sony/gobreaker"
)

// ==================== isSuccessful ====================

func TestIsSuccessful_Nil(t *testing.T) {
	if !isSuccessful(nil) {
		t.Error("nil error should be successful")
	}
}

func TestIsSuccessful_InfraErrors(t *testing.T) {
	infraErrors := []string{
		"rpc timeout",
		"connection timeout",
		"deadline exceeded",
		"connection refused",
		"connection reset by peer",
		"connection shutdown",
		"i/o timeout",
		"circuit breaker open state",
		"too many requests",
	}
	for _, msg := range infraErrors {
		t.Run(msg, func(t *testing.T) {
			if isSuccessful(errors.New(msg)) {
				t.Errorf("infra error %q should NOT be successful", msg)
			}
		})
	}
}

func TestIsSuccessful_BusinessErrors(t *testing.T) {
	bizErrors := []string{
		"user not found",
		"invalid parameter",
		"permission denied",
		"record already exists",
		"insufficient balance",
		"test error",
	}
	for _, msg := range bizErrors {
		t.Run(msg, func(t *testing.T) {
			if !isSuccessful(errors.New(msg)) {
				t.Errorf("business error %q should be successful (not counted as CB failure)", msg)
			}
		})
	}
}

// ==================== readyToTrip ====================

func TestReadyToTrip_LowVolume(t *testing.T) {
	// 少于 20 次请求时不应触发
	counts := gobreaker.Counts{
		Requests:             10,
		TotalFailures:        10,
		TotalSuccesses:       0,
		ConsecutiveFailures:  10,
		ConsecutiveSuccesses: 0,
	}
	if readyToTrip(counts, 3) {
		t.Error("should not trip with < 20 requests")
	}
}

func TestReadyToTrip_HighVolumeLowFailureRate(t *testing.T) {
	// 20 次请求，9 次失败（45%），不应触发
	counts := gobreaker.Counts{
		Requests:             20,
		TotalFailures:        9,
		TotalSuccesses:       11,
		ConsecutiveFailures:  9,
		ConsecutiveSuccesses: 0,
	}
	if readyToTrip(counts, 3) {
		t.Error("should not trip with 45% failure rate")
	}
}

func TestReadyToTrip_HighVolumeHighFailureRate(t *testing.T) {
	// 20 次请求，10 次失败（50%），连续失败 5 次，应触发
	counts := gobreaker.Counts{
		Requests:             20,
		TotalFailures:        10,
		TotalSuccesses:       10,
		ConsecutiveFailures:  5,
		ConsecutiveSuccesses: 0,
	}
	if !readyToTrip(counts, 3) {
		t.Error("should trip with 50% failure rate and consecutive failures >= 3")
	}
}

func TestReadyToTrip_HighFailureRateLowConsecutive(t *testing.T) {
	// 20 次请求，15 次失败（75%），但连续失败只有 2 次，不应触发
	counts := gobreaker.Counts{
		Requests:             20,
		TotalFailures:        15,
		TotalSuccesses:       5,
		ConsecutiveFailures:  2,
		ConsecutiveSuccesses: 0,
	}
	if readyToTrip(counts, 3) {
		t.Error("should not trip with consecutive failures < threshold")
	}
}

func TestReadyToTrip_AllFail(t *testing.T) {
	// 20 次全部失败，连续失败 20 次，应触发
	counts := gobreaker.Counts{
		Requests:             20,
		TotalFailures:        20,
		TotalSuccesses:       0,
		ConsecutiveFailures:  20,
		ConsecutiveSuccesses: 0,
	}
	if !readyToTrip(counts, 5) {
		t.Error("should trip with 100% failure rate")
	}
}

// ==================== 端到端熔断器行为 ====================

func TestCircuitBreaker_BizErrorsDoNotTrip(t *testing.T) {
	cb := NewCircuitBreaker(2, 30*time.Second, 5*time.Second, 3)

	// 30 次业务错误不应触发熔断（isSuccessful 对业务错误返回 true，不计入熔断失败）
	for i := 0; i < 30; i++ {
		// gobreaker 返回原始 error 但将其计为成功
		cb.Execute("svc/BizErr", func() (interface{}, error) {
			return nil, errors.New("record not found")
		})
	}

	if cb.State("svc/BizErr") != gobreaker.StateClosed {
		t.Errorf("should remain Closed with business errors, got %d", cb.State("svc/BizErr"))
	}
}

func TestCircuitBreaker_InfraErrorsTrip(t *testing.T) {
	cb := NewCircuitBreaker(2, 30*time.Second, 5*time.Second, 3)

	// 20 次基础设施错误应触发熔断
	for i := 0; i < 20; i++ {
		cb.Execute("svc/InfraErr", func() (interface{}, error) {
			return nil, errors.New("connection timeout")
		})
	}

	if cb.State("svc/InfraErr") == gobreaker.StateClosed {
		t.Error("should have tripped with infrastructure errors")
	}
}

func TestCircuitBreaker_DefaultMaxRequests(t *testing.T) {
	cb := NewCircuitBreaker(0, 0, 0, 0)
	// 默认 maxRequests 应为 10
	cb.Execute("svc/Default", func() (interface{}, error) { return nil, nil })
	if cb.cfg.MaxRequests != 10 {
		t.Errorf("default MaxRequests = %d, want 10", cb.cfg.MaxRequests)
	}
}
