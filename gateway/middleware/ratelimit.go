package middleware

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"

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

// ipLimiterEntry 封装 rate.Limiter 及其最后访问时间，用于定期淘汰。
type ipLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// localLimiterPerIP 本地内存限流器（按 IP），用于 GET 公开接口的轻量限流。
// 使用 sync.Map + golang.org/x/time/rate 实现无锁读。
// 定期清理长时间未访问的条目，防止攻击者伪造大量 IP 导致 OOM。
var localLimiterPerIP sync.Map // map[string]*ipLimiterEntry

const localRateLimit = 100000 // 每秒每 IP 100000 次（GET 公开接口，内存限流上限）

const (
	localLimiterMaxEntries    = 100000 // 最大缓存 IP 数
	localLimiterTTL           = 10 * time.Minute
	localLimiterCleanInterval = 5 * time.Minute
)

// cleanLocalLimitersStop 用于优雅关闭时停止清理 goroutine。
var cleanLocalLimitersStop chan struct{}

func init() {
	cleanLocalLimitersStop = make(chan struct{})
	go cleanLocalLimiters(cleanLocalLimitersStop)
}

// StopCleanLocalLimiters 停止本地限流器清理 goroutine，防止 goroutine 泄漏。
// 应在优雅关闭时调用。
func StopCleanLocalLimiters() {
	close(cleanLocalLimitersStop)
}

// cleanLocalLimiters 定期清理过期的 IP 限流器条目。
func cleanLocalLimiters(stop <-chan struct{}) {
	ticker := time.NewTicker(localLimiterCleanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			now := time.Now()
			expiredAt := now.Add(-localLimiterTTL)

			// 第一遍：统计当前条目数
			var count int
			localLimiterPerIP.Range(func(_, _ interface{}) bool {
				count++
				return true
			})

			// 条目数在限制内且没有超限风险，跳过清理
			if count < localLimiterMaxEntries/2 {
				continue
			}

			// 第二遍：清理过期条目
			localLimiterPerIP.Range(func(key, value interface{}) bool {
				entry, ok := value.(*ipLimiterEntry)
				if !ok || entry.lastSeen.Before(expiredAt) {
					localLimiterPerIP.Delete(key)
				}
				return true
			})
		}
	}
}

func getLocalLimiter(ip string) *rate.Limiter {
	if v, ok := localLimiterPerIP.Load(ip); ok {
		if entry, ok := v.(*ipLimiterEntry); ok {
			entry.lastSeen = time.Now()
			return entry.limiter
		}
	}
	limiter := rate.NewLimiter(rate.Limit(localRateLimit), localRateLimit)
	entry := &ipLimiterEntry{limiter: limiter, lastSeen: time.Now()}
	actual, _ := localLimiterPerIP.LoadOrStore(ip, entry)
	if actualEntry, ok := actual.(*ipLimiterEntry); ok {
		actualEntry.lastSeen = time.Now()
		return actualEntry.limiter
	}
	return limiter
}

// uniqueMember 返回一个限流成员字符串，即使在同一纳秒的并发请求中也能保证唯一。
// 格式: {unixNano}:{randSuffix}
func uniqueMember(now time.Time) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// P1-06: crypto/rand 失败会影响限流器唯一性（安全相关），
		// 使用时间戳+计数器作为 fallback，记录 ERROR 以便运维介入。
		logger.Errorf("ratelimit: crypto/rand unavailable — rate limiter uniqueness degraded")
		nano := now.UnixNano()
		cnt := fallbackCounter.Add(1)
		// 写满 8 字节：高 4 字节为纳秒时间戳，低 4 字节为计数器
		b[0] = byte(nano >> 24)
		b[1] = byte(nano >> 16)
		b[2] = byte(nano >> 8)
		b[3] = byte(nano)
		b[4] = byte(cnt >> 24)
		b[5] = byte(cnt >> 16)
		b[6] = byte(cnt >> 8)
		b[7] = byte(cnt)
	}
	return fmt.Sprintf("%d:%08x", now.UnixNano(), binary.BigEndian.Uint64(b[:]))
}

// RateLimitMiddleware 返回一个 Hertz 中间件，通过 Redis 执行分布式
// 按 IP 滑动窗口限流。
// GET 请求走本地内存限流（避免 Redis 往返开销），其他方法走 Redis。
func RateLimitMiddleware(rl *RedisRateLimiter) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		ip := ClientRealIP(c)

		// GET 请求走本地内存限流，零网络开销
		if string(c.Request.Method()) == http.MethodGet {
			limiter := getLocalLimiter(ip)
			if !limiter.Allow() {
				response.TooManyRequests(c)
				c.Abort()
				return
			}
			c.Next(ctx)
			return
		}

		// 非 GET 请求走 Redis 分布式限流
		ipKey := fmt.Sprintf("shopmicro:ratelimit:ip:%s", ip)
		if !rl.Allow(ctx, ipKey) {
			response.TooManyRequests(c)
			c.Abort()
			return
		}

		c.Next(ctx)
	}
}
