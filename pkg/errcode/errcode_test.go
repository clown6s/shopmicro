package errcode

import (
	"errors"
	"fmt"
	"testing"
)

func TestNewBizError(t *testing.T) {
	err := NewBizError(CodeUserPhoneRequired, "phone is required")
	if err == nil {
		t.Fatal("expected non-nil BizError")
	}
	if err.Code != CodeUserPhoneRequired {
		t.Fatalf("expected code %d, got %d", CodeUserPhoneRequired, err.Code)
	}
	if err.Message != "phone is required" {
		t.Fatalf("expected msg 'phone is required', got %q", err.Message)
	}
}

func TestBizErrorHTTPStatus(t *testing.T) {
	err := BizErrorHTTPStatus(CodeUserUnauthorized, "unauthorized", 401)
	if err.HTTPStatus != 401 {
		t.Fatalf("expected HTTPStatus 401, got %d", err.HTTPStatus)
	}
	if err.Code != CodeUserUnauthorized {
		t.Fatalf("expected code %d, got %d", CodeUserUnauthorized, err.Code)
	}
}

func TestBizError_Error(t *testing.T) {
	err := NewBizError(10001, "test")
	s := err.Error()
	if s != "BIZ/10001: test" {
		t.Fatalf("expected 'BIZ/10001: test', got %q", s)
	}
}

func TestParseBizError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantOK   bool
		wantCode uint32
		wantMsg  string
	}{
		{
			name:     "valid BizError",
			err:      NewBizError(10001, "phone is required"),
			wantOK:   true,
			wantCode: 10001,
			wantMsg:  "phone is required",
		},
		{
			name:   "nil error",
			err:    nil,
			wantOK: false,
		},
		{
			name:   "non-BizError",
			err:    errors.New("some other error"),
			wantOK: false,
		},
		{
			name:     "zero code",
			err:      NewBizError(0, "ok"),
			wantOK:   true,
			wantCode: 0,
			wantMsg:  "ok",
		},
		{
			name:     "Kitex wrapped format",
			err:      errors.New("remote or network error[remote]: biz error: BIZ/10006: phone or password incorrect"),
			wantOK:   true,
			wantCode: 10006,
			wantMsg:  "phone or password incorrect",
		},
		{
			name:   "BIZ/ in middle not at start (false positive check)",
			err:    errors.New("category: BUSINESS/Sales/100 - something"),
			wantOK: false,
		},
		{
			name:     "wrapped BizError via fmt.Errorf %w",
			err:      fmt.Errorf("context: %w", NewBizError(10009, "not found")),
			wantOK:   true,
			wantCode: 10009,
			wantMsg:  "not found",
		},
		{
			name:     "BizError with HTTPStatus preserved through errors.As",
			err:      BizErrorHTTPStatus(CodeUserUnauthorized, "token expired", 401),
			wantOK:   true,
			wantCode: CodeUserUnauthorized,
			wantMsg:  "token expired",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			biz, ok := ParseBizError(tt.err)
			if ok != tt.wantOK {
				t.Fatalf("expected ok=%v, got %v", tt.wantOK, ok)
			}
			if tt.wantOK {
				if biz.Code != tt.wantCode {
					t.Fatalf("expected code %d, got %d", tt.wantCode, biz.Code)
				}
				if biz.Message != tt.wantMsg {
					t.Fatalf("expected msg %q, got %q", tt.wantMsg, biz.Message)
				}
			}
		})
	}
}

func TestCodeRanges(t *testing.T) {
	// User codes: 10000-19999
	if CodeUserPhoneRequired < 10000 || CodeUserPhoneRequired >= 20000 {
		t.Fatal("user code out of range")
	}
	// Product codes: 20000-29999
	if CodeProductParam < 20000 || CodeProductParam >= 30000 {
		t.Fatal("product code out of range")
	}
	// District codes: 30000-39999
	if CodeDistrictParam < 30000 || CodeDistrictParam >= 40000 {
		t.Fatal("district code out of range")
	}
	// Gateway codes: 50000-59999
	if CodeGwParam < 50000 || CodeGwParam >= 60000 {
		t.Fatal("gateway code out of range")
	}
	// 验证所有 gateway 常量在 50000-59999 范围内
	codes := []uint32{CodeGwParam, CodeGwServer, CodeGwToken, CodeGwNotFound,
		CodeGwResponse, CodeGwRateLimit, CodeGwSrvUnavailable,
		CodeGwForbidden, CodeGwMethodNotAllowed, CodeGwUnsupportedMediaType,
		CodeGwPayloadTooLarge}
	for _, c := range codes {
		if c < 50000 || c >= 60000 {
			t.Fatalf("gateway code %d out of range", c)
		}
	}
}
