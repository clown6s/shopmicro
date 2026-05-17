package response

import (
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/logger"
)

// R 是所有 gateway 端点统一的 JSON 响应结构。
//   - code 0:     成功
//   - code 1xxxx: 业务错误（例如用户服务 BizError）
//   - code 5xxxx: 基础设施错误（gateway 自身）
type R struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// JSON 使用预定义的 errcode.ErrCode 发送统一 JSON 响应。
func JSON(c *app.RequestContext, code errcode.ErrCode, data interface{}) {
	c.JSON(httpStatus(code.Code), R{Code: code.Code, Message: code.Msg, Data: data})
}

// OK 发送成功响应（code=0）。
func OK(c *app.RequestContext, data interface{}) {
	c.JSON(http.StatusOK, R{Code: 0, Message: "ok", Data: data})
}

// BizErr 使用解析后的 BizError 发送下游业务错误。
// 使用 httpStatus() 将业务错误码映射到适当的 HTTP 状态码
//（例如 10014 Unauthorized → 401, 10015 Forbidden → 403）。
// P3-14: 在生产环境中，限制消息长度以防止泄漏内部细节。
func BizErr(c *app.RequestContext, biz *errcode.BizError) {
	msg := biz.Message
	// P3-14: 截断过长的消息 — 内部错误通常包含
	// 不应传递给客户端的堆栈跟踪或 SQL 详情
	if len(msg) > 128 {
		logger.Warnf("biz error message truncated (code=%d, len=%d): %s", biz.Code, len(msg), msg)
		msg = "internal error"
	}
	c.JSON(httpStatus(int(biz.Code)), R{Code: int(biz.Code), Message: msg})
}

// Fail 使用自定义 HTTP 状态码发送通用错误。
func Fail(c *app.RequestContext, httpCode int, bizCode int, msg string) {
	c.JSON(httpCode, R{Code: bizCode, Message: msg})
}

// Unauthorized 发送 401 响应。
// P0 修复：使用 errcode.CodeGwToken (50003) 而非硬编码 401，
// 将 gateway 错误码保持在 5xxxx 基础设施范围内。
func Unauthorized(c *app.RequestContext, msg string) {
	c.JSON(http.StatusUnauthorized, R{Code: int(errcode.CodeGwToken), Message: msg})
}

// TooManyRequests 发送 429 响应。
// P0 修复：使用 errcode.CodeGwRateLimit (50006)
func TooManyRequests(c *app.RequestContext) {
	c.JSON(http.StatusTooManyRequests, R{Code: int(errcode.CodeGwRateLimit), Message: "too many requests"})
}

// ServiceUnavailable 发送 503 响应。
// P0 修复：使用 errcode.CodeGwSrvUnavailable (50007)
func ServiceUnavailable(c *app.RequestContext) {
	c.JSON(http.StatusServiceUnavailable, R{Code: int(errcode.CodeGwSrvUnavailable), Message: "service temporarily unavailable"})
}

// InternalError 发送 500 响应及通用消息。
// P0 修复：使用 errcode.CodeGwServer (50002)
func InternalError(c *app.RequestContext) {
	c.JSON(http.StatusInternalServerError, R{Code: int(errcode.CodeGwServer), Message: "internal server error"})
}

// httpStatus 将业务错误码映射到适当的 HTTP 状态码。
//   - 0          → 200 OK
//   - 10014      → 401 Unauthorized（用户服务）
//   - 10015      → 403 Forbidden（用户服务）
//   - 10000-49999 → 400 Bad Request（其他业务错误）
//   - 50000+     → 500 Internal Server Error（基础设施错误）
func httpStatus(code int) int {
	if code == 0 {
		return consts.StatusOK
	}
	switch code {
	case int(errcode.CodeUserUnauthorized):
		return consts.StatusUnauthorized
	case int(errcode.CodeUserForbidden):
		return consts.StatusForbidden
	}
	if code < 50000 {
		return consts.StatusBadRequest
	}
	return consts.StatusInternalServerError
}
