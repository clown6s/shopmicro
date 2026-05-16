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
	"shopmicro/pkg/redis"
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
	errRegisterTooMany  = newBiz(errcode.CodeUserInternal, "too many registration attempts, please try again later")
	errRefreshTooMany   = newBiz(errcode.CodeUserInternal, "too many refresh attempts, please try again later")
)

// P1-21: phone number regex — Chinese mobile format
var phoneRegex = regexp.MustCompile(`^1[3-9]\d{9}$`)

// emailRegex validates basic email format (RFC 5322 simplified)
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// passwordRegex enforces at least one letter + one digit + special chars ok, 8-128 chars
var passwordRegex = regexp.MustCompile(`^.{8,128}$`)

// passwordValid checks that the password contains at least one letter AND one digit.
func passwordValid(pwd string) bool {
	if !passwordRegex.MatchString(pwd) {
		return false
	}
	var hasLetter, hasDigit bool
	for _, c := range pwd {
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			hasLetter = true
		case c >= '0' && c <= '9':
			hasDigit = true
		}
		if hasLetter && hasDigit {
			return true
		}
	}
	return false
}

// UserServiceImpl implements the user service gRPC handlers.
type UserServiceImpl struct {
	userDal   *data.UserDal
	jwtGen    *jwt.Generator
	jwtExpHrs int
	rtGen     *jwt.Generator // refresh token generator
	rtExpHrs  int            // refresh token expiration hours
	rdb       *redis.Client
}

func NewUserHandler(d *data.Data, jwtSecret string, jwtExpHours int, jwtIssuer string, jwtRefreshSecret string) *UserServiceImpl {
	// P2-16 fix: guard against zero jwtExpHours to prevent degenerate rtExpHrs
	if jwtExpHours <= 0 {
		jwtExpHours = 24
	}
	gen, err := jwt.NewGenerator(jwtSecret, time.Duration(jwtExpHours)*time.Hour, jwtIssuer)
	if err != nil {
		logger.Fatalf("init jwt generator failed: %v", err)
	}
	// Refresh token: 7 days default (must be >= access token)
	rtExpHrs := jwtExpHours * 7
	if rtExpHrs < 24 {
		rtExpHrs = 168 // at least 7 days
	}

	// token secret defeats the purpose of dual-token architecture.
	if jwtRefreshSecret == "" {
		logger.Fatalf("JWT_REFRESH_SECRET is required and must differ from JWT_SECRET")
	}
	rtGen, err := jwt.NewGenerator(jwtRefreshSecret, time.Duration(rtExpHrs)*time.Hour, jwtIssuer)
	if err != nil {
		logger.Fatalf("init refresh token generator failed: %v", err)
	}
	return &UserServiceImpl{
		userDal:   d.User(),
		jwtGen:    gen,
		jwtExpHrs: jwtExpHours,
		rtGen:     rtGen,
		rtExpHrs:  rtExpHrs,
		rdb:       d.Rdb,
	}
}

const tokenPrefix = "shopmicro:user:token:"
const refreshTokenPrefix = "shopmicro:user:refresh:"

// P2-06: Registration rate limit — per-phone, max 5 attempts per hour
const registerRateLimitKey = "shopmicro:register:rate:"
const registerRateLimitMax = 5
const registerRateLimitWindowSec = 3600 // 1 hour

// P2-06: Lua script for atomic rate limit check-and-increment
var registerRateLimitLua = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
    redis.call('EXPIRE', KEYS[1], ARGV[2])
end
if tonumber(count) > tonumber(ARGV[1]) then
    return 1
end
return 0
`)

// P2-07: RefreshToken rate limit — per-userID, max 10 per minute
const refreshRateLimitKey = "shopmicro:refresh:rate:"
const refreshRateLimitMax = 10
const refreshRateLimitWindowSec = 60

// P2-07: Lua script for refresh token rate limiting
var refreshRateLimitLua = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
    redis.call('EXPIRE', KEYS[1], ARGV[2])
end
if tonumber(count) > tonumber(ARGV[1]) then
    return 1
end
return 0
`)

// P1-05: Account-level brute force protection constants
// R4-P1-03: Atomic INCR + conditional EXPIRE Lua script.
var loginFailIncrLua = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
    redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return count
`)

const (
	loginMaxFailures = 10               // max failed attempts before lockout
	loginLockoutTTL  = 15 * time.Minute // lockout duration
)

// R2-P2-04: Atomic Lua script to revoke all user tokens (access + refresh).
var revokeTokensLua = redis.NewScript(`
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
`)

// checkLoginAttempts checks if the phone number has too many recent failed login attempts.
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

// recordLoginFailure increments the failed login counter for a phone number.
func (s *UserServiceImpl) recordLoginFailure(ctx context.Context, phone string) {
	key := fmt.Sprintf("shopmicro:login:fail:%s", phone)
	if _, err := loginFailIncrLua.Run(ctx, s.rdb, []string{key}, int(loginLockoutTTL.Seconds())); err != nil {
		logger.Warnf("login: failed to record login failure for phone %s: %v", maskPhone(phone), err)
	}
}

// clearLoginFailures removes the failed login counter on successful login.
func (s *UserServiceImpl) clearLoginFailures(ctx context.Context, phone string) {
	key := fmt.Sprintf("shopmicro:login:fail:%s", phone)
	_ = s.rdb.Del(ctx, key)
}

// checkOwnership verifies the caller's user_id matches the requested user ID.
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
// helpers
// =========================

// isDuplicateKeyError checks if an error is a MySQL duplicate key error (code 1062).
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

// parseInt64 parses a string to int64 using strconv.
func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// maskPhone 脱敏手机号：保留后3位，其余用 * 代替
func maskPhone(phone string) string {
	if len(phone) <= 3 {
		return "***"
	}
	return "****" + phone[len(phone)-3:]
}

// maskEmail masks email: a***@example.com
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

// maskUserID returns a consistent masked representation of a user ID.
// so hiding it in the response body adds no real security.
func maskUserID(id int64) int64 {
	if id == 0 {
		return 0
	}
	return id ^ 0x5A5A5A5A5A5A5A5A
}
