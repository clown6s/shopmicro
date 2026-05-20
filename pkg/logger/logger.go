package logger

import (
	"strings"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	// R6-P2-07: 使用 atomic.Pointer 替代裸指针，消除 C() 的数据竞争。
	// Init() 中通过 Store 设置，C() 通过 Load 读取，atomic 保证 happens-before。
	logPtr atomic.Pointer[zap.SugaredLogger]
	once   sync.Once

	// P1-09: 使用 atomic.Bool 替代裸 bool，消除 Init() 的数据竞争。
	// initialized 在 once.Do 内部设置，保证只写一次。
	initialized atomic.Bool

	// R6-P1-01: 使用 atomic.Pointer[error] 替代裸 error，
	// 消除 Init() 中 once.Do 之前的竞态读取。
	// atomic.Pointer.Store happens-before Load，保证并发安全。
	initErr atomic.Pointer[error]
)

// P3-11: 用于检测日志数据中敏感信息的模式。
// 由 Safe() 使用，用于对看起来像凭证的值进行脱敏处理。
var sensitivePatterns = []string{
	"password", "passwd", "pwd",
	"secret", "authorization",
	"cookie", "session",
	"credit_card", "cvv", "ssn",
	// R5-P3-01: 常见密钥字段关键词补充
	"api_key", "apikey", "access_key", "accesskey",
	"private_key", "privatekey", "signing_key",
	// DSN 包含数据库凭证 (user:password@tcp(...))
	"dsn",
	// P2-13: token 子串用具体场景替代，避免 "tokenizer"、"token_bucket" 等误匹配
	"refresh_token", "access_token", "csrf_token", "bearer_token",
	"jwt_token", "reset_token",
}

// Safe 返回输入字符串的脱敏版本，如果其键名（fieldName）
// 匹配已知敏感模式。在记录配置值时使用。
//
//	logger.Infof("config: %s=%s", "jwt_secret", logger.Safe("jwt_secret", secretValue))
//
// 如果 fieldName 为空，则原样返回 val（不脱敏）。
// P2-13: 使用单词边界匹配，防止 "token" 匹配 "tokenizer" 等非敏感字段。
func Safe(fieldName, val string) string {
	if fieldName == "" || val == "" {
		return val
	}
	lower := strings.ToLower(fieldName)
	for _, p := range sensitivePatterns {
		if wordMatch(lower, p) {
			return "***"
		}
	}
	return val
}

// wordMatch 检查 pattern 是否作为独立单词出现在 s 中。
// 单词边界定义为字符串首尾、下划线(_)、连字符(-)或点号(.)。
func wordMatch(s, pattern string) bool {
	for {
		idx := strings.Index(s, pattern)
		if idx < 0 {
			return false
		}
		before := idx == 0 || s[idx-1] == '_' || s[idx-1] == '-' || s[idx-1] == '.'
		after := idx+len(pattern) >= len(s) || s[idx+len(pattern)] == '_' || s[idx+len(pattern)] == '-' || s[idx+len(pattern)] == '.'
		if before && after {
			return true
		}
		s = s[idx+1:]
	}
}

func Init(mode string) error {
	if initialized.Load() {
		// R7-P2-01: 此时 logger 已成功初始化，直接用 C() 输出警告。
		// 之前写入 zap.NewNop() 导致警告被静默丢弃。
		C().Warnf("logger.Init called more than once (mode=%s), ignoring", mode)
		return nil
	}
	// R6-P1-01: 先通过 atomic.Pointer 读取首次初始化的错误。
	if errPtr := initErr.Load(); errPtr != nil {
		return *errPtr
	}
	var firstErr error
	once.Do(func() {
		var cfg zap.Config
		if strings.ToLower(mode) == "dev" {
			cfg = zap.NewDevelopmentConfig()
		} else {
			cfg = zap.NewProductionConfig()
		}
		cfg.EncoderConfig.TimeKey = "ts"
		cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		// P1-07: 为 Error 及更高级别添加堆栈跟踪
		log, err := cfg.Build(zap.AddCallerSkip(1), zap.AddStacktrace(zapcore.ErrorLevel))
		if err != nil {
			firstErr = err
			// R7-P2-02: 在 once.Do 内部原子存储错误，
			// 确保并发的 goroutine 在 once.Do 返回后能读到错误。
			initErr.Store(&firstErr)
			return
		}
		sugared := log.Sugar()
		logPtr.Store(sugared)
		initialized.Store(true)
	})
	if firstErr != nil {
		return firstErr
	}
	// R7-P2-02: once.Do 成功但 firstErr 为 nil 的并发 goroutine，
	// 检查是否有其他 goroutine 已在 once.Do 内存储了错误。
	if errPtr := initErr.Load(); errPtr != nil {
		return *errPtr
	}
	return nil
}

// R6-P2-07: 使用 atomic.Pointer.Load 替代裸指针读取，并发安全
func C() *zap.SugaredLogger {
	if l := logPtr.Load(); l != nil {
		return l
	}
	return zap.NewNop().Sugar()
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
