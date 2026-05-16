package logger

import (
	"strings"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	L    *zap.SugaredLogger
	once sync.Once
)

// P3-11: Sensitive key patterns to detect in logged data.
// Used by Safe() to redact values that look like credentials.
var sensitivePatterns = []string{
	"password", "passwd", "pwd",
	"secret", "token", "authorization",
	"cookie", "session",
	"credit_card", "cvv", "ssn",
	// R5-P3-01: 常见密钥字段关键词补充
	"api_key", "apikey", "access_key", "accesskey",
	"private_key", "privatekey", "signing_key",
}

// Safe returns a redacted version of the input string if its key (fieldName)
// matches a known sensitive pattern. Use when logging configuration values.
//
//	logger.Infof("config: %s=%s", "jwt_secret", logger.Safe("jwt_secret", secretValue))
//
// If fieldName is empty, returns val unchanged (no redaction).
func Safe(fieldName, val string) string {
	if fieldName == "" || val == "" {
		return val
	}
	lower := strings.ToLower(fieldName)
	for _, p := range sensitivePatterns {
		if strings.Contains(lower, p) {
			return "***"
		}
	}
	return val
}

func Init(mode string) error {
	var initErr error
	once.Do(func() {
		var cfg zap.Config
		if mode == "dev" {
			cfg = zap.NewDevelopmentConfig()
		} else {
			cfg = zap.NewProductionConfig()
		}
		cfg.EncoderConfig.TimeKey = "ts"
		cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		// P1-07: Add stack trace for Error and higher levels
		log, err := cfg.Build(zap.AddCallerSkip(1), zap.AddStacktrace(zapcore.ErrorLevel))
		if err != nil {
			initErr = err
			return
		}
		L = log.Sugar()
	})
	return initErr
}

func C() *zap.SugaredLogger {
	if L == nil {
		return zap.NewNop().Sugar()
	}
	return L
}

func Info(args ...interface{})                    { C().Info(args...) }
func Infof(template string, args ...interface{})  { C().Infof(template, args...) }
func Error(args ...interface{})                   { C().Error(args...) }
func Errorf(template string, args ...interface{}) { C().Errorf(template, args...) }
func Fatal(args ...interface{})                   { C().Fatal(args...) }
func Fatalf(template string, args ...interface{}) { C().Fatalf(template, args...) }
func Debug(args ...interface{})                   { C().Debug(args...) }
func Debugf(template string, args ...interface{}) { C().Debugf(template, args...) }
func Warn(args ...interface{})                    { C().Warn(args...) }
func Warnf(template string, args ...interface{})  { C().Warnf(template, args...) }
func Sync()                                       { _ = C().Sync() }
