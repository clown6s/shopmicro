package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	v1 "shopmicro/user/kitex_gen/api/user/v1"
	"shopmicro/user/module"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/logger"
)

// timingDummyHash 是用于时序攻击缓解的随机 bcrypt 哈希。
// 在包初始化时生成，确保每次启动使用不同的哈希值，
// 防止攻击者通过固定哈希的确定性计时签名推断手机号是否存在。
var timingDummyHash []byte

// R4-P1-05: loginTokensScript 原子性地写入 access token、refresh token 和反向集合。
// KEYS[1]=tokenKey, KEYS[2]=rtKey, KEYS[3]=rtSetKey
// ARGV[1]=token, ARGV[2]=refreshToken, ARGV[3]=userID, ARGV[4]=atTTL(sec), ARGV[5]=rtTTL(sec)
const loginTokensScript = `
redis.call('SADD', KEYS[1], ARGV[1])
redis.call('EXPIRE', KEYS[1], ARGV[4])
redis.call('SET', KEYS[2], ARGV[3], 'EX', ARGV[5])
redis.call('SADD', KEYS[3], ARGV[2])
redis.call('EXPIRE', KEYS[3], ARGV[5])
return 1
`

func init() {
	// 生成随机 32 字节作为 dummy 密码，使 bcrypt 比较时间与真实哈希一致
	dummyPwd := make([]byte, 32)
	if _, err := rand.Read(dummyPwd); err != nil {
		// crypto/rand 失败是致命错误（意味着 OS CSPRNG 不可用）
		// 不暴露 err.Error() 到 panic 信息，防止泄露内部实现路径
		logger.Errorf("crypto/rand init failed: %v", err)
		panic("security: crypto/rand init failed, see server logs")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(base64.StdEncoding.EncodeToString(dummyPwd)), 12)
	if err != nil {
		logger.Errorf("bcrypt timing dummy generation failed: %v", err)
		panic("security: init failed, see server logs")
	}
	timingDummyHash = hash
}

// RegisterUser 处理用户注册（手机号 + 密码）。
func (s *UserServiceImpl) RegisterUser(ctx context.Context, req *v1.RegisterUserReq) (*v1.RegisterUserResp, error) {
	if req.GetPhone() == "" {
		return nil, errPhoneRequired
	}
	if req.GetPassword() == "" {
		return nil, errPasswordRequired
	}

	if !phoneRegex.MatchString(req.GetPhone()) {
		return nil, errPhoneFormat
	}

	// P2-06：按手机号限制注册频率
	rateKey := registerRateLimitKey + req.GetPhone()
	limited, err := s.rdb.RunScript(ctx, rateLimitScript, []string{rateKey}, registerRateLimitMax, registerRateLimitWindowSec)
	if err != nil {
		logger.Warnf("register: rate limit check failed (phone=%s): %v", maskPhone(req.GetPhone()), err)
		return nil, errRegisterTooMany
	} else if limited != nil {
		if result, ok := limited.(int64); ok && result == 1 {
			return nil, errRegisterTooMany
		}
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

	// R4-P2-09: Gender/Age 校验在 bcrypt 之前，避免对无效请求执行昂贵的 bcrypt 计算
	if req.GetGender() < 0 || req.GetGender() > 3 {
		return nil, newBiz(errcode.CodeUserParam, "gender must be 0-3")
	}
	if req.GetAge() < 0 || req.GetAge() > 150 {
		return nil, newBiz(errcode.CodeUserParam, "age must be 0-150")
	}

	// 在 bcrypt 之前先用 SHA-256 预哈希，避免 72 字节截断限制。
	// bcrypt 只使用密码的前 72 个字节，因此超过 72 字节的密码
	// 如果前缀相同会产生相同的哈希值。
	preHash := sha256.Sum256([]byte(req.GetPassword()))
	hashed, err := bcrypt.GenerateFromPassword(preHash[:], 12)
	if err != nil {
		logger.Errorf("bcrypt hash failed: %v", err)
		return nil, errInternal
	}

	user := &module.User{
		Password: string(hashed),
		RealName: req.GetRealName(),
		Gender:   int8(req.GetGender()),
		Age:      int16(req.GetAge()),
		Phone:    req.GetPhone(),
		Email:    req.GetEmail(),
	}

	if err := s.userDal.Create(ctx, user); err != nil {
		if isDuplicateKeyError(err) {
			// 返回通用的"手机号已存在"错误，不泄露登录失败计数器信息
			return nil, errPhoneExists
		}
		logger.Errorf("create user failed: %v", err)
		return nil, errInternal
	}

	return &v1.RegisterUserResp{}, nil
}

// Login 通过手机号和密码进行身份认证，返回令牌。
func (s *UserServiceImpl) Login(ctx context.Context, req *v1.LoginReq) (*v1.LoginResp, error) {
	if req.GetPhone() == "" {
		return nil, errPhoneRequired
	}
	if req.GetPassword() == "" {
		return nil, errPasswordRequired
	}
	if !phoneRegex.MatchString(req.GetPhone()) {
		// Timing attack mitigation: 对无效手机号也执行一次 bcrypt 比较，
		// 使其响应时间与"手机号不存在"的情况一致，防止通过响应时间差异
		// 推断手机号是否存在于数据库中。
		_ = bcrypt.CompareHashAndPassword(timingDummyHash, []byte(req.GetPassword()))
		return nil, errPhonePwdWrong
	}

	if err := s.checkLoginAttempts(ctx, req.GetPhone()); err != nil {
		return nil, err
	}

	user, err := s.userDal.GetByPhone(ctx, req.GetPhone())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Timing attack mitigation: 手机号不存在时也执行 bcrypt，
			// 使其响应时间与"密码错误"的情况一致。
			_ = bcrypt.CompareHashAndPassword(timingDummyHash, []byte(req.GetPassword()))
			logger.Warnf("login failed: phone not found (%s)", maskPhone(req.GetPhone()))
			s.recordLoginFailure(ctx, req.GetPhone())
			return nil, errPhonePwdWrong
		}
		logger.Errorf("login get user failed: %v", err)
		return nil, errInternal
	}

	preHash := sha256.Sum256([]byte(req.GetPassword()))
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), preHash[:]); err != nil {
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
	atTTL := time.Duration(s.jwtExpHrs) * time.Hour
	rtKey := fmt.Sprintf("%s%s", refreshTokenPrefix, refreshToken)
	rtTTL := time.Duration(s.rtExpHrs) * time.Hour
	rtSetKey := fmt.Sprintf("%s%d", "shopmicro:user:rtset:", user.ID)

	// R4-P1-05: 原子化写入 access token + refresh token + 反向集合，避免中间状态
	if _, err := s.rdb.RunScript(ctx, loginTokensScript,
		[]string{tokenKey, rtKey, rtSetKey},
		token, refreshToken, user.ID, int(atTTL.Seconds()), int(rtTTL.Seconds()),
	); err != nil {
		logger.Errorf("login: atomic token write failed (user_id=%d): %v", user.ID, err)
		return nil, errInternal
	}

	gender := v1.Gender(user.Gender)
	age := int32(user.Age)
	return &v1.LoginResp{
		Id:           user.ID,
		RealName:     user.RealName,
		Avatar:       user.Avatar,
		Gender:       &gender,
		Age:          &age,
		Phone:        maskPhone(user.Phone),
		Email:        maskEmail(user.Email),
		Remark:       user.Remark,
		Token:        token,
		RefreshToken: refreshToken,
	}, nil
}
