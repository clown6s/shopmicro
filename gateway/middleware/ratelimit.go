package middleware

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	goredis "github.com/redis/go-redis/v9"

	"shopmicro/pkg/logger"
	"shopmicro/pkg/redis"
	"shopmicro/pkg/response"
)

// slidingWindowLua 是一个原子性的 Redis Lua 脚本，实现滑动窗口限流。
//
// KEYS[1] = 限流键（例如 "ratelimit:ip:1.2.3.4"）
// ARGV[1] = 窗口起始时间戳（毫秒）(now - window)
// ARGV[2] = 当前时间戳（毫秒）
// ARGV[3] = 限流阈值（窗口内允许的最大请求数）
// ARGV[4] = 键的 TTL（毫秒）
// ARGV[5] = 唯一成员 ID
//
// 返回值: 1 = 允许，0 = 拒绝
var slidingWindowLua = goredis.NewScript(`
local key = KEYS[1]
local window_start = tonumber(ARGV[1])
local now = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])
local member = ARGV[5]

-- 移除滑动窗口之外的过期条目
redis.call('ZREMRANGEBYSCORE', key, '-inf', window_start)

-- 计算窗口内剩余条目数
local count = redis.call('ZCARD', key)

if count >= limit then
    return 0
end

-- 添加当前请求
redis.call('ZADD', key, now, member)

-- 设置键的 TTL 以自动清理
redis.call('PEXPIRE', key, ttl)

return 1
`)

// RedisRateLimiter 通过 Redis 实现分布式滑动窗口限流。
type RedisRateLimiter struct {
	mu                sync.RWMutex // R4-P1-01: 保护 endpointOverrides
	rdb               *redis.Client
	limit             int           // 默认窗口内最大请求数
	window            time.Duration // 默认滑动窗口时长
	endpointOverrides map[string]*endpointLimit
}

// endpointLimit 保存按端点的限流配置
type endpointLimit struct {
	limit  int
	window time.Duration
}

// NewRedisRateLimiter 创建一个基于 Redis 的滑动窗口限流器。
func NewRedisRateLimiter(rdb *redis.Client, limit int, window time.Duration) *RedisRateLimiter {
	return &RedisRateLimiter{
		rdb:               rdb,
		limit:             limit,
		window:            window,
		endpointOverrides: make(map[string]*endpointLimit),
	}
}

// SetEndpointLimit 设置按端点的限流覆盖配置。
// endpointKey 格式: "service/method"（例如 "user/Login"）
func (rl *RedisRateLimiter) SetEndpointLimit(endpointKey string, limit int, windowSec int) {
	w := rl.window
	if windowSec > 0 {
		w = time.Duration(windowSec) * time.Second
	}
	rl.mu.Lock()
	rl.endpointOverrides[endpointKey] = &endpointLimit{limit: limit, window: w}
	rl.mu.Unlock()
}

// GetEndpointLimit 返回按端点的限流配置（如果已配置）。
// 返回值拷贝而非指针，避免并发读写 map 中的共享结构体。
func (rl *RedisRateLimiter) GetEndpointLimit(endpointKey string) *endpointLimit {
	rl.mu.RLock()
	el, ok := rl.endpointOverrides[endpointKey]
	rl.mu.RUnlock()
	if !ok {
		return nil
	}
	cp := *el
	return &cp
}

// NewEndpointCheckFn 返回一个兼容 proxy.WithEndpointRateLimit 的函数。
// 它检查 endpointKey 是否有按端点覆盖配置，并执行严格的限流。
func (rl *RedisRateLimiter) NewEndpointCheckFn() func(ctx context.Context, endpointKey, ip string) bool {
	return func(ctx context.Context, endpointKey, ip string) bool {
		el := rl.GetEndpointLimit(endpointKey)
		if el == nil {
			return true // 未配置按端点限流
		}
		key := fmt.Sprintf("shopmicro:ratelimit:ep:%s:ip:%s", endpointKey, ip)
		now := time.Now()
		member := uniqueMember(now)
		return rl.allowWithConfig(ctx, key, now, el.window, el.limit, member)
	}
}

// Allow 检查来自指定 key 的请求是否被允许。
// P1-3: 当 Redis 不可用时，执行失败关闭（拒绝请求），
// 而不是回退到本地限流器。
// 这可以防止在分布式环境中绕过限流。
func (rl *RedisRateLimiter) Allow(ctx context.Context, key string) bool {
	now := time.Now()
	member := uniqueMember(now)
	return rl.allowWithConfig(ctx, key, now, rl.window, rl.limit, member)
}

// allowWithConfig 使用自定义窗口/限流值检查限流。
func (rl *RedisRateLimiter) allowWithConfig(ctx context.Context, key string, now time.Time, window time.Duration, limit int, member string) bool {
	windowStart := now.Add(-window)

	result, err := slidingWindowLua.Run(ctx, rl.rdb.RawClient(), []string{key},
		windowStart.UnixMilli(),
		now.UnixMilli(),
		limit,
		window.Milliseconds(),
		member,
	).Int()

	if err != nil {
		// P2-03 修复：Redis 不可用 → 执行失败关闭。
		// 拒绝请求而非允许绕过。
		logger.Warnf("ratelimit: redis unavailable, failing closed (rejecting request)")
		return false
	}

	return result == 1
}

// fallbackCounter 是 crypto/rand 失败时的后备计数器
var fallbackCounter atomic.Uint64

// uniqueMember 返回一个限流成员字符串，即使在同一纳秒的并发请求中也能保证唯一。
// 格式: {unixNano}:{randSuffix}
func uniqueMember(now time.Time) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// P1-06: crypto/rand 失败会影响限流器唯一性（安全相关），
		// 使用时间戳+计数器作为 fallback，记录 ERROR 以便运维介入。
		logger.Errorf("ratelimit: crypto/rand unavailable — rate limiter uniqueness degraded")
		nano := now.UnixNano()
		b[0] = byte(nano)
		b[1] = byte(nano >> 8)
		b[2] = byte(nano >> 16)
		b[3] = byte(nano >> 24)
		cnt := fallbackCounter.Add(1)
		b[4] = byte(cnt)
		b[5] = byte(cnt >> 8)
	}
	return fmt.Sprintf("%d:%08x", now.UnixNano(), binary.BigEndian.Uint64(b[:]))
}

// RateLimitMiddleware 返回一个 Hertz 中间件，通过 Redis 执行分布式
// 按 IP 滑动窗口限流。
// R2-P2-01: 已从中间件中移除按用户限流（之前基于未验证的
// JWT 载荷，可被绕过）。代理处理器中的按端点限流
// 为敏感操作提供了更严格的、与方法无关的保护。
func RateLimitMiddleware(rl *RedisRateLimiter) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		ip := ClientRealIP(c)
		ipKey := fmt.Sprintf("shopmicro:ratelimit:ip:%s", ip)

		if !rl.Allow(ctx, ipKey) {
			response.TooManyRequests(c)
			c.Abort()
			return
		}

		c.Next(ctx)
	}
}
