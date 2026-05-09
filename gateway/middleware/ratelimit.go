package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	goredis "github.com/redis/go-redis/v9"

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
	rdb    *redis.Client
	limit  int           // max requests per window
	window time.Duration // sliding window duration
}

// NewRedisRateLimiter creates a Redis-based sliding window rate limiter.
func NewRedisRateLimiter(rdb *redis.Client, limit int, window time.Duration) *RedisRateLimiter {
	return &RedisRateLimiter{
		rdb:    rdb,
		limit:  limit,
		window: window,
	}
}

// Allow checks whether a request from the given key is allowed.
func (rl *RedisRateLimiter) Allow(ctx context.Context, key string) bool {
	now := time.Now()
	windowStart := now.Add(-rl.window)
	member := fmt.Sprintf("%d:%06d", now.UnixNano(), now.Nanosecond()%1000000)

	result, err := slidingWindowLua.Run(ctx, rl.rdb.RawClient(), []string{key},
		windowStart.UnixMilli(),
		now.UnixMilli(),
		rl.limit,
		rl.window.Milliseconds(),
		member,
	).Int()

	if err != nil {
		// Redis error → fail-open: rate limiting is protection, not a gatekeeper.
		return true
	}

	return result == 1
}

// RateLimitMiddleware returns a Hertz middleware that performs distributed
// per-IP sliding window rate limiting via Redis.
func RateLimitMiddleware(rl *RedisRateLimiter) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		ip := c.ClientIP()
		key := fmt.Sprintf("shopmicro:ratelimit:ip:%s", ip)

		if !rl.Allow(ctx, key) {
			response.TooManyRequests(c)
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}
