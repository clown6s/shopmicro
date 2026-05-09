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

func getVisitor(ip string) *rate.Limiter {

	mu.Lock()
	defer mu.Unlock()

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
