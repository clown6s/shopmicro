package middleware

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"sync/atomic"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"go.opentelemetry.io/otel/trace"
)

// requestIDHeader is the header name for the request ID.
const requestIDHeader = "X-Request-Id"

// TraceIDMiddleware sets the X-Trace-Id response header so callers can
// using crypto/rand for cryptographic quality.
func TraceIDMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Next(ctx)

		// After hertztracing has created the span, extract trace ID.
		span := trace.SpanFromContext(ctx)
		if span.SpanContext().IsValid() {
			c.Response.Header.Set("X-Trace-Id", span.SpanContext().TraceID().String())
		}

		// Even if otel tracing is disabled, we still provide a correlation ID.
		existingReqID := c.Request.Header.Get(requestIDHeader)
		if existingReqID != "" {
			c.Response.Header.Set(requestIDHeader, existingReqID)
		} else {
			c.Response.Header.Set(requestIDHeader, generateRequestID())
		}
	}
}

// requestIDCounter is an atomic counter used to reduce collision probability.
var requestIDCounter atomic.Uint64

// generateRequestID creates a pseudo-random request ID.
// Format: timestamp(10 hex) + random(14 hex) = 24 char string.
func generateRequestID() string {
	const hexChars = "0123456789abcdef"

	now := time.Now().Unix()

	// H1: Read 8 bytes from crypto/rand (64 bits of cryptographic entropy)
	var rngBytes [8]byte
	_, _ = rand.Read(rngBytes[:])
	r1 := binary.BigEndian.Uint64(rngBytes[:])

	// H2: Lock-free atomic counter
	r2 := requestIDCounter.Add(1)
	combined := uint64(now)<<32 | (r1 & 0xffffffff) ^ (r2 << 48)

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
