package response

import (
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/logger"
)

const (
	// maxBizErrMsgLen 业务错误消息最大长度，超过则截断为 "internal error"。
	// P2-14: 提取为命名常量，避免魔法数字。
	maxBizErrMsgLen = 128
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

// OK 发送成功响应（code=0）。
func OK(c *app.RequestContext, data interface{}) {
	c.JSON(http.StatusOK, R{Code: 0, Message: "ok", Data: data})
}

// BizErr 使用解析后的 BizError 发送下游业务错误。
// HTTP 状态码优先使用 BizError.HTTPStatus（精确映射），
// 若 HTTPStatus == 0 则 fallback 到 HttpStatus(code) 按错误码范围推断。
// P3-14: 在生产环境中，限制消息长度以防止泄漏内部细节。
func BizErr(c *app.RequestContext, biz *errcode.BizError) {
	// R5-P1-03: 防御 nil BizError，避免调用方忽略 ParseBizError bool 返回值时 panic
	if biz == nil {
		InternalError(c)
		return
	}
	msg := biz.Message
	truncated := false
	// P3-14: 截断过长的消息 — 内部错误通常包含
	// 不应传递给客户端的堆栈跟踪或 SQL 详情
	if len(msg) > maxBizErrMsgLen {
		// P2-15: 不在日志中记录被截断的完整消息，防止敏感信息泄漏到日志文件。
		logger.Warnf("biz error message truncated (code=%d, len=%d)", biz.Code, len(msg))
		msg = "internal error"
		truncated = true
	}
	// 优先使用 BizError 自带的 HTTPStatus（精确映射），
	// 0 则 fallback 到按错误码范围的推断。
	httpCode := biz.HTTPStatus
	if httpCode <= 0 {
		httpCode = HttpStatus(int(biz.Code))
	}
	// 消息被截断为 "internal error" 时，强制使用 HTTP 500 避免语义矛盾
	// （400 暗示客户端错误，但实际是内部错误被截断）
	if truncated && httpCode < 500 {
		httpCode = consts.StatusInternalServerError
	}
	c.JSON(httpCode, R{Code: int(biz.Code), Message: msg})
}

// Fail 使用自定义 HTTP 状态码发送通用错误。
// P3-14: 限制消息长度以防止泄漏内部细节，与 BizErr() 行为一致。
func Fail(c *app.RequestContext, httpCode int, bizCode int, msg string) {
	if len(msg) > maxBizErrMsgLen {
		logger.Warnf("fail message truncated (bizCode=%d, len=%d)", bizCode, len(msg))
		msg = "internal error"
		if httpCode < 500 {
			httpCode = consts.StatusInternalServerError
		}
	}
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

// HttpStatus 将业务错误码映射到适当的 HTTP 状态码。
// 导出版本供 proxy.handleRPCResponse 复用，消除重复映射逻辑。
//
// 精确映射（优先匹配）：
//
//	User 服务:
//	  10009 NotFound      → 404 Not Found
//	  10010 TokenRequired  → 401 Unauthorized
//	  10011 TokenInvalid   → 401 Unauthorized
//	  10012 TokenMismatch  → 401 Unauthorized
//	  10013 Internal       → 500 Internal Server Error
//	  10014 Unauthorized   → 401 Unauthorized
//	  10015 Forbidden      → 403 Forbidden
//	  10019 AccountLocked  → 423 Locked
//
//	Gateway 服务:
//	  50003 Token          → 401 Unauthorized
//	  50004 NotFound       → 404 Not Found
//	  50005 Response       → 500 Internal Server Error
//	  50006 RateLimit      → 429 Too Many Requests
//	  50007 SrvUnavailable → 503 Service Unavailable
//	  50008 Forbidden      → 403 Forbidden
//	  50009 MethodNotAllowed → 405 Method Not Allowed
//	  50010 UnsupportedMediaType → 415 Unsupported Media Type
//	  50011 PayloadTooLarge → 413 Payload Too Large
//
// 范围回退：
//
//	0            → 200 OK
//	10000-49999  → 400 Bad Request（其他业务错误）
//	50000+       → 500 Internal Server Error（基础设施错误）
func HttpStatus(code int) int {
	if code == 0 {
		return consts.StatusOK
	}
	switch code {
	// User 服务精确映射
	case int(errcode.CodeUserNotFound):
		return consts.StatusNotFound
	case int(errcode.CodeUserTokenRequired), int(errcode.CodeUserTokenInvalid),
		int(errcode.CodeUserTokenMismatch), int(errcode.CodeUserUnauthorized):
		return consts.StatusUnauthorized
	case int(errcode.CodeUserForbidden):
		return consts.StatusForbidden
	case int(errcode.CodeUserAccountLocked):
		return http.StatusLocked
	case int(errcode.CodeUserInternal):
		return consts.StatusInternalServerError
	// Gateway 精确映射
	case int(errcode.CodeGwToken):
		return consts.StatusUnauthorized
	case int(errcode.CodeGwNotFound):
		return consts.StatusNotFound
	case int(errcode.CodeGwResponse):
		return consts.StatusInternalServerError
	case int(errcode.CodeGwRateLimit):
		return consts.StatusTooManyRequests
	case int(errcode.CodeGwSrvUnavailable):
		return consts.StatusServiceUnavailable
	case int(errcode.CodeGwForbidden):
		return consts.StatusForbidden
	case int(errcode.CodeGwMethodNotAllowed):
		return consts.StatusMethodNotAllowed
	case int(errcode.CodeGwUnsupportedMediaType):
		return consts.StatusUnsupportedMediaType
	case int(errcode.CodeGwPayloadTooLarge):
		return consts.StatusRequestEntityTooLarge
	}
	if code < 50000 {
		return consts.StatusBadRequest
	}
	return consts.StatusInternalServerError
}
