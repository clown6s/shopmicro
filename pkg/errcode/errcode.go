package errcode

// ErrCode 网关 HTTP 响应错误码，仅 gateway 层使用
type ErrCode struct {
	Code int
	Msg  string
}

var (
	Success = ErrCode{0, "success"}

	// 基础设施错误 — gateway 自身产生的错误
	ErrParam          = ErrCode{int(CodeGwParam), "参数错误"}
	ErrServer         = ErrCode{int(CodeGwServer), "服务器内部错误"}
	ErrToken          = ErrCode{int(CodeGwToken), "token无效"}
	ErrNotFound       = ErrCode{int(CodeGwNotFound), "service not found"}
	ErrResponse       = ErrCode{int(CodeGwResponse), "invalid response"}
	ErrRateLimit      = ErrCode{int(CodeGwRateLimit), "too many requests"}
	ErrServiceUnavail = ErrCode{int(CodeGwSrvUnavailable), "service temporarily unavailable"}
)

// WithMsg 创建新错误码，保留 Code 替换 Msg
func (e ErrCode) WithMsg(msg string) ErrCode {
	return ErrCode{Code: e.Code, Msg: msg}
}

// FromBizError 将下游 BizError 转为网关 ErrCode，保留原始业务 code 和 message
func FromBizError(biz *BizError) ErrCode {
	if biz == nil {
		return Success
	}
	return ErrCode{Code: int(biz.Code), Msg: biz.Message}
}
