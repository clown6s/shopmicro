package errcode

import "net/http"

type ErrCode struct {
	Code int
	Msg  string
}

var (
	Success         = ErrCode{0, "success"}
	ErrParam        = ErrCode{10001, "参数错误"}
	ErrDB           = ErrCode{10002, "数据库错误"}
	ErrUserExist    = ErrCode{10003, "用户已存在"}
	ErrUserNotFound = ErrCode{10004, "用户不存在"}
	ErrServer       = ErrCode{10005, "服务器内部错误"}
	ErrToken        = ErrCode{10006, "token无效"}
)

func (e ErrCode) HttpCode() int {
	if e.Code == 0 {
		return http.StatusOK
	}
	return http.StatusInternalServerError
}

func (e ErrCode) Error() string {
	return e.Msg
}

// WithMsg 创建新错误码，保留 Code 替换 Msg
func (e ErrCode) WithMsg(msg string) ErrCode {
	return ErrCode{Code: e.Code, Msg: msg}
}
