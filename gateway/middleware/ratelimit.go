package middleware

import (
	"context"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"golang.org/x/time/rate"

	"shopmicro/gateway/conf"
	"shopmicro/pkg/response"
)

// RateLimiter manages per-IP token buckets with automatic eviction.
type RateLimiter struct {
	mu         sync.Mutex
	visitors   map[string]*visitor
	rate       float64
	burst      int
	maxIPs     int
	visitorTTL time.Duration
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter creates a configurable rate limiter.
func NewRateLimiter(cfg conf.RateLimit, cleanupInterval, visitorTTL time.Duration) *RateLimiter {
	rl := &RateLimiter{
		visitors:   make(map[string]*visitor),
		rate:       cfg.Rate,
		burst:      cfg.Burst,
		maxIPs:     cfg.MaxIPs,
		visitorTTL: visitorTTL,
	}
	go rl.cleanupLoop(cleanupInterval)
	return rl
}

// cleanupLoop periodically removes expired visitor entries.
func (rl *RateLimiter) cleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		for ip, v := range rl.visitors {
			if time.Since(v.lastSeen) > rl.visitorTTL {
				delete(rl.visitors, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// getLimiter returns the token bucket for the given IP.
func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if len(rl.visitors) >= rl.maxIPs {
		if v, ok := rl.visitors[ip]; ok {
			v.lastSeen = time.Now()
			return v.limiter
		}
		// Map full + unknown IP → shared restrictive limiter.
		return rate.NewLimiter(1, 3)
	}

	v, exists := rl.visitors[ip]
	if !exists {
		limiter := rate.NewLimiter(rate.Limit(rl.rate), rl.burst)
		rl.visitors[ip] = &visitor{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}

	v.lastSeen = time.Now()
	return v.limiter
}

// RateLimitMiddleware returns a Hertz middleware that limits per-IP request rate.
// Callers must create the RateLimiter once at startup and pass it here.
func RateLimitMiddleware(rl *RateLimiter) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		limiter := rl.getLimiter(c.ClientIP())
		if !limiter.Allow() {
			response.TooManyRequests(c)
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}
