package middleware

import (
	"context"
	"errors"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/sony/gobreaker"

	"shopmicro/pkg/response"
)

// CircuitBreakerMiddleware returns a Hertz middleware with a circuit breaker
// configured by the given parameters. The breaker is created once per call.
func CircuitBreakerMiddleware(maxRequests int, interval, timeout time.Duration, consecutiveFailures uint32) app.HandlerFunc {

	cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "gateway",
		MaxRequests: uint32(maxRequests),
		Interval:    interval,
		Timeout:     timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= consecutiveFailures
		},
	})

	return func(ctx context.Context, c *app.RequestContext) {
		_, err := cb.Execute(func() (interface{}, error) {
			c.Next(ctx)
			if c.Response.StatusCode() >= 500 {
				return nil, errors.New("server error")
			}
			return nil, nil
		})
		if err != nil {
			response.ServiceUnavailable(c)
			c.Abort()
			return
		}
	}
}
