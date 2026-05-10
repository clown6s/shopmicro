package middleware

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/sony/gobreaker"

	"shopmicro/pkg/response"
)

// perServiceCB stores one circuit breaker per service name.
// Value is *gobreaker.CircuitBreaker.
var perServiceCB sync.Map // map[string]*gobreaker.CircuitBreaker

// RemoveCircuitBreaker removes the circuit breaker for a service.
// Call this when a service is deregistered (e.g. etcd watch delete event)
// to prevent unbounded memory growth in perServiceCB.
func RemoveCircuitBreaker(service string) {
	perServiceCB.Delete(service)
}

// CircuitBreakerMiddleware returns a Hertz middleware with a circuit breaker.
// P1-02 fix: Creates one CircuitBreaker per downstream service (extracted
// from the :service URL parameter) instead of one shared breaker.
// Settings are the same for all services; override per-service by extending
// this function if needed.
func CircuitBreakerMiddleware(maxRequests int, interval, timeout time.Duration, consecutiveFailures uint32) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		// Extract service name from URL: /api/v1/:service/:method
		service := c.Param("service")
		if service == "" {
			c.Next(ctx)
			return
		}

		// Get or create the circuit breaker for this service
		cbKey := service
		cbI, _ := perServiceCB.LoadOrStore(cbKey, gobreaker.NewCircuitBreaker(gobreaker.Settings{
			Name:        "gateway:" + service,
			MaxRequests: uint32(maxRequests),
			Interval:    interval,
			Timeout:     timeout,
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.ConsecutiveFailures >= consecutiveFailures
			},
		}))

		cb := cbI.(*gobreaker.CircuitBreaker)

		_, err := cb.Execute(func() (interface{}, error) {
			c.Next(ctx)

			// P1-04 fix: only check HTTP status code.
			// Reading c.Response.Body() after c.Next() is not reliable
			// (body may have been flushed). The circuit breaker now trips
			// on HTTP 5xx responses only.
			if c.Response.StatusCode() >= 500 {
				return nil, errors.New("downstream returned server error")
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

// GetCircuitBreakerStatus returns a snapshot of all circuit breaker states.
// Useful for a debug/service-status endpoint.
func GetCircuitBreakerStatus() map[string]string {
	result := make(map[string]string)
	perServiceCB.Range(func(key, value interface{}) bool {
		cb := value.(*gobreaker.CircuitBreaker)
		result[key.(string)] = cb.State().String()
		return true
	})
	return result
}
