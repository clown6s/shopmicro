package proxy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

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
