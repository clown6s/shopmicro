package proxy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	dproto "github.com/cloudwego/dynamicgo/proto"
)

// mergeQueryToJSON 将 URL query 参数合并到 JSON body 中。
// query 参数保持 string 类型，不做 int/bool 自动转换。
func mergeQueryToJSON(body string, params url.Values) (string, error) {
	m, err := parseBodyToMap(body)
	if err != nil {
		return body, err
	}

	for key, values := range params {
		if len(values) == 0 || mapContains(m, key) {
			continue
		}
		m[key] = values[0]
	}

	return marshalMap(m, body)
}

// mergeQueryToJSONTyped 使用 proto 服务描述符做类型感知的 query→JSON 合并。
// 根据 proto 字段类型决定是否将 query value 转为数字/布尔。
// 无描述符时回退到 mergeQueryToJSON（纯 string 合并）。
func (p *Proxy) mergeQueryToJSONTyped(body string, params url.Values, svcDesc *dproto.ServiceDescriptor, method string) (string, error) {
	inputMsg := resolveInputMessage(svcDesc, method)
	if inputMsg == nil {
		return mergeQueryToJSON(body, params)
	}

	m, err := parseBodyToMap(body)
	if err != nil {
		return body, err
	}

	for key, values := range params {
		if len(values) == 0 || mapContains(m, key) {
			continue
		}
		m[key] = convertValueByProtoType(values[0], inputMsg, key)
	}

	return marshalMap(m, body)
}

// parseBodyToMap 将 JSON body 解析为 map。空/null body 返回空 map。
func parseBodyToMap(body string) (map[string]interface{}, error) {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return nil, fmt.Errorf("parse body: %w", err)
	}
	if m == nil {
		m = make(map[string]interface{})
	}
	return m, nil
}

// marshalMap 将 map 序列化为 JSON 字符串。
// 失败时返回原始 fallback 值。
func marshalMap(m map[string]interface{}, fallback string) (string, error) {
	out, err := json.Marshal(m)
	if err != nil {
		return fallback, fmt.Errorf("marshal merged: %w", err)
	}
	return string(out), nil
}

// resolveInputMessage 从 proto 服务描述符中解析请求消息类型。
// 任何一级为 nil 均返回 nil，调用方应回退到非类型感知合并。
func resolveInputMessage(svcDesc *dproto.ServiceDescriptor, method string) *dproto.MessageDescriptor {
	if svcDesc == nil {
		return nil
	}
	mtdDesc := svcDesc.LookupMethodByName(method)
	if mtdDesc == nil {
		return nil
	}
	return mtdDesc.Input().Message()
}

// convertValueByProtoType 根据 proto 字段类型将 string value 转为对应 Go 类型。
// 未找到字段定义或类型不匹配时保持原始 string。
func convertValueByProtoType(val string, inputMsg *dproto.MessageDescriptor, key string) interface{} {
	field := inputMsg.ByJSONName(key)
	if field == nil {
		field = inputMsg.ByName(key)
	}
	if field == nil {
		return val
	}

	ft := field.Type().Type()
	switch {
	case isProtoIntType(ft):
		if intVal, err := strconv.ParseInt(val, 10, 64); err == nil {
			return intVal
		}
	case isProtoFloatType(ft):
		if floatVal, err := strconv.ParseFloat(val, 64); err == nil {
			return floatVal
		}
	case ft == dproto.BOOL:
		if boolVal, err := strconv.ParseBool(val); err == nil {
			return boolVal
		}
	}
	return val
}

// mapContains 检查 map 是否包含指定 key。
func mapContains(m map[string]interface{}, key string) bool {
	_, ok := m[key]
	return ok
}

// isProtoIntType 判断字段类型是否为整数类型。
func isProtoIntType(t dproto.Type) bool {
	switch t {
	case dproto.INT32, dproto.SINT32, dproto.UINT32, dproto.SFIX32, dproto.FIX32,
		dproto.INT64, dproto.SINT64, dproto.UINT64, dproto.SFIX64, dproto.FIX64:
		return true
	}
	return false
}

// isProtoFloatType 判断字段类型是否为浮点类型。
func isProtoFloatType(t dproto.Type) bool {
	return t == dproto.FLOAT || t == dproto.DOUBLE
}

// validateBodyAgainstProto 使用 proto 服务描述符预校验 JSON body 的字段类型。
// 在泛化 RPC 调用之前拦截类型不匹配的错误，避免 Kitex dynamicgo 序列化失败。
//
// 校验规则：
//   - proto int 字段：JSON 值必须是数字（float64）或可解析为数字的字符串
//   - proto float 字段：同上
//   - proto bool 字段：JSON 值必须是 bool 或可解析为 bool 的字符串
//   - proto string/bytes 字段：JSON 值必须是 string
//   - proto enum 字段：JSON 值必须是数字或字符串
//   - proto message/repeated/map 字段：JSON 值必须是 object/array/map
//   - 未在 proto 中定义的字段：跳过（dynamicgo 会忽略未知字段）
//
// 返回 nil 表示校验通过，返回 *FieldTypeError 表示首个类型不匹配的字段。
func validateBodyAgainstProto(body string, svcDesc *dproto.ServiceDescriptor, method string) error {
	inputMsg := resolveInputMessage(svcDesc, method)
	if inputMsg == nil {
		return nil // 无描述符时跳过校验，走正常 RPC 调用
	}

	var m map[string]interface{}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return &FieldTypeError{Field: "(body)", Err: err}
	}

	for jsonKey, val := range m {
		if val == nil {
			continue // null 值交给 proto 处理（optional 字段）
		}

		// 查找 proto 字段定义（先按 JSON name，再按 proto name）
		field := inputMsg.ByJSONName(jsonKey)
		if field == nil {
			field = inputMsg.ByName(jsonKey)
		}
		if field == nil {
			continue // 未知字段，dynamicgo 会忽略
		}

		if err := checkFieldType(val, field); err != nil {
			return &FieldTypeError{
				Field: jsonKey,
				Proto: field.Type().Type().String(),
				Err:   err,
			}
		}
	}

	return nil
}

// checkFieldType 检查单个 JSON 值是否与 proto 字段类型兼容。
func checkFieldType(val interface{}, field *dproto.FieldDescriptor) error {
	ft := field.Type().Type()
	isRepeated := field.Type().IsList()

	if isRepeated {
		if _, ok := val.([]interface{}); !ok {
			return fmt.Errorf("expected array for repeated field, got %T", val)
		}
		return nil
	}

	switch ft {
	case dproto.INT32, dproto.SINT32, dproto.UINT32, dproto.SFIX32, dproto.FIX32,
		dproto.INT64, dproto.SINT64, dproto.UINT64, dproto.SFIX64, dproto.FIX64:
		if _, ok := val.(float64); ok {
			return nil
		}
		if s, ok := val.(string); ok {
			if _, err := strconv.ParseInt(s, 10, 64); err == nil {
				return nil
			}
		}
		return fmt.Errorf("expected int, got %T", val)

	case dproto.FLOAT, dproto.DOUBLE:
		if _, ok := val.(float64); ok {
			return nil
		}
		if s, ok := val.(string); ok {
			if _, err := strconv.ParseFloat(s, 64); err == nil {
				return nil
			}
		}
		return fmt.Errorf("expected number, got %T", val)

	case dproto.BOOL:
		if _, ok := val.(bool); ok {
			return nil
		}
		if s, ok := val.(string); ok {
			if _, err := strconv.ParseBool(s); err == nil {
				return nil
			}
		}
		return fmt.Errorf("expected bool, got %T", val)

	case dproto.STRING, dproto.BYTE:
		if _, ok := val.(string); ok {
			return nil
		}
		return fmt.Errorf("expected string, got %T", val)

	case dproto.ENUM:
		// proto3 enum 兼容数字和字符串
		if _, ok := val.(float64); ok {
			return nil
		}
		if _, ok := val.(string); ok {
			return nil
		}
		return fmt.Errorf("expected enum (string or number), got %T", val)

	case dproto.MESSAGE:
		if _, ok := val.(map[string]interface{}); ok {
			return nil
		}
		return fmt.Errorf("expected object, got %T", val)

	default:
		// 未知/复合类型跳过校验
		return nil
	}
}

// FieldTypeError 表示请求体中某个字段的类型与 proto 定义不匹配。
type FieldTypeError struct {
	Field string // JSON 字段名
	Proto string // proto 类型名
	Err   error  // 底层错误
}

func (e *FieldTypeError) Error() string {
	if e.Proto != "" {
		return fmt.Sprintf("field %q: proto type %s, %v", e.Field, e.Proto, e.Err)
	}
	return fmt.Sprintf("field %q: %v", e.Field, e.Err)
}

// isSerializationError 判断 Kitex 泛化调用返回的 error 是否为 JSON→protobuf
// 序列化失败。这是 fallback 层，正常流程应被 validateBodyAgainstProto() 拦截。
//
// 使用多关键词组合匹配（json + proto/marshal/unmarshal + failed/cannot/invalid），
// 降低单一字符串误判风险，同时兼容 dynamicgo 版本升级后的消息格式变化。
func isSerializationError(err error) bool {
	msg := err.Error()
	hasJSON := strings.Contains(msg, "json")
	hasProtoOrMarshal := strings.Contains(msg, "protobuf") ||
		strings.Contains(msg, "marshal") ||
		strings.Contains(msg, "unmarshal") ||
		strings.Contains(msg, "proto")
	hasFailed := strings.Contains(msg, "failed") ||
		strings.Contains(msg, "cannot") ||
		strings.Contains(msg, "invalid")
	return hasJSON && hasProtoOrMarshal && hasFailed
}
