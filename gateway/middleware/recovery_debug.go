//go:build !prod

package middleware

import "runtime/debug"

func init() {
	stackTracer = func() string { return string(debug.Stack()) }
}
