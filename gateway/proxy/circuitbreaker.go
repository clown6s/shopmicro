package proxy

import (
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
// 熔断策略:
//   - 连续 5 次 RPC 失败 → Open（熔断）
//   - Open 状态持续 30 秒后 → Half-Open（放行 3 个探测请求）
//   - Half-Open 中探测成功 → Closed；探测失败 → Open
func NewCircuitBreaker() *CircuitBreaker {
	cb := &CircuitBreaker{
		cbs: make(map[string]*gobreaker.CircuitBreaker),
		cfg: gobreaker.Settings{
			MaxRequests:   3,                // 半开状态最大探测请求数
			Interval:      30 * time.Second, // 闭合→半开的间隔
			Timeout:       5 * time.Second,  // 半开→开的超时
			ReadyToTrip:   readyToTrip,      // 连续失败判定
			OnStateChange: onStateChange,    // 状态变更日志
			IsSuccessful:  isSuccessful,     // 成功判定：err 为 nil 视为成功
		},
	}
	return cb
}

// readyToTrip 连续 5 次失败触发熔断。
// gobreaker.Counts 包含 ConsecutiveFailures 字段。
func readyToTrip(counts gobreaker.Counts) bool {
	return counts.ConsecutiveFailures >= 5
}

// isSuccessful 判定 RPC 调用是否成功。
// err 为 nil 视为成功，err 非 nil 视为失败。
func isSuccessful(err error) bool {
	return err == nil
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
