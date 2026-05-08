package response

import (
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"shopmicro/pkg/errcode"
)

type R struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data,omitempty"`
}

func JSON(c *app.RequestContext, code errcode.ErrCode, data interface{}) {
	c.JSON(httpStatus(code.Code), R{Code: code.Code, Msg: code.Msg, Data: data})
}

// httpStatus 根据业务错误码返回合适的 HTTP 状态码
// 0          → 200 OK
// 10000-49999 → 400 Bad Request（业务错误）
// 50000+     → 500 Internal Server Error（基础设施错误）
func httpStatus(code int) int {
	if code == 0 {
		return consts.StatusOK
	}
	if code < 50000 {
		return consts.StatusBadRequest
	}
	return consts.StatusInternalServerError
}
