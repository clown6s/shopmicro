package middleware

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"sync/atomic"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"go.opentelemetry.io/otel/trace"

	"shopmicro/pkg/logger"
)

// requestIDHeader 是请求 ID 的 HTTP 头名称。
const requestIDHeader = "X-Request-Id"

// TraceIDMiddleware 设置 X-Trace-Id 响应头，使调用方可以
// 使用 crypto/rand 生成密码学质量的追踪 ID。
func TraceIDMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Next(ctx)

		// 在 hertztracing 创建 span 之后，提取 trace ID。
		span := trace.SpanFromContext(ctx)
		if span.SpanContext().IsValid() {
			c.Response.Header.Set("X-Trace-Id", span.SpanContext().TraceID().String())
		}

		// 即使 otel 链路追踪被禁用，我们仍然提供一个关联 ID。
		existingReqID := c.Request.Header.Get(requestIDHeader)
		if existingReqID != "" {
			c.Response.Header.Set(requestIDHeader, existingReqID)
		} else {
			c.Response.Header.Set(requestIDHeader, generateRequestID())
		}
	}
}

// requestIDCounter 是一个原子计数器，用于降低碰撞概率。
var requestIDCounter atomic.Uint64

// generateRequestID 创建一个密码学质量的唯一请求 ID。
// 格式: 时间戳(8 位十六进制) + 随机数(8 位十六进制) + 计数器(8 位十六进制) = 24 字符字符串。
func generateRequestID() string {
	const hexChars = "0123456789abcdef"

	now := time.Now().Unix()

	// 从 crypto/rand 读取 8 字节（64 位密码学熵）
	var rngBytes [8]byte
	if _, err := rand.Read(rngBytes[:]); err != nil {
		logger.Warnf("trace: crypto/rand read failed: %v — using zero entropy", err)
	}
	r1 := binary.BigEndian.Uint64(rngBytes[:])

	// 无锁原子计数器，降低碰撞概率
	r2 := requestIDCounter.Add(1)

	// 三个 uint64 分别提供时间戳、随机数、计数器
	parts := [3]uint64{
		uint64(now) & 0xffffffff, // 时间戳低 32 位
		r1,                       // 密码学随机
		r2,                       // 单调递增计数器
	}

	buf := make([]byte, 24)
	for i := 2; i >= 0; i-- {
		for j := 7; j >= 0; j-- {
			buf[i*8+j] = hexChars[parts[i]&0xf]
			parts[i] >>= 4
		}
	}
	return string(buf)
}
