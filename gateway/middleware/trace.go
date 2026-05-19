package middleware

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"sync"
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

// P3-06: 复用 buffer 减少 GC 压力
var requestIDBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 24)
		return &buf
	},
}

// generateRequestID 创建一个密码学质量的唯一请求 ID。
// 格式: 时间戳(8 位十六进制) + 随机数(8 位十六进制) + 计数器(8 位十六进制) = 24 字符字符串。
func generateRequestID() string {
	const hexChars = "0123456789abcdef"

	now := time.Now().UnixMilli()

	// 从 crypto/rand 读取 8 字节（64 位密码学熵）
	var rngBytes [8]byte
	if _, err := rand.Read(rngBytes[:]); err != nil {
		// P1-06: crypto/rand 失败说明系统熵源不可用，使用时间戳填充 fallback。
		// 请求 ID 非安全关键路径（有 timestamp + counter 兜底），不 panic。
		logger.Errorf("trace: crypto/rand unavailable (system entropy source issue): %v — using timestamp fallback", err)
		rngBytes[0] = byte(now)
		rngBytes[1] = byte(now >> 8)
		rngBytes[2] = byte(now >> 16)
		rngBytes[3] = byte(now >> 24)
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

	// P3-06: 从 pool 获取 buffer，用完归还
	bufPtr := requestIDBufPool.Get().(*[]byte)
	buf := *bufPtr
	for i := 2; i >= 0; i-- {
		for j := 7; j >= 0; j-- {
			buf[i*8+j] = hexChars[parts[i]&0xf]
			parts[i] >>= 4
		}
	}
	result := string(buf)
	requestIDBufPool.Put(bufPtr)
	return result
}
