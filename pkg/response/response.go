package response

import (
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"shopmicro/pkg/errcode"
)

// R is the unified JSON response structure for all gateway endpoints.
//   - code 0:     success
//   - code 1xxxx: business error (e.g. user service BizError)
//   - code 5xxxx: infrastructure error (gateway itself)
type R struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// JSON sends a unified JSON response using a predefined errcode.ErrCode.
func JSON(c *app.RequestContext, code errcode.ErrCode, data interface{}) {
	c.JSON(httpStatus(code.Code), R{Code: code.Code, Message: code.Msg, Data: data})
}

// OK sends a successful response (code=0).
func OK(c *app.RequestContext, data interface{}) {
	c.JSON(http.StatusOK, R{Code: 0, Message: "ok", Data: data})
}

// BizErr sends a downstream business error using the parsed BizError.
// Uses httpStatus() to map biz error codes to proper HTTP status codes
// (e.g. 10014 Unauthorized → 401, 10015 Forbidden → 403).
// P3-14: In production, limits message length to prevent leaking internal details.
func BizErr(c *app.RequestContext, biz *errcode.BizError) {
	msg := biz.Message
	// P3-14: Truncate overly long messages — internal errors often include
	// stack traces or SQL details that shouldn't reach the client
	if len(msg) > 128 {
		msg = "internal error"
	}
	c.JSON(httpStatus(int(biz.Code)), R{Code: int(biz.Code), Message: msg})
}

// Fail sends a generic error with custom HTTP status code.
func Fail(c *app.RequestContext, httpCode int, bizCode int, msg string) {
	c.JSON(httpCode, R{Code: bizCode, Message: msg})
}

// Unauthorized sends a 401 response.
// P0 fix: use errcode.CodeGwToken (50003) instead of hardcoded 401,
// keeping gateway error codes in the 5xxxx infrastructure range.
func Unauthorized(c *app.RequestContext, msg string) {
	c.JSON(http.StatusUnauthorized, R{Code: int(errcode.CodeGwToken), Message: msg})
}

// TooManyRequests sends a 429 response.
// P0 fix: use errcode.CodeGwRateLimit (50006)
func TooManyRequests(c *app.RequestContext) {
	c.JSON(http.StatusTooManyRequests, R{Code: int(errcode.CodeGwRateLimit), Message: "too many requests"})
}

// ServiceUnavailable sends a 503 response.
// P0 fix: use errcode.CodeGwSrvUnavailable (50007)
func ServiceUnavailable(c *app.RequestContext) {
	c.JSON(http.StatusServiceUnavailable, R{Code: int(errcode.CodeGwSrvUnavailable), Message: "service temporarily unavailable"})
}

// InternalError sends a 500 response with a generic message.
// P0 fix: use errcode.CodeGwServer (50002)
func InternalError(c *app.RequestContext) {
	c.JSON(http.StatusInternalServerError, R{Code: int(errcode.CodeGwServer), Message: "internal server error"})
}

// httpStatus maps a business error code to the appropriate HTTP status code.
//   - 0          → 200 OK
//   - 10014      → 401 Unauthorized (user service)
//   - 10015      → 403 Forbidden    (user service)
//   - 10000-49999 → 400 Bad Request (other business errors)
//   - 50000+     → 500 Internal Server Error (infrastructure error)
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
