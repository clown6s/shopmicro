package data

import (
	"testing"
)

func TestEscapeLike(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "normal", want: "normal"},
		{input: "%", want: "\\%"},
		{input: "_", want: "\\_"},
		{input: `\`, want: "\\\\"},
		{input: "hello%world", want: "hello\\%world"},
		{input: "100%", want: "100\\%"},
		{input: "admin_123", want: "admin\\_123"},
		{input: "", want: ""},
		{input: `\%`, want: "\\\\\\%"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeLike(tt.input)
			if got != tt.want {
				t.Errorf("escapeLike(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestUserUpdate_Fields(t *testing.T) {
	// 空更新
	u := &UserUpdate{}
	fields := u.Fields()
	if len(fields) != 0 {
		t.Errorf("empty UserUpdate should have 0 fields, got %d", len(fields))
	}

	// 多字段设置
	u.SetRealName("张三").SetAge(25).SetGender(1).SetEmail("test@test.com")
	fields = u.Fields()
	if len(fields) != 4 {
		t.Errorf("expected 4 fields, got %d: %v", len(fields), fields)
	}
	if fields["real_name"] != "张三" {
		t.Errorf("real_name = %v, want 张三", fields["real_name"])
	}
	if fields["age"] != int16(25) {
		t.Errorf("age = %v, want 25", fields["age"])
	}
	if fields["gender"] != int8(1) {
		t.Errorf("gender = %v, want 1", fields["gender"])
	}
	if fields["email"] != "test@test.com" {
		t.Errorf("email = %v, want test@test.com", fields["email"])
	}

	// SetRealName 空值不应设置
	u2 := &UserUpdate{}
	u2.SetRealName("")
	if u2.RealName != nil {
		t.Error("SetRealName('') should not set RealName")
	}

	// SetAvatar 非空应该设置
	u3 := &UserUpdate{}
	u3.SetAvatar("https://example.com/avatar.jpg")
	if u3.Avatar == nil || *u3.Avatar != "https://example.com/avatar.jpg" {
		t.Error("SetAvatar should set Avatar")
	}

	// SetPhone
	u4 := &UserUpdate{}
	u4.SetPhone("13800138000")
	if u4.Phone == nil || *u4.Phone != "13800138000" {
		t.Error("SetPhone should set Phone")
	}

	// SetRemark
	u5 := &UserUpdate{}
	u5.SetRemark("测试备注")
	if u5.Remark == nil || *u5.Remark != "测试备注" {
		t.Error("SetRemark should set Remark")
	}
}
