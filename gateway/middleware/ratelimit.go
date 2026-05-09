package middleware

import (
	"context"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"golang.org/x/time/rate"
)

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

var (
	visitors = make(map[string]*visitor)
	mu       sync.Mutex
)

const (
	// visitorTTL controls how long an idle visitor entry stays in the map.
	visitorTTL = 3 * time.Minute
	// cleanupInterval controls how often stale visitors are evicted.
	cleanupInterval = 1 * time.Minute
	// maxVisitors caps the total number of tracked IPs to prevent OOM.
	maxVisitors = 100_000
)

func init() {
	go cleanupStaleVisitors()
}

// cleanupStaleVisitors periodically removes expired visitor entries.
func cleanupStaleVisitors() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		mu.Lock()
		for ip, v := range visitors {
			if time.Since(v.lastSeen) > visitorTTL {
				delete(visitors, ip)
			}
		}
		mu.Unlock()
	}
}

func getVisitor(ip string) *rate.Limiter {

	mu.Lock()
	defer mu.Unlock()

	// Hard cap: reject new entries if we've hit the limit.
	if len(visitors) >= maxVisitors {
		// Reset existing entry's lastSeen so it won't be evicted while active.
		if v, ok := visitors[ip]; ok {
			v.lastSeen = time.Now()
			return v.limiter
		}
		// Unknown IP and map is full — apply a shared restrictive limiter.
		limiter := rate.NewLimiter(1, 3) // 1 req/s, burst 3
		return limiter
	}

	v, exists := visitors[ip]

	if !exists {

		limiter := rate.NewLimiter(
			10, // 每秒 10 个请求
			20, // burst
		)

		visitors[ip] = &visitor{
			limiter:  limiter,
			lastSeen: time.Now(),
		}

		return limiter
	}

	v.lastSeen = time.Now()

	return v.limiter
}

func RateLimitMiddleware() app.HandlerFunc {

	return func(ctx context.Context, c *app.RequestContext) {

		ip := c.ClientIP()

		limiter := getVisitor(ip)

		if !limiter.Allow() {

			c.JSON(429, map[string]interface{}{
				"code": 429,
				"msg":  "too many requests",
			})

			c.Abort()
			return
		}

		c.Next(ctx)
	}
}
