package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/go-sql-driver/mysql"
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
	errPasswordTooLong  = newBiz(errcode.CodeUserPwdWeak, "password must not exceed 128 characters")
	errPhoneExists      = newBiz(errcode.CodeUserPhoneExists, "phone already exists")
	errInternal         = newBiz(errcode.CodeUserInternal, "internal error")
	errPhonePwdWrong    = newBiz(errcode.CodeUserPhonePwdWrong, "phone or password incorrect")
	errInvalidUserID    = newBiz(errcode.CodeUserInvalidID, "invalid user id")
	errNothingUpdate    = newBiz(errcode.CodeUserNothingUpdate, "nothing to update")
	errUserNotFound     = newBiz(errcode.CodeUserNotFound, "user not found")
	errUnauthorized     = newBiz(errcode.CodeUserUnauthorized, "unauthorized: missing user identity")
	errForbidden        = newBiz(errcode.CodeUserForbidden, "forbidden: cannot operate other user's data")
	errEmailFormat      = newBiz(errcode.CodeUserEmailFormat, "invalid email format")
)

// P1-21: phone number regex — Chinese mobile format
var phoneRegex = regexp.MustCompile(`^1[3-9]\d{9}$`)

// emailRegex validates basic email format (RFC 5322 simplified)
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// passwordRegex enforces at least one letter + one digit, 8-128 chars
var passwordRegex = regexp.MustCompile(`^[a-zA-Z\d]{8,128}$`)

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

func NewUserHandler(d *data.Data, jwtSecret string, jwtExpHours int) *UserServiceImpl {
	gen, err := jwt.NewGenerator(jwtSecret, time.Duration(jwtExpHours)*time.Hour)
	if err != nil {
		logger.Fatalf("init jwt generator failed: %v", err)
	}
	// Refresh token: 7 days default (must be >= access token)
	rtExpHrs := jwtExpHours * 7
	if rtExpHrs < 24 {
		rtExpHrs = 168 // at least 7 days
	}
	rtGen, err := jwt.NewGenerator(jwtSecret, time.Duration(rtExpHrs)*time.Hour)
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

func (s *UserServiceImpl) RegisterUser(ctx context.Context, req *v1.RegisterUserReq) (*v1.RegisterUserResp, error) {
	if req.GetPhone() == "" {
		return nil, errPhoneRequired
	}
	if req.GetPassword() == "" {
		return nil, errPasswordRequired
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

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.GetPassword()), 12) // P1-21: cost 12
	if err != nil {
		logger.Errorf("bcrypt hash failed: %v", err)
		return nil, errInternal
	}

	user := &model.User{
		Password: string(hashed),
		RealName: req.GetRealName(),
		Gender:   int8(req.GetGender()),
		Age:      int8(req.GetAge()),
		Phone:    req.GetPhone(),
		Email:    req.GetEmail(),
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

	return &v1.RegisterUserResp{}, nil
}

func (s *UserServiceImpl) Login(ctx context.Context, req *v1.LoginReq) (*v1.LoginResp, error) {
	if req.GetPhone() == "" {
		return nil, errPhoneRequired
	}
	if req.GetPassword() == "" {
		return nil, errPasswordRequired
	}

	user, err := s.userDal.GetByPhone(ctx, req.GetPhone())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			logger.Warnf("login failed: phone not found (%s)", maskPhone(req.GetPhone()))
			return nil, errPhonePwdWrong
		}
		logger.Errorf("login get user failed: %v", err)
		return nil, errInternal
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.GetPassword())); err != nil {
		logger.Warnf("login failed: wrong password (phone: %s)", maskPhone(req.GetPhone()))
		return nil, errPhonePwdWrong
	}

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

	// P1-1: Store tokens in Redis SET (multi-device support).
	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, user.ID)
	ttl := time.Duration(s.jwtExpHrs) * time.Hour
	if err := s.rdb.SAdd(ctx, tokenKey, token); err != nil {
		logger.Warnf("login: store token to redis failed (user still logged in): %v", err)
	} else if err := s.rdb.Expire(ctx, tokenKey, ttl); err != nil {
		logger.Warnf("login: set token ttl failed: %v", err)
	}

	// Store refresh token in Redis (1:1 mapping: refreshToken -> userID)
	rtKey := fmt.Sprintf("%s%s", refreshTokenPrefix, refreshToken)
	rtTTL := time.Duration(s.rtExpHrs) * time.Hour
	if err := s.rdb.Set(ctx, rtKey, user.ID, rtTTL); err != nil {
		logger.Warnf("login: store refresh token to redis failed: %v", err)
	}

	return &v1.LoginResp{
		Id:           user.ID,
		RealName:     user.RealName,
		Avatar:       user.Avatar,
		Gender:       v1.Gender(user.Gender),
		Age:          int32(user.Age),
		Phone:        user.Phone,
		Email:        user.Email,
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
		updates["real_name"] = req.GetRealName()
	}
	if req.Avatar != nil {
		updates["avatar"] = req.GetAvatar()
	}
	if req.Gender != nil {
		updates["gender"] = int8(req.GetGender())
	}
	if req.Age != nil {
		updates["age"] = int8(req.GetAge())
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
		updates["email"] = v
	}
	if req.Remark != nil {
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
			return nil, errPhoneExists
		}
		logger.Errorf("update user failed: %v", err)
		return nil, errInternal
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

	if err := s.userDal.Delete(ctx, req.GetId()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errUserNotFound
		}
		logger.Errorf("delete user failed: %v", err)
		return nil, errInternal
	}

	// P2-05: Clear ALL Redis tokens for this user (DELETE on SET key removes the whole set).
	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, req.GetId())
	if err := s.rdb.Del(ctx, tokenKey); err != nil {
		logger.Warnf("delete user: failed to clear redis tokens (user_id=%d): %v", req.GetId(), err)
	}

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
		list = append(list, toUserInfo(u))
	}

	return &v1.ListUserResp{
		List:     list,
		Page:     req.GetPage(),
		PageSize: req.GetPageSize(),
		Total:    total,
	}, nil
}

func toUserInfo(u *model.User) *v1.UserInfo {
	return &v1.UserInfo{
		Id:       u.ID,
		RealName: u.RealName,
		Avatar:   u.Avatar,
		Gender:   v1.Gender(u.Gender),
		Age:      int32(u.Age),
		Phone:    u.Phone,
		Email:    u.Email,
		Remark:   u.Remark,
	}
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

// parseInt64 parses a string to int64.
// Returns an error if the string contains any non-digit character or is empty.
func parseInt64(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("parseInt64: empty string")
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("parseInt64: invalid character %c", c)
		}
		// 溢出检查
		if n > (1<<63-1)/10 {
			return 0, fmt.Errorf("parseInt64: overflow")
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

// maskPhone 脱敏手机号：保留后4位，其余用 * 代替
func maskPhone(phone string) string {
	if len(phone) <= 4 {
		return "****"
	}
	return "****" + phone[len(phone)-4:]
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
		logger.Warnf("logout: failed to remove token from redis (user_id=%d): %v", userID, err)
	}

	return &v1.LogoutResp{}, nil
}

var errRefreshTokenRequired = newBiz(errcode.CodeUserTokenRequired, "refresh token is required")
var errRefreshTokenInvalid = newBiz(errcode.CodeUserTokenInvalid, "invalid or expired refresh token")

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

	// Store new access token in user's token SET
	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, userID)
	ttl := time.Duration(s.jwtExpHrs) * time.Hour
	if err := s.rdb.SAdd(ctx, tokenKey, token); err != nil {
		logger.Warnf("refresh token: store new access token failed: %v", err)
	} else if err := s.rdb.Expire(ctx, tokenKey, ttl); err != nil {
		logger.Warnf("refresh token: set token ttl failed: %v", err)
	}

	// Store new refresh token
	newRTKey := fmt.Sprintf("%s%s", refreshTokenPrefix, newRT)
	rtTTL := time.Duration(s.rtExpHrs) * time.Hour
	if err := s.rdb.Set(ctx, newRTKey, userID, rtTTL); err != nil {
		logger.Warnf("refresh token: store new refresh token failed: %v", err)
	}

	return &v1.RefreshTokenResp{
		Token:        token,
		RefreshToken: newRT,
	}, nil
}
