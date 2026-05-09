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
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data,omitempty"`
}

// JSON sends a unified JSON response using a predefined errcode.ErrCode.
func JSON(c *app.RequestContext, code errcode.ErrCode, data interface{}) {
	c.JSON(httpStatus(code.Code), R{Code: code.Code, Msg: code.Msg, Data: data})
}

// OK sends a successful response (code=0).
func OK(c *app.RequestContext, data interface{}) {
	c.JSON(http.StatusOK, R{Code: 0, Msg: "ok", Data: data})
}

// BizErr sends a downstream business error using the parsed BizError.
func BizErr(c *app.RequestContext, biz *errcode.BizError) {
	statusCode := http.StatusBadRequest
	if biz.Code >= 50000 {
		statusCode = http.StatusInternalServerError
	}
	c.JSON(statusCode, R{Code: int(biz.Code), Msg: biz.Message})
}

// Fail sends a generic error with custom HTTP status code.
func Fail(c *app.RequestContext, httpCode int, bizCode int, msg string) {
	c.JSON(httpCode, R{Code: bizCode, Msg: msg})
}

// Unauthorized sends a 401 response.
func Unauthorized(c *app.RequestContext, msg string) {
	c.JSON(http.StatusUnauthorized, R{Code: http.StatusUnauthorized, Msg: msg})
}

// TooManyRequests sends a 429 response.
func TooManyRequests(c *app.RequestContext) {
	c.JSON(http.StatusTooManyRequests, R{Code: 429, Msg: "too many requests"})
}

// ServiceUnavailable sends a 503 response.
func ServiceUnavailable(c *app.RequestContext) {
	c.JSON(http.StatusServiceUnavailable, R{Code: 503, Msg: "service temporarily unavailable"})
}

// InternalError sends a 500 response with a generic message.
func InternalError(c *app.RequestContext) {
	c.JSON(http.StatusInternalServerError, R{Code: 500, Msg: "internal server error"})
}

// httpStatus maps a business error code to the appropriate HTTP status code.
//   - 0          → 200 OK
//   - 10000-49999 → 400 Bad Request (business error)
//   - 50000+     → 500 Internal Server Error (infrastructure error)
func httpStatus(code int) int {
	if code == 0 {
		return consts.StatusOK
	}
	if code < 50000 {
		return consts.StatusBadRequest
	}
	return consts.StatusInternalServerError
}
