package middleware

import (
	"context"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"shopmicro/pkg/logger"
	"shopmicro/pkg/response"
)

// stackTracer 由 recovery_debug.go 通过构建标签注入。
// 在生产构建（-tags prod）中此值为 nil，
// runtime/debug 不会链接到二进制中。
var stackTracer func() string

// RecoveryMiddleware 捕获 panic，记录错误日志，并返回统一的
// 错误响应。在生产模式下会抑制完整的堆栈跟踪，
// 防止内部架构细节泄露到日志中。
//
// 使用 -tags prod 编译时，debug.Stack() 代码会被完全排除
//（通过 recovery_debug.go 的构建标签）。运行模式检查作为
// 额外的纵深防御层。
func RecoveryMiddleware(mode string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		defer func() {
			if err := recover(); err != nil {
				if stackTracer != nil && !strings.EqualFold(mode, "prod") {
					logger.Errorf(
						"[PANIC] method=%s path=%s err=%v stack=%s",
						string(c.Method()), string(c.Path()),
						err,
						stackTracer(),
					)
				} else {
					logger.Errorf(
						"[PANIC] method=%s path=%s err=%v",
						string(c.Method()), string(c.Path()),
						err,
					)
				}
				// P2-17 修复：先中止处理链，防止下游处理器
				// 在错误响应已提交后继续写入。
				c.Abort()
				response.InternalError(c)
			}
		}()
		c.Next(ctx)
	}
}
