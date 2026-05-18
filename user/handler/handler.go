package handler

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/go-sql-driver/mysql"
	goredis "github.com/redis/go-redis/v9"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/jwt"
	"shopmicro/pkg/logger"
	"shopmicro/user/data"
)

func newBiz(code uint32, msg string) *errcode.BizError { return errcode.NewBizError(code, msg) }

var (
	errPhoneRequired    = newBiz(errcode.CodeUserPhoneRequired, "phone is required")
	errPasswordRequired = newBiz(errcode.CodeUserPwdRequired, "password is required")
	errPhoneFormat      = newBiz(errcode.CodeUserPhoneFormat, "invalid phone format")
	errPasswordWeak     = newBiz(errcode.CodeUserPwdWeak, "password must be 8-128 characters with at least one letter and one digit")
	errPasswordTooLong  = newBiz(errcode.CodeUserPwdTooLong, "password must not exceed 128 characters")
	errPhoneExists      = newBiz(errcode.CodeUserPhoneExists, "phone already exists")
	errInternal         = newBiz(errcode.CodeUserInternal, "internal error")
	errPhonePwdWrong    = newBiz(errcode.CodeUserPhonePwdWrong, "phone or password incorrect")
	errInvalidUserID    = newBiz(errcode.CodeUserInvalidID, "invalid user id")
	errNothingUpdate    = newBiz(errcode.CodeUserNothingUpdate, "nothing to update")
	errUserNotFound     = newBiz(errcode.CodeUserNotFound, "user not found")
	errUnauthorized     = newBiz(errcode.CodeUserUnauthorized, "unauthorized: missing user identity")
	errForbidden        = newBiz(errcode.CodeUserForbidden, "forbidden: cannot operate other user's data")
	errEmailFormat      = newBiz(errcode.CodeUserEmailFormat, "invalid email format")
	errAccountLocked    = newBiz(errcode.CodeUserAccountLocked, "account temporarily locked due to too many failed login attempts")
	errRegisterTooMany  = newBiz(errcode.CodeUserParam, "too many registration attempts, please try again later")
	errRefreshTooMany   = newBiz(errcode.CodeUserParam, "too many refresh attempts, please try again later")
)

// P1-21：手机号正则 — 中国手机号格式
var phoneRegex = regexp.MustCompile(`^1[3-9]\d{9}$`)

// emailRegex 校验基本邮箱格式（RFC 5322 简化版）。
// 注意：不支持国际化域名 (IDN)，如需支持应在入库前转换为 Punycode。
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// passwordLetterRe 和 passwordDigitRe 拆分为两个检查，因为 Go 的 RE2 引擎
// 不支持前瞻断言 ((?=...))。使用 (?=.*[a-zA-Z])(?=.*\d) 的单一正则方式
// 在 Go 的 regexp 包中不可用。
var passwordLetterRe = regexp.MustCompile(`[a-zA-Z]`)
var passwordDigitRe = regexp.MustCompile(`\d`)

// passwordValid 检查密码长度为 8-128 个字符，且至少包含一个字母和一个数字。
func passwordValid(pwd string) bool {
	return len(pwd) >= 8 && len(pwd) <= 128 &&
		passwordLetterRe.MatchString(pwd) &&
		passwordDigitRe.MatchString(pwd)
}

// UserServiceImpl 实现用户服务的 gRPC handler。
type UserServiceImpl struct {
	userDal   UserStore
	jwtGen    *jwt.Generator
	jwtExpHrs int
	rtGen     *jwt.Generator // 刷新令牌生成器
	rtExpHrs  int            // 刷新令牌过期时间（小时）
	rdb       RedisOps
}

func NewUserHandler(d *data.Data, jwtSecret string, jwtExpHours int, jwtIssuer string, jwtRefreshSecret string) (*UserServiceImpl, error) {
	// P2-16 修复：防止 jwtExpHours 为零导致 rtExpHrs 异常
	if jwtExpHours <= 0 {
		jwtExpHours = 24
	}
	gen, err := jwt.NewGenerator(jwtSecret, time.Duration(jwtExpHours)*time.Hour, jwtIssuer)
	if err != nil {
		return nil, fmt.Errorf("init jwt generator failed: %w", err)
	}
	// 刷新令牌：默认 7 天（必须大于等于访问令牌）
	rtExpHrs := jwtExpHours * 7
	if rtExpHrs < 24 {
		rtExpHrs = 168 // 至少 7 天
	}

	// 使用相同密钥会破坏双令牌架构的设计目的。
	if jwtRefreshSecret == "" {
		return nil, fmt.Errorf("JWT_REFRESH_SECRET is required")
	}
	if jwtRefreshSecret == jwtSecret {
		return nil, fmt.Errorf("JWT_REFRESH_SECRET must differ from JWT_SECRET — using the same secret defeats the dual-token architecture")
	}
	rtGen, err := jwt.NewGenerator(jwtRefreshSecret, time.Duration(rtExpHrs)*time.Hour, jwtIssuer)
	if err != nil {
		return nil, fmt.Errorf("init refresh token generator failed: %w", err)
	}
	return &UserServiceImpl{
		userDal:   d.User(),
		jwtGen:    gen,
		jwtExpHrs: jwtExpHours,
		rtGen:     rtGen,
		rtExpHrs:  rtExpHrs,
		rdb:       d.Rdb,
	}, nil
}

const tokenPrefix = "shopmicro:user:token:"
const refreshTokenPrefix = "shopmicro:user:refresh:"

// P2-06/P2-07：共享的频率限制 Lua 脚本（原子性 INCR + 条件 EXPIRE）
const rateLimitScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 then
    redis.call('EXPIRE', KEYS[1], ARGV[2])
end
if tonumber(count) > tonumber(ARGV[1]) then
    return 1
end
return 0
`

// P2-06：注册频率限制 — 按手机号，每小时最多 5 次
const registerRateLimitKey = "shopmicro:register:rate:"
const registerRateLimitMax = 5
const registerRateLimitWindowSec = 3600 // 1 小时

// P2-07：RefreshToken 频率限制 — 按用户 ID，每分钟最多 10 次
const refreshRateLimitKey = "shopmicro:refresh:rate:"
const refreshRateLimitMax = 10
const refreshRateLimitWindowSec = 60

// P1-05：账号级别暴力破解保护常量
// R4-P1-03：原子性 INCR + 条件 EXPIRE 的 Lua 脚本。
const loginFailIncrScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 then
    redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return count
`

const (
	loginMaxFailures = 10               // 锁定前最大失败尝试次数
	loginLockoutTTL  = 15 * time.Minute // 锁定持续时间
)

// R2-P2-04：用于撤销用户所有令牌（访问令牌 + 刷新令牌）的原子性 Lua 脚本。
const revokeTokensScript = `
local tokenKey = KEYS[1]
local rtSetKey = KEYS[2]
local rtTokens = redis.call('SMEMBERS', rtSetKey)
for _, rt in ipairs(rtTokens) do
    local rtKey = 'shopmicro:user:refresh:' .. rt
    redis.call('DEL', rtKey)
end
redis.call('DEL', rtSetKey)
redis.call('DEL', tokenKey)
return 1
`

// checkLoginAttempts 检查手机号是否有过多的近期登录失败记录。
func (s *UserServiceImpl) checkLoginAttempts(ctx context.Context, phone string) error {
	key := fmt.Sprintf("shopmicro:login:fail:%s", phone)
	count, err := s.rdb.Get(ctx, key)
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil
		}
		logger.Warnf("login: redis unavailable for rate check, failing closed (phone=%s): %v", maskPhone(phone), err)
		return errAccountLocked
	}
	if n, _ := strconv.Atoi(count); n >= loginMaxFailures {
		return errAccountLocked
	}
	return nil
}

// recordLoginFailure 递增手机号的登录失败计数器。
func (s *UserServiceImpl) recordLoginFailure(ctx context.Context, phone string) {
	key := fmt.Sprintf("shopmicro:login:fail:%s", phone)
	if _, err := s.rdb.RunScript(ctx, loginFailIncrScript, []string{key}, int(loginLockoutTTL.Seconds())); err != nil {
		logger.Warnf("login: failed to record login failure for phone %s: %v", maskPhone(phone), err)
	}
}

// clearLoginFailures 在成功登录后清除登录失败计数器。
func (s *UserServiceImpl) clearLoginFailures(ctx context.Context, phone string) {
	key := fmt.Sprintf("shopmicro:login:fail:%s", phone)
	_ = s.rdb.Del(ctx, key)
}

// checkOwnership 验证调用者的 user_id 是否与请求的用户 ID 匹配。
func (s *UserServiceImpl) checkOwnership(ctx context.Context, targetID int64) error {
	userIDStr, ok := metainfo.GetPersistentValue(ctx, "user_id")
	if !ok || userIDStr == "" {
		return errUnauthorized
	}
	callerID, err := parseInt64(userIDStr)
	if err != nil || callerID == 0 {
		return errUnauthorized
	}
	if callerID != targetID {
		return errForbidden
	}
	return nil
}

// =========================
// 辅助函数
// =========================

// isDuplicateKeyError 检查错误是否为 MySQL 重复键错误（错误码 1062）。
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1062
	}
	return false
}

// parseInt64 使用 strconv 将字符串解析为 int64。
func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// maskPhone 脱敏手机号：保留前3后4，中间用 * 代替（中国手机号 11 位）
func maskPhone(phone string) string {
	if len(phone) != 11 {
		return "***"
	}
	return phone[:3] + "****" + phone[7:]
}

// maskEmail 脱敏邮箱：a***@example.com
func maskEmail(email string) string {
	if email == "" {
		return ""
	}
	at := strings.Index(email, "@")
	if at < 1 {
		return "***"
	}
	return email[:1] + "***" + email[at:]
}
