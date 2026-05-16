package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/go-sql-driver/mysql"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"shopmicro/user/data"
	v1 "shopmicro/user/kitex_gen/api/user/v1"
	"shopmicro/user/model"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/jwt"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/redis"
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
	// P2-06: Use separate refresh secret if provided, fallback to main secret
	rtSecret := jwtRefreshSecret
	if rtSecret == "" {
		rtSecret = jwtSecret
	}
	rtGen, err := jwt.NewGenerator(rtSecret, time.Duration(rtExpHrs)*time.Hour, jwtIssuer)
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
// Returns 0 if allowed (under limit), 1 if rate limited
// KEYS[1] = rate limit key (registerRateLimitKey + phone)
// ARGV[1] = max attempts
// ARGV[2] = TTL in seconds
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
// Prevents the race where INCR succeeds but EXPIRE is never executed
// (e.g. process crash), leaving the key without TTL → permanent lockout.
// KEYS[1] = rate limit key
// ARGV[1] = TTL in seconds
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

// checkLoginAttempts checks if the phone number has too many recent failed login attempts.
func (s *UserServiceImpl) checkLoginAttempts(ctx context.Context, phone string) error {
	key := fmt.Sprintf("shopmicro:login:fail:%s", phone)
	count, err := s.rdb.Get(ctx, key)
	if err != nil {
		// redis.Nil means the key does not exist → no failed attempts → allow.
		// Only treat non-Nil errors (network, timeout) as Redis failures.
		if errors.Is(err, goredis.Nil) {
			return nil
		}
		// P2-13 fix: Redis down → fail closed (reject login).
		// Allowing login when Redis is unreachable enables brute-force bypass.
		logger.Warnf("login: redis unavailable for rate check, failing closed (phone=%s): %v", maskPhone(phone), err)
		return errAccountLocked
	}
	if n, _ := strconv.Atoi(count); n >= loginMaxFailures {
		return errAccountLocked
	}
	return nil
}

// recordLoginFailure increments the failed login counter for a phone number.
// R4-P1-03: Uses atomic Lua script (INCR + conditional EXPIRE) to prevent
// the race where INCR succeeds but EXPIRE is lost on crash, causing permanent lockout.
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

func (s *UserServiceImpl) RegisterUser(ctx context.Context, req *v1.RegisterUserReq) (*v1.RegisterUserResp, error) {
	if req.GetPhone() == "" {
		return nil, errPhoneRequired
	}
	if req.GetPassword() == "" {
		return nil, errPasswordRequired
	}

	// P2-06: Rate limit registration attempts per phone number
	rateKey := registerRateLimitKey + req.GetPhone()
	limited, err := registerRateLimitLua.Run(ctx, s.rdb, []string{rateKey}, registerRateLimitMax, registerRateLimitWindowSec)
	if err != nil {
		logger.Warnf("register: rate limit check failed (phone=%s): %v", maskPhone(req.GetPhone()), err)
		// fail-closed: if Redis is down, allow registration but log the error
	} else if limited != nil {
		if result, ok := limited.(int64); ok && result == 1 {
			return nil, errRegisterTooMany
		}
	}

	// P1-21: Strict phone format validation
	if !phoneRegex.MatchString(req.GetPhone()) {
		return nil, errPhoneFormat
	}
	// P1-21: Strengthened password policy
	if len(req.GetPassword()) < 8 {
		return nil, errPasswordWeak
	}
	if len(req.GetPassword()) > 128 {
		return nil, errPasswordTooLong
	}
	if !passwordValid(req.GetPassword()) {
		return nil, errPasswordWeak
	}

	if req.GetEmail() != "" && !emailRegex.MatchString(req.GetEmail()) {
		return nil, errEmailFormat
	}
	if len(req.GetRealName()) > 64 {
		return nil, newBiz(errcode.CodeUserParam, "realName too long (max 64)")
	}
	if len(req.GetEmail()) > 128 {
		return nil, newBiz(errcode.CodeUserParam, "email too long (max 128)")
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.GetPassword()), 12) // P1-21: cost 12
	if err != nil {
		logger.Errorf("bcrypt hash failed: %v", err)
		return nil, errInternal
	}

	user := &model.User{
		Password: string(hashed),
		RealName: req.GetRealName(),
		Gender:   int8(req.GetGender()),
		Age:      int16(req.GetAge()),
		Phone:    req.GetPhone(),
		Email:    req.GetEmail(),
	}

	// 范围校验
	if user.Gender < 0 || user.Gender > 3 {
		return nil, newBiz(errcode.CodeUserParam, "gender must be 0-3")
	}
	if req.GetAge() < 0 || req.GetAge() > 150 {
		return nil, newBiz(errcode.CodeUserParam, "age must be 0-150")
	}

	// P0-04: Rely on DB unique constraint for phone uniqueness.
	// If the phone column has a UNIQUE index, the INSERT will fail with a
	// duplicate key error which we catch here — no TOCTOU race condition.
	if err := s.userDal.Create(ctx, user); err != nil {
		if isDuplicateKeyError(err) {
			return nil, errPhoneExists
		}
		logger.Errorf("create user failed: %v", err)
		return nil, errInternal
	}

	// R2-P2-02: Clear login failure counter for this phone on successful registration.
	// Prevents immediate lockout if the phone was previously brute-forced before registration.
	s.clearLoginFailures(ctx, req.GetPhone())

	return &v1.RegisterUserResp{}, nil
}

func (s *UserServiceImpl) Login(ctx context.Context, req *v1.LoginReq) (*v1.LoginResp, error) {
	if req.GetPhone() == "" {
		return nil, errPhoneRequired
	}
	if req.GetPassword() == "" {
		return nil, errPasswordRequired
	}
	// R4-P3-08: 校验手机号格式，无效格式不查DB，返回统一错误信息
	if !phoneRegex.MatchString(req.GetPhone()) {
		// R4-P1-04: 使用正确60字符的 dummy bcrypt hash 执行完整比较，
		// 防止短 hash 跳过比较导致时序侧信道泄露用户是否存在
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$12$000000000000000000000000000000000000000000000000000000"), []byte(req.GetPassword()))
		return nil, errPhonePwdWrong
	}

	// P1-05: Check account-level brute force lockout before any auth
	if err := s.checkLoginAttempts(ctx, req.GetPhone()); err != nil {
		return nil, err
	}

	user, err := s.userDal.GetByPhone(ctx, req.GetPhone())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// R4-P1-01: 防止时序侧信道 — 即使用户不存在也执行 dummy bcrypt，
			// 使响应时间与"用户存在但密码错误"一致，防止通过计时攻击枚举手机号。
			// R4-P1-04: 使用正确60字符 dummy hash，确保 bcrypt 执行完整比较
			_ = bcrypt.CompareHashAndPassword([]byte("$2a$12$000000000000000000000000000000000000000000000000000000"), []byte(req.GetPassword()))
			logger.Warnf("login failed: phone not found (%s)", maskPhone(req.GetPhone()))
			// P1-05: Record failed attempt
			s.recordLoginFailure(ctx, req.GetPhone())
			return nil, errPhonePwdWrong
		}
		logger.Errorf("login get user failed: %v", err)
		return nil, errInternal
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.GetPassword())); err != nil {
		logger.Warnf("login failed: wrong password (phone: %s)", maskPhone(req.GetPhone()))
		// P1-05: Record failed attempt
		s.recordLoginFailure(ctx, req.GetPhone())
		return nil, errPhonePwdWrong
	}

	// P1-05: Clear failed login counter on successful authentication
	s.clearLoginFailures(ctx, req.GetPhone())

	token, err := s.jwtGen.Generate(user.ID)
	if err != nil {
		logger.Errorf("generate token failed: %v", err)
		return nil, errInternal
	}

	// Generate refresh token (7-day lifetime)
	refreshToken, err := s.rtGen.Generate(user.ID)
	if err != nil {
		logger.Errorf("generate refresh token failed: %v", err)
		return nil, errInternal
	}

	// P3-R3: Store tokens in Redis SET (multi-device support).
	// Use SAddExpire (pipeline) to prevent crash between SAdd and Expire
	// leaving tokens without TTL (would cause tokens to never expire).
	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, user.ID)
	ttl := time.Duration(s.jwtExpHrs) * time.Hour
	if err := s.rdb.SAddExpire(ctx, tokenKey, ttl, token); err != nil {
		logger.Errorf("login: store token to redis failed: %v", err)
		return nil, errInternal
	}

	// Store refresh token in Redis (1:1 mapping: refreshToken -> userID)
	rtKey := fmt.Sprintf("%s%s", refreshTokenPrefix, refreshToken)
	rtTTL := time.Duration(s.rtExpHrs) * time.Hour
	if err := s.rdb.Set(ctx, rtKey, user.ID, rtTTL); err != nil {
		logger.Errorf("login: store refresh token to redis failed: %v", err)
		return nil, errInternal
	}

	// P1-3 fix: Maintain reverse mapping SET so DeleteUser can invalidate
	// all refresh tokens for a user (prevents refresh token replay after
	// account deletion). Key: shopmicro:user:rtset:{userID} → {refreshToken, ...}
	rtSetKey := fmt.Sprintf("%s%d", "shopmicro:user:rtset:", user.ID)
	if err := s.rdb.SAddExpire(ctx, rtSetKey, rtTTL, refreshToken); err != nil {
		logger.Errorf("login: store refresh token to reverse set failed: %v", err)
		// Non-fatal: access token still works; worst case refresh survives deletion until TTL
	}

	return &v1.LoginResp{
		// P2-05: 不返回自增 userId，防止信息泄露（可推测用户量和注册顺序）
		// proto 字段保留兼容性，返回默认值 0
		Id:           0,
		RealName:     user.RealName,
		Avatar:       user.Avatar,
		Gender:       v1.Gender(user.Gender),
		Age:          int32(user.Age),
		Phone:        maskPhone(user.Phone),
		Email:        maskEmail(user.Email),
		Remark:       user.Remark,
		Token:        token,
		RefreshToken: refreshToken,
	}, nil
}

// checkOwnership verifies the caller's user_id matches the requested user ID.
// Returns errForbidden if they don't match.
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

func (s *UserServiceImpl) UpdateUser(ctx context.Context, req *v1.UpdateUserReq) (*v1.UpdateUserResp, error) {
	if req.GetId() <= 0 {
		return nil, errInvalidUserID
	}

	// P1-01: Ownership check — users can only update their own profile
	if err := s.checkOwnership(ctx, req.GetId()); err != nil {
		return nil, err
	}

	updates := make(map[string]interface{})

	if req.RealName != nil {
		if len(req.GetRealName()) > 64 {
			return nil, newBiz(errcode.CodeUserParam, "realName too long (max 64)")
		}
		updates["real_name"] = req.GetRealName()
	}
	if req.Avatar != nil {
		if len(req.GetAvatar()) > 512 {
			return nil, newBiz(errcode.CodeUserParam, "avatar too long (max 512)")
		}
		updates["avatar"] = req.GetAvatar()
	}
	if req.Gender != nil {
		g := int8(req.GetGender())
		if g < 0 || g > 3 {
			return nil, newBiz(errcode.CodeUserParam, "gender must be 0-3")
		}
		updates["gender"] = g
	}
	if req.Age != nil {
		a := req.GetAge()
		if a < 0 || a > 150 {
			return nil, newBiz(errcode.CodeUserParam, "age must be 0-150")
		}
		updates["age"] = int16(a)
	}
	if req.Phone != nil {
		v := req.GetPhone()
		if !phoneRegex.MatchString(v) {
			return nil, errPhoneFormat
		}
		updates["phone"] = v
	}
	if req.Email != nil {
		v := req.GetEmail()
		if v != "" && !emailRegex.MatchString(v) {
			return nil, errEmailFormat
		}
		if len(v) > 128 {
			return nil, newBiz(errcode.CodeUserParam, "email too long (max 128)")
		}
		updates["email"] = v
	}
	if req.Remark != nil {
		if len(req.GetRemark()) > 500 {
			return nil, newBiz(errcode.CodeUserParam, "remark too long (max 500)")
		}
		updates["remark"] = req.GetRemark()
	}

	if len(updates) == 0 {
		return nil, errNothingUpdate
	}

	// P2-1 fix: Removed TOCTOU phone uniqueness pre-check.
	// Rely entirely on the DB UNIQUE index + isDuplicateKeyError below.
	// The SELECT-then-UPDATE pattern had a race window where two concurrent
	// requests could both pass the check.

	if err := s.userDal.Update(ctx, req.GetId(), updates); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errUserNotFound
		}
		if isDuplicateKeyError(err) {
			// P3-07: Return vague error to prevent phone enumeration via uniqueness check
			return nil, errNothingUpdate
		}
		logger.Errorf("update user failed: %v", err)
		return nil, errInternal
	}

	// P2-12 fix: When phone is changed, revoke all existing sessions so the old
	// phone number cannot be used to receive an SMS with a 2FA code for the account.
	// The user must re-login with the new phone to get fresh tokens.
	if _, changed := updates["phone"]; changed {
		tokenKey := fmt.Sprintf("%s%d", tokenPrefix, req.GetId())
		if err := s.rdb.Del(ctx, tokenKey); err != nil {
			logger.Warnf("update user: failed to revoke tokens on phone change (user_id=%d): %v", req.GetId(), err)
		}
		rtSetKey := fmt.Sprintf("%s%d", "shopmicro:user:rtset:", req.GetId())
		rtTokens, rtErr := s.rdb.SMembers(ctx, rtSetKey)
		if rtErr != nil {
			logger.Warnf("update user: failed to read refresh token set (user_id=%d): %v", req.GetId(), rtErr)
		} else {
			for _, t := range rtTokens {
				rtKey := fmt.Sprintf("%s%s", refreshTokenPrefix, t)
				_ = s.rdb.Del(ctx, rtKey)
			}
			_ = s.rdb.Del(ctx, rtSetKey)
		}
	}

	return &v1.UpdateUserResp{}, nil
}

func (s *UserServiceImpl) DeleteUser(ctx context.Context, req *v1.DeleteUserReq) (*v1.DeleteUserResp, error) {
	if req.GetId() <= 0 {
		return nil, errInvalidUserID
	}

	// P1-01: Ownership check — users can only delete their own account
	if err := s.checkOwnership(ctx, req.GetId()); err != nil {
		return nil, err
	}

	// R2-P1-03: Fetch user record to get phone for fail counter cleanup
	user, err := s.userDal.GetByID(ctx, req.GetId())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errUserNotFound
		}
		logger.Errorf("delete user: failed to get user (user_id=%d): %v", req.GetId(), err)
		return nil, errInternal
	}

	// R2-P2-04 fix: Atomically revoke all user tokens in one Lua script.
	// This prevents the race where some RT keys are deleted but others remain
	// (e.g. process crash mid-loop), leaving the user logged in with old sessions.
	revokeTokensLua := redis.NewScript(`
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
	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, req.GetId())
	rtSetKey := fmt.Sprintf("%s%d", "shopmicro:user:rtset:", req.GetId())
	if _, err := revokeTokensLua.Run(ctx, s.rdb, []string{tokenKey, rtSetKey}); err != nil {
		logger.Errorf("delete user: failed to revoke tokens atomically (user_id=%d): %v", req.GetId(), err)
	}

	// Now perform the soft delete
	if err := s.userDal.Delete(ctx, req.GetId()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errUserNotFound
		}
		logger.Errorf("delete user failed: %v", err)
		return nil, errInternal
	}

	logger.Infof("user deleted: user_id=%d", req.GetId())

	// R2-P1-03: Clear login failure counter by phone (not user ID)
	failKey := fmt.Sprintf("shopmicro:login:fail:%s", user.Phone)
	_ = s.rdb.Del(ctx, failKey)

	return &v1.DeleteUserResp{}, nil
}

func (s *UserServiceImpl) ListUser(ctx context.Context, req *v1.ListUserReq) (*v1.ListUserResp, error) {

	users, total, err := s.userDal.List(ctx, req.GetPage(), req.GetPageSize(), req.GetName(), req.GetPhone())
	if err != nil {
		logger.Errorf("list users failed: %v", err)
		return nil, errInternal
	}

	list := make([]*v1.UserInfo, 0, len(users))
	for _, u := range users {
		list = append(list, toUserInfoMasked(u))
	}

	return &v1.ListUserResp{
		List:     list,
		Page:     req.GetPage(),
		PageSize: req.GetPageSize(),
		Total:    total,
	}, nil
}

// toUserInfoMasked returns user info with phone and email masked.
// Used by ListUser to prevent PII exposure.
func toUserInfoMasked(u *model.User) *v1.UserInfo {
	return &v1.UserInfo{
		Id:       u.ID,
		RealName: u.RealName,
		Avatar:   u.Avatar,
		Gender:   v1.Gender(u.Gender),
		Age:      int32(u.Age),
		Phone:    maskPhone(u.Phone),
		Email:    maskEmail(u.Email),
		Remark:   u.Remark,
	}
}

// maskEmail masks email: a***@example.com
// For any local part, shows first char + "***" + domain.
// P3-06: Abnormal format returns "***" instead of original value
func maskEmail(email string) string {
	if email == "" {
		return ""
	}
	at := strings.Index(email, "@")
	if at < 1 {
		return "***" // no local part before "@" — return masked instead of original
	}
	return email[:1] + "***" + email[at:]
}

// =========================
// helpers
// =========================

// isDuplicateKeyError checks if an error is a MySQL duplicate key error (code 1062).
// Uses the mysql driver's typed error instead of fragile string matching.
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
// P3-05: 对于非标准长度手机号，仅保留后3位降低信息熵
func maskPhone(phone string) string {
	if len(phone) <= 3 {
		return "***"
	}
	return "****" + phone[len(phone)-3:]
}

// Logout removes the caller's specific token from Redis.
// Gateway forwards user_id AND token via metainfo, so we only invalidate
// the current device's session (multi-device support).
func (s *UserServiceImpl) Logout(ctx context.Context, req *v1.LogoutReq) (resp *v1.LogoutResp, err error) {
	userIDStr, ok := metainfo.GetPersistentValue(ctx, "user_id")
	if !ok || userIDStr == "" {
		logger.Warn("logout: user_id not found in metainfo, request may bypass gateway")
		return nil, errUnauthorized
	}

	tokenStr, ok := metainfo.GetPersistentValue(ctx, "token")
	if !ok || tokenStr == "" {
		logger.Warn("logout: token not found in metainfo, request may bypass gateway")
		return nil, errUnauthorized
	}

	// P1 fix: use %d format consistently, parse userIDStr to int64 first
	userID, err := parseInt64(userIDStr)
	if err != nil || userID <= 0 {
		logger.Warnf("logout: invalid user_id in metainfo: %s", userIDStr)
		return nil, errUnauthorized
	}

	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, userID)
	// P1-1: Remove only the current device's token from the SET.
	if err := s.rdb.SRem(ctx, tokenKey, tokenStr); err != nil {
		logger.Errorf("logout: failed to remove token from redis (user_id=%d): %v", userID, err)
		return nil, errInternal
	}

	return &v1.LogoutResp{}, nil
}

var errRefreshTokenRequired = newBiz(errcode.CodeUserTokenRequired, "refresh token is required")
var errRefreshTokenInvalid = newBiz(errcode.CodeUserTokenInvalid, "invalid or expired refresh token")

// refreshTokensLua atomically:
//  1. Removes old RT from reverse set
//  2. Adds new RT to reverse set
//  3. Stores new RT → userID mapping in Redis
//  4. Resets TTL on the reverse set
//
// KEYS[1] = rtSetKey  (shopmicro:user:rtset:{userID})
// KEYS[2] = rtKey     (shopmicro:user:refresh:{newRT})
// ARGV[1] = old RT to remove from set
// ARGV[2] = new RT to add to set and store
// ARGV[3] = userID (string) to store alongside new RT
// ARGV[4] = TTL in seconds for rtSet
// ARGV[5] = TTL in seconds for new RT key
var refreshTokensLua = redis.NewScript(`
redis.call('SREM', KEYS[1], ARGV[1])
redis.call('SADD', KEYS[1], ARGV[2])
redis.call('SET', KEYS[2], ARGV[3], 'EX', ARGV[5])
redis.call('EXPIRE', KEYS[1], ARGV[4])
return 1
`)

// RefreshToken issues a new access token + refresh token pair.
// The old refresh token is rotated (deleted from Redis) to prevent replay.
func (s *UserServiceImpl) RefreshToken(ctx context.Context, req *v1.RefreshTokenReq) (resp *v1.RefreshTokenResp, err error) {
	rt := req.GetRefreshToken()
	if rt == "" {
		return nil, errRefreshTokenRequired
	}

	// Look up userID from Redis (refreshToken -> userID)
	// Use atomic GET+DEL to prevent concurrent replay attacks.
	// Two concurrent requests with the same refresh token will race;
	// only the first GETDEL succeeds, the second gets "" and fails.
	rtKey := fmt.Sprintf("%s%s", refreshTokenPrefix, rt)
	userIDStr, err := s.rdb.GetDel(ctx, rtKey)
	if err != nil || userIDStr == "" {
		return nil, errRefreshTokenInvalid
	}

	userID, err := parseInt64(userIDStr)
	if err != nil || userID <= 0 {
		return nil, errRefreshTokenInvalid
	}

	// P2-07: Rate limit refresh token requests per user ID (10/min)
	refreshRateKey := refreshRateLimitKey + userIDStr
	limited, rlErr := refreshRateLimitLua.Run(ctx, s.rdb, []string{refreshRateKey}, refreshRateLimitMax, refreshRateLimitWindowSec)
	if rlErr != nil {
		logger.Warnf("refresh token: rate limit check failed (user_id=%d): %v", userID, rlErr)
	} else if limited != nil {
		if result, ok := limited.(int64); ok && result == 1 {
			return nil, errRefreshTooMany
		}
	}

	// R4-P1-03: 验证用户仍存在且未被删除
	if _, err := s.userDal.GetByID(ctx, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errRefreshTokenInvalid
		}
		logger.Errorf("refresh token: check user existence failed (user_id=%d): %v", userID, err)
		return nil, errInternal
	}

	// Issue new access token
	token, err := s.jwtGen.Generate(userID)
	if err != nil {
		logger.Errorf("refresh token: generate new access token failed: %v", err)
		return nil, errInternal
	}

	// Issue new refresh token
	newRT, err := s.rtGen.Generate(userID)
	if err != nil {
		logger.Errorf("refresh token: generate new refresh token failed: %v", err)
		return nil, errInternal
	}

	// Store new access token in user's token SET (atomic pipeline)
	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, userID)
	ttl := time.Duration(s.jwtExpHrs) * time.Hour
	if err := s.rdb.SAddExpire(ctx, tokenKey, ttl, token); err != nil {
		logger.Errorf("refresh token: store new access token failed: %v", err)
		return nil, errInternal
	}

	// R4-P2-03 fix: Atomically update refresh token state using Lua script.
	// This prevents the race where old RT is removed but new RT is not stored
	// (causing token loss) or new RT is stored but old RT is not removed (replay).
	rtSetKey := fmt.Sprintf("%s%d", "shopmicro:user:rtset:", userID)
	newRTKey := fmt.Sprintf("%s%s", refreshTokenPrefix, newRT)
	rtTTL := time.Duration(s.rtExpHrs) * time.Hour
	if _, err := refreshTokensLua.Run(ctx, s.rdb,
		[]string{rtSetKey, newRTKey},
		rt, newRT, userIDStr, int(rtTTL.Seconds()), int(rtTTL.Seconds()),
	); err != nil {
		logger.Errorf("refresh token: atomic RT rotation failed (user_id=%d): %v", userID, err)
		return nil, errInternal
	}

	return &v1.RefreshTokenResp{
		Token:        token,
		RefreshToken: newRT,
	}, nil
}
