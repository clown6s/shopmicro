package proxy

import (
	"encoding/json"
	"net/url"
	"testing"

	dproto "github.com/cloudwego/dynamicgo/proto"
)

func TestMergeQueryToJSON_Basic(t *testing.T) {
	body := `{"name":"test","page":1}`
	params := url.Values{"page_size": []string{"20"}, "keyword": []string{"hello"}}

	result, err := mergeQueryToJSON(body, params)
	if err != nil {
		t.Fatalf("mergeQueryToJSON error: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal([]byte(result), &m); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	// body 中已有的字段不应被覆盖
	if m["name"] != "test" {
		t.Errorf("name = %v, want test", m["name"])
	}
	// page 在 body 中是 int，不应被覆盖
	if m["page"] != float64(1) {
		t.Errorf("page = %v, want 1", m["page"])
	}
	// query param 应被合并
	if m["page_size"] != "20" {
		t.Errorf("page_size = %v, want '20'", m["page_size"])
	}
	if m["keyword"] != "hello" {
		t.Errorf("keyword = %v", m["keyword"])
	}
}

func TestMergeQueryToJSON_BodyOverrides(t *testing.T) {
	body := `{"status":"active"}`
	params := url.Values{"status": []string{"inactive"}}

	result, err := mergeQueryToJSON(body, params)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	var m map[string]interface{}
	json.Unmarshal([]byte(result), &m)

	// body 字段优先
	if m["status"] != "active" {
		t.Errorf("status = %v, want active (body should override)", m["status"])
	}
}

func TestMergeQueryToJSON_EmptyBody(t *testing.T) {
	params := url.Values{"key": []string{"value"}}

	result, err := mergeQueryToJSON("{}", params)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	var m map[string]interface{}
	json.Unmarshal([]byte(result), &m)
	if m["key"] != "value" {
		t.Errorf("key = %v, want value", m["key"])
	}
}

func TestMergeQueryToJSON_NoParams(t *testing.T) {
	body := `{"foo":"bar"}`
	result, err := mergeQueryToJSON(body, url.Values{})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	var m map[string]interface{}
	json.Unmarshal([]byte(result), &m)
	if m["foo"] != "bar" {
		t.Errorf("body should be unchanged, got foo=%v", m["foo"])
	}
}

func TestMergeQueryToJSON_EmptyParamValues(t *testing.T) {
	body := `{}`
	params := url.Values{"empty": []string{}}

	result, err := mergeQueryToJSON(body, params)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	var m map[string]interface{}
	json.Unmarshal([]byte(result), &m)
	if _, ok := m["empty"]; ok {
		t.Error("empty param values should not be merged")
	}
}

func TestMergeQueryToJSONTyped_NilDescriptor(t *testing.T) {
	// nil svcDesc 应安全处理
	p := &Proxy{}
	params := url.Values{"adcode": []string{"330100"}}

	result, err := p.mergeQueryToJSONTyped(`{}`, params, nil, "GetChildren")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	var m map[string]interface{}
	json.Unmarshal([]byte(result), &m)
	// nil descriptor 时回退到 mergeQueryToJSON，adcode 保持 string
	if m["adcode"] != "330100" {
		t.Errorf("adcode = %v, want string '330100'", m["adcode"])
	}
}

func TestIsProtoIntType(t *testing.T) {
	intTypes := []dproto.Type{
		dproto.INT32, dproto.SINT32, dproto.UINT32, dproto.SFIX32, dproto.FIX32,
		dproto.INT64, dproto.SINT64, dproto.UINT64, dproto.SFIX64, dproto.FIX64,
	}
	for _, tt := range intTypes {
		if !isProtoIntType(tt) {
			t.Errorf("isProtoIntType(%v) = false, want true", tt)
		}
	}
	nonIntTypes := []dproto.Type{dproto.FLOAT, dproto.DOUBLE, dproto.BOOL, dproto.STRING, dproto.ENUM}
	for _, tt := range nonIntTypes {
		if isProtoIntType(tt) {
			t.Errorf("isProtoIntType(%v) = true, want false", tt)
		}
	}
}

func TestIsProtoFloatType(t *testing.T) {
	if !isProtoFloatType(dproto.FLOAT) {
		t.Error("isProtoFloatType(FLOAT) = false, want true")
	}
	if !isProtoFloatType(dproto.DOUBLE) {
		t.Error("isProtoFloatType(DOUBLE) = false, want true")
	}
	if isProtoFloatType(dproto.INT32) {
		t.Error("isProtoFloatType(INT32) = true, want false")
	}
	if isProtoFloatType(dproto.STRING) {
		t.Error("isProtoFloatType(STRING) = true, want false")
	}
}

func TestToMethodName_EdgeCases(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"login", "Login"},
		{"Login", "Login"},
		{"registerUser", "RegisterUser"},
		{"A", "A"},
		{"Z", "Z"},
		{"", ""},
		{"123abc", ""},      // 数字开头
		{"_hidden", ""},     // 下划线开头
		{"list-orders", ""}, // 连字符
		{"list orders", ""}, // 空格
		{"getUserById", "GetUserById"},
	}
	for _, tt := range tests {
		got := toMethodName(tt.input)
		if got != tt.want {
			t.Errorf("toMethodName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBearerToken_TooLong(t *testing.T) {
	// 构造一个超长但格式正确的 token
	longToken := make([]byte, maxTokenLen+1)
	for i := range longToken {
		longToken[i] = 'a'
	}
	longToken[0] = 'e'
	longToken[1] = '.'
	longToken[2] = 'y'
	longToken[3] = '.'
	longToken[4] = 'J'

	_, ok := bearerToken("Bearer " + string(longToken))
	if ok {
		t.Fatal("token exceeding maxTokenLen should be rejected")
	}
}

func TestNew_NoOptions(t *testing.T) {
	p := New()
	if p == nil {
		t.Fatal("New() returned nil")
	}
	if p.cb != nil {
		t.Error("cb should be nil by default")
	}
	if p.jwtParser != nil {
		t.Error("jwtParser should be nil by default")
	}
	if p.rdb != nil {
		t.Error("rdb should be nil by default")
	}
	if len(p.publicMethod) != 0 {
		t.Error("publicMethod should be empty")
	}
}

func TestNew_WithPublicMethods(t *testing.T) {
	p := New(WithPublicMethods("user", "Login", "Register"))
	if !p.publicMethod["user/Login"] {
		t.Error("user/Login should be public")
	}
	if !p.publicMethod["user/Register"] {
		t.Error("user/Register should be public")
	}
	if p.publicMethod["user/Logout"] {
		t.Error("user/Logout should not be public")
	}
}

func TestNew_WithAllowedMethods(t *testing.T) {
	p := New(WithAllowedMethods("user", "Login", "RegisterUser"))
	allowed, ok := p.allowedMethods["user"]
	if !ok {
		t.Fatal("user should be in allowedMethods")
	}
	if !allowed["Login"] || !allowed["RegisterUser"] {
		t.Error("Login and RegisterUser should be allowed")
	}
	if allowed["Logout"] {
		t.Error("Logout should not be allowed")
	}
}

func TestNew_WithHTTPMethod(t *testing.T) {
	p := New(WithHTTPMethod("user", "Login", "POST"))
	methods, ok := p.httpMethodMap["user/Login"]
	if !ok {
		t.Fatal("user/Login should be in httpMethodMap")
	}
	if !methods["POST"] {
		t.Error("POST should be allowed")
	}
	if methods["GET"] {
		t.Error("GET should not be allowed")
	}
}

func TestNew_WithMethodTimeout(t *testing.T) {
	p := New(WithMethodTimeout("district", "UpdateDistrict", 120))
	timeout, ok := p.methodTimeout["district/UpdateDistrict"]
	if !ok {
		t.Fatal("district/UpdateDistrict should be in methodTimeout")
	}
	if timeout != 120 {
		t.Errorf("timeout = %v, want 120", timeout)
	}
}

func TestNew_WithCacheableMethods(t *testing.T) {
	p := New(WithCacheableMethods("district", "GetProvinces", "GetChildren"))
	if !p.cacheableMethods["district/GetProvinces"] {
		t.Error("district/GetProvinces should be cacheable")
	}
	if !p.cacheableMethods["district/GetChildren"] {
		t.Error("district/GetChildren should be cacheable")
	}
	if p.cacheableMethods["user/Login"] {
		t.Error("user/Login should not be cacheable")
	}
}

func TestGetClient_Empty(t *testing.T) {
	p := New()
	_, ok := p.getClient("nonexistent")
	if ok {
		t.Fatal("getClient should return false for nonexistent service")
	}
}
