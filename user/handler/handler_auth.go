package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	v1 "shopmicro/user/kitex_gen/api/user/v1"
	"shopmicro/user/model"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/logger"
)

// RegisterUser handles user registration with phone + password.
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
		return nil, errRegisterTooMany
	} else if limited != nil {
		if result, ok := limited.(int64); ok && result == 1 {
			return nil, errRegisterTooMany
		}
	}

	if !phoneRegex.MatchString(req.GetPhone()) {
		return nil, errPhoneFormat
	}
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

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.GetPassword()), 12)
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

	if user.Gender < 0 || user.Gender > 3 {
		return nil, newBiz(errcode.CodeUserParam, "gender must be 0-3")
	}
	if req.GetAge() < 0 || req.GetAge() > 150 {
		return nil, newBiz(errcode.CodeUserParam, "age must be 0-150")
	}

	if err := s.userDal.Create(ctx, user); err != nil {
		if isDuplicateKeyError(err) {
			return nil, errPhoneExists
		}
		logger.Errorf("create user failed: %v", err)
		return nil, errInternal
	}

	s.clearLoginFailures(ctx, req.GetPhone())

	return &v1.RegisterUserResp{}, nil
}

// Login authenticates a user by phone and password, returns tokens.
func (s *UserServiceImpl) Login(ctx context.Context, req *v1.LoginReq) (*v1.LoginResp, error) {
	if req.GetPhone() == "" {
		return nil, errPhoneRequired
	}
	if req.GetPassword() == "" {
		return nil, errPasswordRequired
	}
	if !phoneRegex.MatchString(req.GetPhone()) {
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$12$000000000000000000000000000000000000000000000000000000"), []byte(req.GetPassword()))
		return nil, errPhonePwdWrong
	}

	if err := s.checkLoginAttempts(ctx, req.GetPhone()); err != nil {
		return nil, err
	}

	user, err := s.userDal.GetByPhone(ctx, req.GetPhone())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			_ = bcrypt.CompareHashAndPassword([]byte("$2a$12$000000000000000000000000000000000000000000000000000000"), []byte(req.GetPassword()))
			logger.Warnf("login failed: phone not found (%s)", maskPhone(req.GetPhone()))
			s.recordLoginFailure(ctx, req.GetPhone())
			return nil, errPhonePwdWrong
		}
		logger.Errorf("login get user failed: %v", err)
		return nil, errInternal
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.GetPassword())); err != nil {
		logger.Warnf("login failed: wrong password (phone: %s)", maskPhone(req.GetPhone()))
		s.recordLoginFailure(ctx, req.GetPhone())
		return nil, errPhonePwdWrong
	}

	s.clearLoginFailures(ctx, req.GetPhone())

	token, err := s.jwtGen.Generate(user.ID)
	if err != nil {
		logger.Errorf("generate token failed: %v", err)
		return nil, errInternal
	}

	refreshToken, err := s.rtGen.Generate(user.ID)
	if err != nil {
		logger.Errorf("generate refresh token failed: %v", err)
		return nil, errInternal
	}

	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, user.ID)
	ttl := time.Duration(s.jwtExpHrs) * time.Hour
	if err := s.rdb.SAddExpire(ctx, tokenKey, ttl, token); err != nil {
		logger.Errorf("login: store token to redis failed: %v", err)
		return nil, errInternal
	}

	rtKey := fmt.Sprintf("%s%s", refreshTokenPrefix, refreshToken)
	rtTTL := time.Duration(s.rtExpHrs) * time.Hour
	if err := s.rdb.Set(ctx, rtKey, user.ID, rtTTL); err != nil {
		logger.Errorf("login: store refresh token to redis failed: %v", err)
		return nil, errInternal
	}

	rtSetKey := fmt.Sprintf("%s%d", "shopmicro:user:rtset:", user.ID)
	if err := s.rdb.SAddExpire(ctx, rtSetKey, rtTTL, refreshToken); err != nil {
		logger.Errorf("login: store refresh token to reverse set failed: %v", err)
	}

	return &v1.LoginResp{
		// L3 fix: Return masked user ID instead of 0.
		Id:           maskUserID(user.ID),
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
