package middleware

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"go.opentelemetry.io/otel/trace"
)

// requestIDHeader is the header name for the request ID.
const requestIDHeader = "X-Request-Id"

// TraceIDMiddleware sets the X-Trace-Id response header so callers can
// correlate responses with distributed traces. It does NOT create its own
// span — hertztracing.ServerMiddleware handles that.
// P3-04 fix: If no otel span is available, generates an independent request ID
// using cryptographically insecure random (sufficient for correlation purposes).
func TraceIDMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Next(ctx)

		// After hertztracing has created the span, extract trace ID.
		span := trace.SpanFromContext(ctx)
		if span.SpanContext().IsValid() {
			c.Response.Header.Set("X-Trace-Id", span.SpanContext().TraceID().String())
		}

		// P3-04 fix: Always set X-Request-Id header for request correlation
		// Even if otel tracing is disabled, we still provide a correlation ID.
		existingReqID := c.Request.Header.Get(requestIDHeader)
		if existingReqID != "" {
			c.Response.Header.Set(requestIDHeader, existingReqID)
		} else {
			c.Response.Header.Set(requestIDHeader, generateRequestID())
		}
	}
}

// requestIDGen contains shared state for request ID generation.
var requestIDGen struct {
	sync.Mutex
	rng     *rand.Rand
	counter uint64
}

func init() {
	// Seed with time + random for better distribution
	src := rand.NewSource(time.Now().UnixNano())
	requestIDGen.rng = rand.New(src)
}

// generateRequestID creates a pseudo-random request ID.
// Format: timestamp(10 hex) + random(14 hex) = 24 char string.
// Uses atomic counter to reduce collision probability.
// For production, consider using UUID v7 or a distributed ID generator.
func generateRequestID() string {
	const hexChars = "0123456789abcdef"

	now := time.Now().Unix()
	r1 := requestIDGen.rng.Int63()
	requestIDGen.Lock()
	r2 := requestIDGen.counter
	requestIDGen.counter++
	requestIDGen.Unlock()
	combined := uint64(now)<<32 | uint64(r1&0xffffffff) ^ (r2 << 48)

	buf := make([]byte, 24)
	for i := 23; i >= 0; i-- {
		buf[i] = hexChars[combined&0xf]
		combined >>= 4
	}
	return string(buf)
}

// SetRequestID retrieves or generates a request ID from the context.
// Use this in handlers that need access to the request ID.
func SetRequestID(ctx context.Context, c *app.RequestContext) string {
	reqID := c.Request.Header.Get(requestIDHeader)
	if reqID == "" {
		reqID = generateRequestID()
	}
	return reqID
}
