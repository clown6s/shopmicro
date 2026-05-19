package errcode

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// BizError 业务错误，通过 gRPC 传递给 gateway。
// 包含 HTTPStatus 字段，供 gateway 直接使用精确的 HTTP 状态码，
// 避免仅依赖错误码范围的粗粒度映射。
type BizError struct {
	Code       uint32
	Message    string
	HTTPStatus int // 推荐的 HTTP 状态码；0 表示由 gateway 按错误码范围推断。
}

func NewBizError(code uint32, msg string) *BizError {
	return &BizError{Code: code, Message: msg}
}

// BizErrorHTTPStatus 创建附带 HTTP 状态码的 BizError。
// 调用方应优先使用此构造器，使 gateway 能直接映射到精确的 HTTP 状态码。
func BizErrorHTTPStatus(code uint32, msg string, httpStatus int) *BizError {
	return &BizError{Code: code, Message: msg, HTTPStatus: httpStatus}
}

// bizPrefix 是 Error() 的序列化前缀，用于 Kitex gRPC 跨进程传输时标识 BizError。
// 这是框架层面的传输协议，不可去除 —— Kitex 会把 error.Error() 序列化为字符串，
// gateway 端需要通过此前缀识别并还原 BizError。
const bizPrefix = "BIZ/"

func (e *BizError) Error() string {
	return fmt.Sprintf("%s%d: %s", bizPrefix, e.Code, e.Message)
}

// ParseBizError 从 error 中提取 BizError。
//
// 策略：
//  1. errors.As —— 进程内 wrapped BizError（类型安全，零误判）
//  2. 字符串回退  —— Kitex 远程传输后 error 被序列化为普通 string，
//     errors.As 无法穿透，只能通过 BIZ/ 前缀识别（必要的兼容层）。
func ParseBizError(err error) (*BizError, bool) {
	if err == nil {
		return nil, false
	}
	var biz *BizError
	if errors.As(err, &biz) {
		return biz, true
	}
	return parseBizErrorFromString(err.Error())
}

// parseBizErrorFromString 从错误消息字符串中解析 BizError。
// 仅用于 Kitex 远程传输兼容 —— 不应在进程内使用。
//
// 匹配规则：
//   - "BIZ/{code}: {message}" —— 精确前缀匹配
//   - "... BIZ/{code}: {message}" —— Kitex 包裹格式子串匹配
func parseBizErrorFromString(msg string) (*BizError, bool) {
	if idx := strings.Index(msg, bizPrefix); idx >= 0 {
		content := msg[idx+len(bizPrefix):]
		colon := strings.Index(content, ":")
		if colon == -1 {
			return nil, false
		}
		code, err := strconv.ParseUint(content[:colon], 10, 32)
		if err != nil {
			return nil, false
		}
		message := strings.TrimSpace(content[colon+1:])
		return &BizError{Code: uint32(code), Message: message}, true
	}
	return nil, false
}
