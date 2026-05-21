package middleware

import (
	"bytes"
	"context"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/compress"
)

// GzipMiddleware 轻量 gzip 压缩中间件。
// 与 pre-gzip 缓存配合：当 pre-gzip 已设置 Content-Encoding: gzip 时跳过，
// 避免对已压缩数据重复压缩。
func GzipMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		// 客户端不接受 gzip 则直接跳过
		ae := string(c.Request.Header.Peek("Accept-Encoding"))
		if !strings.Contains(ae, "gzip") && strings.TrimSpace(ae) != "*" {
			c.Next(ctx)
			return
		}

		c.Next(ctx)

		// pre-gzip 缓存已处理时跳过，避免双重压缩
		if len(c.Response.Header.Peek("Content-Encoding")) > 0 {
			return
		}

		body := c.Response.Body()
		if len(body) == 0 {
			return
		}

		c.Header("Content-Encoding", "gzip")

		// 追加 Vary 而非覆盖，保留 CORS 设的 "Vary: Origin"
		existing := string(c.Response.Header.Peek("Vary"))
		if existing != "" {
			c.Header("Vary", existing+", Accept-Encoding")
		} else {
			c.Header("Vary", "Accept-Encoding")
		}

		gzipped := compress.AppendGzipBytesLevel(nil, body, 1) // BestSpeed
		c.Response.SetBodyStream(bytes.NewBuffer(gzipped), len(gzipped))
	}
}
