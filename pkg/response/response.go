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
	httpCode := consts.StatusOK
	if code.Code != 0 {
		httpCode = consts.StatusInternalServerError
	}
	c.JSON(httpCode, R{Code: code.Code, Msg: code.Msg, Data: data})
}
