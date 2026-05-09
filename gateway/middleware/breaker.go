package middleware

import (
	"context"
	"errors"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/sony/gobreaker"
)

var cb *gobreaker.CircuitBreaker

func init() {

	cb = gobreaker.NewCircuitBreaker(
		gobreaker.Settings{
			Name: "gateway",

			MaxRequests: 5,

			Interval: time.Minute,

			Timeout: 10 * time.Second,

			ReadyToTrip: func(
				counts gobreaker.Counts,
			) bool {

				return counts.ConsecutiveFailures >= 5
			},
		},
	)
}

func CircuitBreakerMiddleware() app.HandlerFunc {

	return func(ctx context.Context, c *app.RequestContext) {

		_, err := cb.Execute(func() (interface{}, error) {

			c.Next(ctx)

			status := c.Response.StatusCode()

			if status >= 500 {
				return nil, errors.New("server error")
			}

			return nil, nil
		})

		if err != nil {

			c.JSON(503, map[string]interface{}{
				"code": 503,
				"msg":  "service temporarily unavailable",
			})

			c.Abort()
			return
		}
	}
}
