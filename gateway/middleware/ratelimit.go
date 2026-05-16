package middleware

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	goredis "github.com/redis/go-redis/v9"

	"shopmicro/pkg/logger"
	"shopmicro/pkg/redis"
	"shopmicro/pkg/response"
)

// slidingWindowLua is an atomic Redis Lua script that implements sliding window rate limiting.
//
// KEYS[1] = rate limit key (e.g. "ratelimit:ip:1.2.3.4")
// ARGV[1] = window start timestamp in ms (now - window)
// ARGV[2] = current timestamp in ms
// ARGV[3] = limit (max requests allowed in window)
// ARGV[4] = window TTL in ms
// ARGV[5] = unique member ID
//
// Returns: 1 = allowed, 0 = rejected
var slidingWindowLua = goredis.NewScript(`
local key = KEYS[1]
local window_start = tonumber(ARGV[1])
local now = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])
local member = ARGV[5]

-- Remove expired entries outside the sliding window
redis.call('ZREMRANGEBYSCORE', key, '-inf', window_start)

-- Count remaining entries in the window
local count = redis.call('ZCARD', key)

if count >= limit then
    return 0
end

-- Add current request
redis.call('ZADD', key, now, member)

-- Set key TTL to auto-cleanup
redis.call('PEXPIRE', key, ttl)

return 1
`)

// RedisRateLimiter implements distributed sliding window rate limiting via Redis.
type RedisRateLimiter struct {
	mu                sync.RWMutex // R4-P1-01: protects endpointOverrides
	rdb               *redis.Client
	limit             int           // max requests per window (default)
	window            time.Duration // sliding window duration (default)
	endpointOverrides map[string]*endpointLimit
}

// endpointLimit holds per-endpoint rate limit configuration
type endpointLimit struct {
	limit  int
	window time.Duration
}

// NewRedisRateLimiter creates a Redis-based sliding window rate limiter.
func NewRedisRateLimiter(rdb *redis.Client, limit int, window time.Duration) *RedisRateLimiter {
	return &RedisRateLimiter{
		rdb:               rdb,
		limit:             limit,
		window:            window,
		endpointOverrides: make(map[string]*endpointLimit),
	}
}

// SetEndpointLimit sets a per-endpoint rate limit override.
// endpointKey format: "service/method" (e.g. "user/Login")
func (rl *RedisRateLimiter) SetEndpointLimit(endpointKey string, limit int, windowS int) {
	w := rl.window
	if windowS > 0 {
		w = time.Duration(windowS) * time.Second
	}
	rl.mu.Lock()
	rl.endpointOverrides[endpointKey] = &endpointLimit{limit: limit, window: w}
	rl.mu.Unlock()
}

// GetEndpointLimit returns the per-endpoint limit if one is configured.
func (rl *RedisRateLimiter) GetEndpointLimit(endpointKey string) *endpointLimit {
	rl.mu.RLock()
	el, ok := rl.endpointOverrides[endpointKey]
	rl.mu.RUnlock()
	if !ok {
		return nil
	}
	return el
}

// NewEndpointCheckFn returns a function compatible with proxy.WithEndpointRateLimit.
// It checks if the endpointKey has a per-endpoint override and applies strict rate limiting.
func (rl *RedisRateLimiter) NewEndpointCheckFn() func(ctx context.Context, endpointKey, ip string) bool {
	return func(ctx context.Context, endpointKey, ip string) bool {
		el := rl.GetEndpointLimit(endpointKey)
		if el == nil {
			return true // no endpoint-specific limit configured
		}
		key := fmt.Sprintf("shopmicro:ratelimit:ep:%s:ip:%s", endpointKey, ip)
		now := time.Now()
		member := uniqueMember(now)
		return rl.allowWithConfig(ctx, key, now, el.window, el.limit, member)
	}
}

// Allow checks whether a request from the given key is allowed.
// P1-3: When Redis is down, FAIL CLOSED (reject requests)
// instead of falling back to local rate limiter.
// This prevents rate-limit bypass in distributed environments.
func (rl *RedisRateLimiter) Allow(ctx context.Context, key string) bool {
	now := time.Now()
	member := uniqueMember(now)
	return rl.allowWithConfig(ctx, key, now, rl.window, rl.limit, member)
}

// allowWithConfig checks rate limiting with custom window/limit.
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
		// P2-03 fix: Redis down → fail closed.
		// Reject the request rather than allowing bypass.
		logger.Warnf("ratelimit: redis unavailable, failing closed (rejecting request)")
		return false
	}

	return result == 1
}

// getClientIP returns the real client IP from the TCP connection.
func getClientIP(c *app.RequestContext) string {
	return c.ClientIP()
}

// uniqueMember returns a rate-limit member string that is guaranteed unique
// even for concurrent requests at the same nanosecond.
// Format: {unixNano}:{randSuffix}
func uniqueMember(now time.Time) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%d:%08x", now.UnixNano(), binary.BigEndian.Uint64(b[:]))
}

// RateLimitMiddleware returns a Hertz middleware that performs distributed
// per-IP sliding window rate limiting via Redis.
// R2-P2-01: Removed per-user rate limiting from middleware (was based on unverified
// JWT payload, allowing bypass). Per-endpoint rate limiting in the proxy handler
// provides stricter, method-independent protection for sensitive operations.
func RateLimitMiddleware(rl *RedisRateLimiter) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		ip := getClientIP(c)
		ipKey := fmt.Sprintf("shopmicro:ratelimit:ip:%s", ip)

		if !rl.Allow(ctx, ipKey) {
			response.TooManyRequests(c)
			c.Abort()
			return
		}

		c.Next(ctx)
	}
}
