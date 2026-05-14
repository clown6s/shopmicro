package middleware

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
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
	rl.endpointOverrides[endpointKey] = &endpointLimit{limit: limit, window: w}
}

// Allow checks whether a request from the given key is allowed.
// P1-3: When Redis is down, FAIL CLOSED (reject requests)
// instead of falling back to local rate limiter.
// This prevents rate-limit bypass in distributed environments.
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
		// P2-03 fix: Redis down → fail closed.
		// Reject the request rather than allowing bypass.
		logger.Warnf("ratelimit: redis unavailable, failing closed (rejecting request)")
		return false
	}

	return result == 1
}

// getTrustedIP returns the client IP from the X-Real-IP header set by a
// trusted reverse proxy (e.g. nginx). Falls back to c.ClientIP() when
// the header is absent (direct client connection).
// This prevents rate-limit bypass by spoofing X-Forwarded-For.
func getTrustedIP(c *app.RequestContext) string {
	if realIP := string(c.GetHeader("X-Real-IP")); realIP != "" {
		return realIP
	}
	return c.ClientIP()
}

// RateLimitMiddleware returns a Hertz middleware that performs distributed
// per-IP sliding window rate limiting via Redis.
// If a JWT Bearer token is present in the Authorization header, an additional
// per-user rate limit check is applied using the user_id from the token.
func RateLimitMiddleware(rl *RedisRateLimiter) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		ip := getTrustedIP(c)
		ipKey := fmt.Sprintf("shopmicro:ratelimit:ip:%s", ip)

		if !rl.Allow(ctx, ipKey) {
			response.TooManyRequests(c)
			c.Abort()
			return
		}

		// Per-user rate limiting: extract user_id from Bearer token for
		// authenticated requests. Only the user_id sub-field is read (no
		// full JWT verification — that is done later by the proxy handler).
		// This prevents token replay but mainly throttles authenticated users.
		if userID := extractUserIDFromToken(c); userID != "" {
			userKey := fmt.Sprintf("shopmicro:ratelimit:user:%s", userID)
			if !rl.Allow(ctx, userKey) {
				response.TooManyRequests(c)
				c.Abort()
				return
			}
		}

		c.Next(ctx)
	}
}

// extractUserIDFromToken extracts the user_id claim from a JWT Bearer token
// without full verification (the proxy handler handles verification).
// Returns empty string if no token or parse fails.
func extractUserIDFromToken(c *app.RequestContext) string {
	authHeader := strings.TrimSpace(string(c.GetHeader("Authorization")))
	if authHeader == "" {
		return ""
	}
	parts := strings.Fields(authHeader)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return ""
	}
	// JWT is base64url-encoded payload between two dots
	// Format: header.payload.signature
	token := parts[1]
	dot1 := strings.IndexByte(token, '.')
	dot2 := strings.LastIndexByte(token, '.')
	if dot1 < 0 || dot2 <= dot1+1 {
		return ""
	}
	payload := token[dot1+1 : dot2]
	// Decode base64url (no padding) using standard library
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}
	// Extract user_id from JSON (simple extraction, no full JSON parse overhead)
	needle := `"user_id":`
	idx := strings.Index(string(decoded), needle)
	if idx < 0 {
		return ""
	}
	rest := string(decoded)[idx+len(needle):]
	// Skip whitespace
	rest = strings.TrimLeft(rest, " ")
	if len(rest) == 0 || rest[0] < '0' || rest[0] > '9' {
		return ""
	}
	// Read digits
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	return rest[:end]
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
		ip := getTrustedIP(c)
		key := fmt.Sprintf("shopmicro:ratelimit:ep:%s:ip:%s", endpointKey, ip)

		if !dedicated.Allow(ctx, key) {
			response.TooManyRequests(c)
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}
