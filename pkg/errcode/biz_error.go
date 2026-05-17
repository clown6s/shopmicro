package errcode

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// BizError 业务错误，通过 gRPC 传递给 gateway
// Gateway 通过 ParseBizError 从错误消息字符串解析提取
type BizError struct {
	Code    uint32
	Message string
}

func NewBizError(code uint32, msg string) *BizError {
	return &BizError{Code: code, Message: msg}
}

// bizPrefix 用于 BizError.Error() 的前缀标记
// 使用特殊前缀避免与普通错误消息冲突
const bizPrefix = "BIZ/"

func (e *BizError) Error() string {
	return fmt.Sprintf("%s%d: %s", bizPrefix, e.Code, e.Message)
}

// ParseBizError 从 gRPC 错误消息中解析 BizError
// R4-P1-02: 使用 HasPrefix 优先匹配，防止子串伪造
// P3-05 修复：先用 errors.As 处理 wrapped BizError，再回退到字符串前缀解析
// Kitex 远程错误格式: "remote or network error[remote]: biz error: BIZ/xxx: ..."
// 需要同时处理精确前缀匹配和 Kitex 包装后的子串匹配。
func ParseBizError(err error) (*BizError, bool) {
	if err == nil {
		return nil, false
	}
	// 先尝试 errors.As 来处理被包装的 BizError（例如 fmt.Errorf("ctx: %w", bizErr)）
	var biz *BizError
	if errors.As(err, &biz) {
		return biz, true
	}
	msg := err.Error()
	// 优先匹配消息起始位置的 BIZ/ 前缀
	if idx := strings.Index(msg, bizPrefix); idx != -1 {
		content := msg[idx+len(bizPrefix):]
		colon := strings.Index(content, ":")
		if colon == -1 {
			return nil, false
		}
		code, err2 := strconv.ParseUint(content[:colon], 10, 32)
		if err2 != nil {
			return nil, false
		}
		message := strings.TrimSpace(content[colon+1:])
		return &BizError{Code: uint32(code), Message: message}, true
	}
	return nil, false
}
