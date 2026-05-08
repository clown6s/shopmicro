package logger

import (
	"fmt"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	L    *zap.SugaredLogger
	once sync.Once
)

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
		log, err := cfg.Build(zap.AddCallerSkip(1))
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
		_ = Init("dev")
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

// Fatalf2 替代 Fatalf，返回 error 而不是调用 os.Exit
func Fatalf2(format string, args ...interface{}) error {
	return fmt.Errorf(format, args...)
}
