package response

import (
	"testing"

	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"shopmicro/pkg/errcode"
)

func TestHttpStatus(t *testing.T) {
	tests := []struct {
		name string
		code int
		want int
	}{
		{"zero code → 200", 0, consts.StatusOK},
		{"unauthorized → 401", int(errcode.CodeUserUnauthorized), consts.StatusUnauthorized},
		{"forbidden → 403", int(errcode.CodeUserForbidden), consts.StatusForbidden},
		{"user internal → 500", int(errcode.CodeUserInternal), consts.StatusInternalServerError},
		{"other biz error → 400", 10001, consts.StatusBadRequest},
		{"gateway server → 500", int(errcode.CodeGwServer), consts.StatusInternalServerError},
		{"gateway param → 500", int(errcode.CodeGwParam), consts.StatusInternalServerError},
		{"gateway token → 401", int(errcode.CodeGwToken), consts.StatusUnauthorized},
		{"gateway not found → 404", int(errcode.CodeGwNotFound), consts.StatusNotFound},
		{"gateway rate limit → 429", int(errcode.CodeGwRateLimit), consts.StatusTooManyRequests},
		{"gateway service unavailable → 503", int(errcode.CodeGwSrvUnavailable), consts.StatusServiceUnavailable},
		{"gateway forbidden → 403", int(errcode.CodeGwForbidden), consts.StatusForbidden},
		{"gateway method not allowed → 405", int(errcode.CodeGwMethodNotAllowed), consts.StatusMethodNotAllowed},
		{"gateway unsupported media type → 415", int(errcode.CodeGwUnsupportedMediaType), consts.StatusUnsupportedMediaType},
		{"gateway payload too large → 413", int(errcode.CodeGwPayloadTooLarge), consts.StatusRequestEntityTooLarge},
		{"boundary just below 50000 → 400", 49999, consts.StatusBadRequest},
		{"boundary at 50000 → 500", 50000, consts.StatusInternalServerError},
		{"large code → 500", 99999, consts.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HttpStatus(tt.code)
			if got != tt.want {
				t.Errorf("HttpStatus(%d) = %d, want %d", tt.code, got, tt.want)
			}
		})
	}
}
