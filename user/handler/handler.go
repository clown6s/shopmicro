package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"go.uber.org/zap"
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
)

// P1-21: phone number regex — Chinese mobile format
var phoneRegex = regexp.MustCompile(`^1[3-9]\d{9}$`)

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
	rdb       *redis.Client
}

func NewUserHandler(d *data.Data, jwtSecret string, jwtExpHours int) *UserServiceImpl {
	gen, err := jwt.NewGenerator(jwtSecret, time.Duration(jwtExpHours)*time.Hour)
	if err != nil {
		logger.Fatalf("init jwt generator failed: %v", err)
	}
	return &UserServiceImpl{
		userDal:   d.User(),
		jwtGen:    gen,
		jwtExpHrs: jwtExpHours,
		rdb:       d.Rdb,
	}
}

const tokenPrefix = "shopmicro:user:token:"

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
			return nil, errPhonePwdWrong
		}
		logger.Errorf("login get user failed: %v", err)
		return nil, errInternal
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.GetPassword())); err != nil {
		return nil, errPhonePwdWrong
	}

	token, err := s.jwtGen.Generate(user.ID, user.Phone)
	if err != nil {
		logger.Errorf("generate token failed: %v", err)
		return nil, errInternal
	}

	// P1-04: Store token in Redis — fail-open consistent with P0-01 fix.
	// If Redis is down, the login still succeeds. Token revocation becomes
	// best-effort (gateway also fail-opens on Redis errors).
	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, user.ID)
	if err := s.rdb.Set(ctx, tokenKey, token, time.Duration(s.jwtExpHrs)*time.Hour); err != nil {
		logger.Warnf("login: store token to redis failed (user still logged in): %v", err)
	}

	return &v1.LoginResp{
		Id:       user.ID,
		RealName: user.RealName,
		Avatar:   user.Avatar,
		Gender:   v1.Gender(user.Gender),
		Age:      int32(user.Age),
		Phone:    user.Phone,
		Email:    user.Email,
		Remark:   user.Remark,
		Token:    token,
	}, nil
}

// checkOwnership verifies the caller's user_id matches the requested user ID.
// Returns errForbidden if they don't match.
func (s *UserServiceImpl) checkOwnership(ctx context.Context, targetID int64) error {
	userIDStr, ok := metainfo.GetPersistentValue(ctx, "user_id")
	if !ok || userIDStr == "" {
		return errUnauthorized
	}
	callerID := parseInt64(userIDStr)
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

	// P1-06: Use req.HasField-style checks where available.
	// For proto3 without optional, we check against the zero value of the
	// underlying wire type. Since we can't distinguish "not sent" from "sent as zero",
	// we use a sentinel approach: caller must explicitly set fields they want to update.
	if v, ok := getFieldUpdateString(req.GetRealName); ok {
		updates["real_name"] = v
	}
	if v, ok := getFieldUpdateString(req.GetAvatar); ok {
		updates["avatar"] = v
	}
	if v, ok := getFieldUpdateInt32(func() int32 { return int32(req.GetGender()) }); ok {
		updates["gender"] = int8(v)
	}
	if v, ok := getFieldUpdateInt32(req.GetAge); ok {
		updates["age"] = int8(v)
	}
	if v, ok := getFieldUpdateString(req.GetPhone); ok {
		if !phoneRegex.MatchString(v) {
			return nil, errPhoneFormat
		}
		updates["phone"] = v
	}
	if v, ok := getFieldUpdateString(req.GetEmail); ok {
		updates["email"] = v
	}
	if v, ok := getFieldUpdateString(req.GetRemark); ok {
		updates["remark"] = v
	}

	if len(updates) == 0 {
		return nil, errNothingUpdate
	}

	// P0-04: Check phone uniqueness before update (still needed for UPDATE path).
	if newPhone, ok := updates["phone"].(string); ok && newPhone != "" {
		existing, err := s.userDal.GetByPhone(ctx, newPhone)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			logger.Errorf("check phone uniqueness failed: %v", err)
			return nil, errInternal
		}
		if existing != nil && existing.ID != req.GetId() {
			return nil, errPhoneExists
		}
	}

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

	// P2-05: Also clear Redis token on delete to invalidate sessions
	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, req.GetId())
	if err := s.rdb.Del(ctx, tokenKey); err != nil {
		logger.Warnf("delete user: failed to clear redis token (user_id=%d): %v", req.GetId(), err)
	}

	return &v1.DeleteUserResp{}, nil
}

func (s *UserServiceImpl) ListUser(ctx context.Context, req *v1.ListUserReq) (*v1.ListUserResp, error) {

	users, total, err := s.userDal.List(ctx, req.GetPage(), req.GetPageSize())
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
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	// MySQL duplicate key: Error 1062: Duplicate entry '...' for key '...'
	return strings.Contains(err.Error(), "Error 1062") ||
		strings.Contains(err.Error(), "Duplicate entry") ||
		strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// getFieldUpdateString returns the value and true if the proto field getter returns
// a non-zero string. This is a workaround for proto3's zero-value problem (P1-06).
// NOTE: This still can't distinguish "not sent" from "sent as empty string".
// For true zero-value support, the proto should use `optional` keyword or wrapper types.
func getFieldUpdateString(getter func() string) (string, bool) {
	v := getter()
	return v, v != ""
}

// getFieldUpdateInt32 returns the value and true if the proto field getter returns
// a non-zero int32.
func getFieldUpdateInt32(getter func() int32) (int32, bool) {
	v := getter()
	return v, v != 0
}

// parseInt64 parses a string to int64, returning 0 on failure.
func parseInt64(s string) int64 {
	var n int64
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int64(c-'0')
		} else {
			return 0
		}
	}
	return n
}

// Logout deletes the user's active token from Redis.
// Token validation is handled by the gateway; downstream receives user_id via metainfo.
func (s *UserServiceImpl) Logout(ctx context.Context, req *v1.LogoutReq) (resp *v1.LogoutResp, err error) {
	userIDStr, ok := metainfo.GetPersistentValue(ctx, "user_id")
	if !ok || userIDStr == "" {
		logger.Warn("logout: user_id not found in metainfo, request may bypass gateway", zap.String("user_id", userIDStr))
		return nil, errUnauthorized
	}

	tokenKey := fmt.Sprintf("%s%s", tokenPrefix, userIDStr)
	// Log out all sessions by deleting the key directly (single-device login).
	// P1-20: Log Redis Del errors instead of swallowing them.
	if err := s.rdb.Del(ctx, tokenKey); err != nil {
		logger.Warnf("logout: failed to delete redis token (user_id=%s): %v", userIDStr, err)
		// Still return success — the JWT will expire naturally
	}

	return &v1.LogoutResp{}, nil
}
