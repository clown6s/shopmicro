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
// 直接在错误消息中查找 BIZ/ 标记，不依赖 Kitex 包装格式
func ParseBizError(err error) (*BizError, bool) {
	if err == nil {
		return nil, false
	}
	msg := err.Error()
	idx := strings.Index(msg, bizPrefix)
	if idx == -1 {
		return nil, false
	}
	content := msg[idx+len(bizPrefix):] // "10006: phone or password incorrect"
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
