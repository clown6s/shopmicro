package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/sony/gobreaker"

	"shopmicro/pkg/response"
)

// CircuitBreakerMiddleware returns a Hertz middleware with a circuit breaker
// configured by the given parameters. The breaker is created once per call.
// P1-4: Also inspects the response body for 5xxxx infrastructure error codes,
// not just HTTP status — downstream may return 200 with {"code":50001}.
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

			// Check HTTP status code
			if c.Response.StatusCode() >= 500 {
				return nil, errors.New("server error")
			}

			// P1-4: Also check response body for infrastructure error codes (5xxxx).
			// Downstream may return HTTP 200 but include {"code":50001, ...} in body.
			body := c.Response.Body()
			if len(body) > 0 && bytes.Contains(body, []byte(`"code"`)) {
				var result struct {
					Code int `json:"code"`
				}
				if json.Unmarshal(body, &result) == nil && result.Code >= 50000 {
					return nil, errors.New("infrastructure error in response")
				}
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
