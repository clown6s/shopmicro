package logger

import (
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	L    *zap.SugaredLogger
	once sync.Once
)

func Init(mode string) {
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
			panic(err)
		}
		L = log.Sugar()
	})
}

func C() *zap.SugaredLogger {
	if L == nil {
		Init("dev")
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
