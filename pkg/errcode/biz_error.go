package errcode

import (
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
// R4-P1-02: 使用 HasPrefix 而非 Index，防止子串伪造
func ParseBizError(err error) (*BizError, bool) {
	if err == nil {
		return nil, false
	}
	msg := err.Error()
	// R4-P1-02: 只匹配消息起始位置的 BIZ/ 前缀，防止中间层错误消息包含
	// "BIZ/xxx: ..." 子串时被误解析为 BizError
	if !strings.HasPrefix(msg, bizPrefix) {
		return nil, false
	}
	content := msg[len(bizPrefix):] // "10006: phone or password incorrect"
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
