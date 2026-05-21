package proxy

import (
	"strings"
	"sync"
	"time"

	"github.com/sony/gobreaker"

	"shopmicro/pkg/logger"
)

// CircuitBreaker 为每个 service/method 维护独立的熔断器实例。
// 使用 sony/gobreaker 实现（Closed → Open → Half-Open 状态机）。
type CircuitBreaker struct {
	mu  sync.RWMutex
	cbs map[string]*gobreaker.CircuitBreaker
	cfg gobreaker.Settings
}

// NewCircuitBreaker 创建一个 CircuitBreaker。
//
// consecutiveFailures: 连续失败次数触发熔断（default 5），仅作为辅助条件
// interval: Closed 状态计数重置间隔（default 30s）
// timeout: Open→HalfOpen 超时（default 5s）
// maxRequests: HalfOpen 状态最大探测请求数（default 3）
//
// 触发条件（同时满足）：
//   - 窗口内请求数 >= 20（避免低流量误触发）
//   - 失败率 >= 50%
//   - 连续失败次数 >= consecutiveFailures
func NewCircuitBreaker(consecutiveFailures uint32, interval, timeout time.Duration, maxRequests uint32) *CircuitBreaker {
	if consecutiveFailures == 0 {
		consecutiveFailures = 5
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if maxRequests == 0 {
		maxRequests = 10
	}
	cf := consecutiveFailures
	cb := &CircuitBreaker{
		cbs: make(map[string]*gobreaker.CircuitBreaker),
		cfg: gobreaker.Settings{
			MaxRequests:   maxRequests,
			Interval:      interval,
			Timeout:       timeout,
			ReadyToTrip:   func(counts gobreaker.Counts) bool { return readyToTrip(counts, cf) },
			OnStateChange: onStateChange,
			IsSuccessful:  isSuccessful,
		},
	}
	return cb
}

// readyToTrip 判断是否应该打开熔断器。
// 同时使用失败率和连续失败次数，避免高并发下少量瞬态错误误触发熔断。
//
// 触发条件（三个同时满足）：
//  1. 窗口内总请求数 >= 20（排除低流量场景的噪音）
//  2. 失败率 >= 50%
//  3. 连续失败次数 >= consecutiveFailures
func readyToTrip(counts gobreaker.Counts, consecutiveFailures uint32) bool {
	// 低流量时不触发，避免少量错误就打开熔断
	if counts.Requests < 20 {
		return false
	}
	failureRate := float64(counts.TotalFailures) / float64(counts.Requests)
	return failureRate >= 0.5 && counts.ConsecutiveFailures >= consecutiveFailures
}

// isSuccessful 判定 RPC 调用是否成功（用于熔断器计数）。
//
// 只有基础设施错误才计入熔断失败：
//   - 超时（timeout, deadline exceeded）
//   - 连接错误（connection refused, connection reset, connection shutdown）
//   - 熔断器错误（ErrOpenState, ErrTooManyRequests）
//
// 业务错误（参数错误、记录不存在、权限不足等）不影响熔断计数，
// 因为它们不代表下游服务不可用。
func isSuccessful(err error) bool {
	if err == nil {
		return true
	}
	msg := strings.ToLower(err.Error())
	// 基础设施错误 → 熔断失败
	return !(strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection shutdown") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "open state") ||
		strings.Contains(msg, "too many requests"))
}

// onStateChange 记录熔断器状态变更日志。
func onStateChange(name string, from, to gobreaker.State) {
	logger.Infof("circuit breaker [%s] state: %s → %s", name, from.String(), to.String())
}

// getOrCreate 获取或创建指定 key 的熔断器实例。
func (cb *CircuitBreaker) getOrCreate(key string) *gobreaker.CircuitBreaker {
	cb.mu.RLock()
	if c, ok := cb.cbs[key]; ok {
		cb.mu.RUnlock()
		return c
	}
	cb.mu.RUnlock()

	cb.mu.Lock()
	defer cb.mu.Unlock()
	// double-check
	if c, ok := cb.cbs[key]; ok {
		return c
	}
	cfg := cb.cfg
	cfg.Name = key
	c := gobreaker.NewCircuitBreaker(cfg)
	cb.cbs[key] = c
	return c
}

// Execute 在熔断器保护下执行 fn。
// key 格式为 "service/method"。
// 当熔断器为 Open 状态时，直接返回 gobreaker.ErrOpenState，不执行 fn。
func (cb *CircuitBreaker) Execute(key string, fn func() (interface{}, error)) (interface{}, error) {
	if cb == nil {
		return fn()
	}
	c := cb.getOrCreate(key)
	return c.Execute(fn)
}

// State 返回指定 key 的熔断器当前状态。
// 用于健康检查端点暴露熔断状态。
func (cb *CircuitBreaker) State(key string) gobreaker.State {
	if cb == nil {
		return gobreaker.StateClosed
	}
	cb.mu.RLock()
	c, ok := cb.cbs[key]
	cb.mu.RUnlock()
	if !ok {
		return gobreaker.StateClosed
	}
	return c.State()
}

// States 返回所有熔断器的状态快照。
func (cb *CircuitBreaker) States() map[string]gobreaker.State {
	if cb == nil {
		return nil
	}
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	result := make(map[string]gobreaker.State, len(cb.cbs))
	for k, c := range cb.cbs {
		result[k] = c.State()
	}
	return result
}
