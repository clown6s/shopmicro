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
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return body, fmt.Errorf("parse body: %w", err)
	}
	if m == nil {
		m = make(map[string]interface{})
	}

	for key, values := range params {
		if len(values) == 0 {
			continue
		}
		if _, exists := m[key]; exists {
			continue
		}
		m[key] = values[0]
	}

	out, err := json.Marshal(m)
	if err != nil {
		return body, fmt.Errorf("marshal merged: %w", err)
	}
	return string(out), nil
}

// mergeQueryToJSONTyped 使用 proto 服务描述符做类型感知的 query→JSON 合并。
// 根据 proto 字段类型决定是否将 query value 转为数字/布尔。
func (p *Proxy) mergeQueryToJSONTyped(body string, params url.Values, svcDesc *dproto.ServiceDescriptor, method string) (string, error) {
	mtdDesc := svcDesc.LookupMethodByName(method)
	if mtdDesc == nil {
		return mergeQueryToJSON(body, params)
	}
	inputMsg := mtdDesc.Input().Message()
	if inputMsg == nil {
		return mergeQueryToJSON(body, params)
	}

	var m map[string]interface{}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return body, fmt.Errorf("parse body: %w", err)
	}
	if m == nil {
		m = make(map[string]interface{})
	}

	for key, values := range params {
		if len(values) == 0 {
			continue
		}
		if _, exists := m[key]; exists {
			continue
		}
		val := values[0]

		field := inputMsg.ByJSONName(key)
		if field == nil {
			field = inputMsg.ByName(key)
		}

		if field == nil {
			m[key] = val
			continue
		}

		ft := field.Type().Type()
		switch {
		case isProtoIntType(ft):
			if intVal, err := strconv.ParseInt(val, 10, 64); err == nil {
				m[key] = intVal
			} else {
				m[key] = val
			}
		case isProtoFloatType(ft):
			if floatVal, err := strconv.ParseFloat(val, 64); err == nil {
				m[key] = floatVal
			} else {
				m[key] = val
			}
		case ft == dproto.BOOL:
			if boolVal, err := strconv.ParseBool(val); err == nil {
				m[key] = boolVal
			} else {
				m[key] = val
			}
		default:
			m[key] = val
		}
	}

	out, err := json.Marshal(m)
	if err != nil {
		return body, fmt.Errorf("marshal merged: %w", err)
	}
	return string(out), nil
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
