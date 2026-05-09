package middleware

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"

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
	rdb               *redis.Client
	limit             int           // max requests per window (default)
	window            time.Duration // sliding window duration (default)
	endpointOverrides map[string]*endpointLimit
	// P1-3: Local fallback rate limiters (token bucket) when Redis is down.
	localLimiters sync.Map // map[string]*rate.Limiter
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
	rl.endpointOverrides[endpointKey] = &endpointLimit{limit: limit, window: w}
}

// Allow checks whether a request from the given key is allowed.
// P1-3: Falls back to local in-memory rate limiter when Redis is down.
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
		// P1-3: Redis down → fall back to local in-memory rate limiter.
		// Prevents rate limit bypass when Redis is unavailable (e.g. DDoS + brute force).
		return rl.localAllow(key)
	}

	return result == 1
}

// localAllow provides in-memory token-bucket rate limiting as a fallback
// when Redis is unavailable. Uses per-key limiters stored in a sync.Map.
func (rl *RedisRateLimiter) localAllow(key string) bool {
	// tokens per second = limit / window_seconds
	r := rate.Limit(float64(rl.limit) / rl.window.Seconds())
	limiter, _ := rl.localLimiters.LoadOrStore(key, rate.NewLimiter(r, 1))
	return limiter.(*rate.Limiter).Allow()
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

// P1-15: EndpointRateLimitMiddleware creates a middleware that applies
// a specific rate limit to a single endpoint (service/method).
// Use this for sensitive endpoints like Login.
func EndpointRateLimitMiddleware(rl *RedisRateLimiter, endpointKey string) app.HandlerFunc {
	el, ok := rl.endpointOverrides[endpointKey]
	if !ok {
		// No override configured, fall through to global rate limit
		return func(ctx context.Context, c *app.RequestContext) {
			c.Next(ctx)
		}
	}
	// Create a dedicated limiter for this endpoint
	dedicated := &RedisRateLimiter{
		rdb:    rl.rdb,
		limit:  el.limit,
		window: el.window,
	}
	return func(ctx context.Context, c *app.RequestContext) {
		ip := c.ClientIP()
		key := fmt.Sprintf("shopmicro:ratelimit:ep:%s:ip:%s", endpointKey, ip)

		if !dedicated.Allow(ctx, key) {
			response.TooManyRequests(c)
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}
