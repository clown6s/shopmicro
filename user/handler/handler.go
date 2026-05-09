package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"shopmicro/user/data"
	v1 "shopmicro/user/kitex_gen/api/user/v1"
	"shopmicro/user/model"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/jwt"
	"shopmicro/pkg/logger"
)

func newBiz(code uint32, msg string) *errcode.BizError { return errcode.NewBizError(code, msg) }

var (
	errPhoneRequired    = newBiz(errcode.CodeUserPhoneRequired, "phone is required")
	errPasswordRequired = newBiz(errcode.CodeUserPwdRequired, "password is required")
	errPhoneFormat      = newBiz(errcode.CodeUserPhoneFormat, "invalid phone format")
	errPasswordWeak     = newBiz(errcode.CodeUserPwdWeak, "password must be at least 6 characters")
	errPhoneExists      = newBiz(errcode.CodeUserPhoneExists, "phone already exists")
	errInternal         = newBiz(errcode.CodeUserInternal, "internal error")
	errPhonePwdWrong    = newBiz(errcode.CodeUserPhonePwdWrong, "phone or password incorrect")
	errInvalidUserID    = newBiz(errcode.CodeUserInvalidID, "invalid user id")
	errNothingUpdate    = newBiz(errcode.CodeUserNothingUpdate, "nothing to update")
	errTokenRequired    = newBiz(errcode.CodeUserTokenRequired, "token is required")
	errTokenInvalid     = newBiz(errcode.CodeUserTokenInvalid, "invalid token")
	errTokenMismatch    = newBiz(errcode.CodeUserTokenMismatch, "token mismatch, please login again")
	errUserNotFound     = newBiz(errcode.CodeUserNotFound, "user not found")
)

type UserServiceImpl struct {
	userDal   *data.UserDal
	jwtGen    *jwt.Generator
	jwtParser *jwt.Parser
	jwtExpHrs int
	redisSet  func(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	redisGet  func(ctx context.Context, key string) (string, error)
	redisDel  func(ctx context.Context, keys ...string) error
}

func NewUserHandler(d *data.Data, jwtSecret string, jwtExpHours int) *UserServiceImpl {
	gen, err := jwt.NewGenerator(jwtSecret, time.Duration(jwtExpHours)*time.Hour)
	if err != nil {
		logger.Fatalf("init jwt generator failed: %v", err)
	}
	return &UserServiceImpl{
		userDal:   d.User(),
		jwtGen:    gen,
		jwtParser: jwt.NewParser(jwtSecret),
		jwtExpHrs: jwtExpHours,
		redisSet: func(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
			return d.Rdb.Set(ctx, key, value, ttl).Err()
		},
		redisGet: func(ctx context.Context, key string) (string, error) {
			return d.Rdb.Get(ctx, key).Result()
		},
		redisDel: func(ctx context.Context, keys ...string) error {
			return d.Rdb.Del(ctx, keys...).Err()
		},
	}
}

const tokenPrefix = "user:token:"

func (s *UserServiceImpl) RegisterUser(ctx context.Context, req *v1.RegisterUserReq) (*v1.RegisterUserResp, error) {
	if req.GetPhone() == "" {
		return nil, errPhoneRequired
	}
	if req.GetPassword() == "" {
		return nil, errPasswordRequired
	}
	if len(req.GetPhone()) != 11 {
		return nil, errPhoneFormat
	}
	if len(req.GetPassword()) < 6 {
		return nil, errPasswordWeak
	}

	exist, err := s.userDal.GetByPhone(ctx, req.GetPhone())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logger.Errorf("check phone exists failed: %v", err)
		return nil, errInternal
	}
	if exist != nil {
		return nil, errPhoneExists
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.GetPassword()), bcrypt.DefaultCost)
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

	if err := s.userDal.Create(ctx, user); err != nil {
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

	// token 存 Redis
	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, user.ID)
	if err := s.redisSet(ctx, tokenKey, token, time.Duration(s.jwtExpHrs)*time.Hour); err != nil {
		logger.Errorf("store token to redis failed: %v", err)
		// Redis 写入失败不阻塞登录，但 token 可能无法被主动失效
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

func (s *UserServiceImpl) UpdateUser(ctx context.Context, req *v1.UpdateUserReq) (*v1.UpdateUserResp, error) {
	if req.GetId() <= 0 {
		return nil, errInvalidUserID
	}

	updates := make(map[string]interface{})
	if req.GetRealName() != "" {
		updates["real_name"] = req.GetRealName()
	}
	if req.GetAvatar() != "" {
		updates["avatar"] = req.GetAvatar()
	}
	if req.GetGender() != 0 {
		updates["gender"] = int8(req.GetGender())
	}
	if req.GetAge() != 0 {
		updates["age"] = int8(req.GetAge())
	}
	if req.GetPhone() != "" {
		updates["phone"] = req.GetPhone()
	}
	if req.GetEmail() != "" {
		updates["email"] = req.GetEmail()
	}
	if req.GetRemark() != "" {
		updates["remark"] = req.GetRemark()
	}

	if len(updates) == 0 {
		return nil, errNothingUpdate
	}

	if err := s.userDal.Update(ctx, req.GetId(), updates); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errUserNotFound
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

	if err := s.userDal.Delete(ctx, req.GetId()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errUserNotFound
		}
		logger.Errorf("delete user failed: %v", err)
		return nil, errInternal
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

// Logout implements the UserServiceImpl interface.
func (s *UserServiceImpl) Logout(ctx context.Context, req *v1.LogoutReq) (resp *v1.LogoutResp, err error) {
	if req.GetToken() == "" {
		return nil, errTokenRequired
	}

	claims, parseErr := s.jwtParser.Parse(req.GetToken())
	if parseErr != nil {
		return nil, errTokenInvalid
	}

	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, claims.UserID)
	storedToken, _ := s.redisGet(ctx, tokenKey)
	if storedToken != req.GetToken() {
		return nil, errTokenMismatch
	}

	if err := s.redisDel(ctx, tokenKey); err != nil {
		logger.Errorf("delete token from redis failed: %v", err)
	}

	return &v1.LogoutResp{}, nil
}
