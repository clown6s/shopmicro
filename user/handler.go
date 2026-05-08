package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"shopmicro/user/data"
	v1 "shopmicro/user/kitex_gen/api/user/v1"
	"shopmicro/user/model"

	"shopmicro/pkg/jwt"
	"shopmicro/pkg/logger"
)

type UserServiceImpl struct {
	userDal   *data.UserDal
	jwtGen    *jwt.Generator
	jwtParser *jwt.Parser
	redisSet  func(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	redisDel  func(ctx context.Context, keys ...string) error
}

func newUserHandler(d *data.Data, jwtSecret string, jwtExpHours int) *UserServiceImpl {
	gen := jwt.NewGenerator(jwtSecret, time.Duration(jwtExpHours)*time.Hour)
	return &UserServiceImpl{
		userDal:   d.User(),
		jwtGen:    gen,
		jwtParser: jwt.NewParser(jwtSecret),
		redisSet: func(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
			return d.Rdb.Set(ctx, key, value, ttl).Err()
		},
		redisDel: func(ctx context.Context, keys ...string) error {
			return d.Rdb.Del(ctx, keys...).Err()
		},
	}
}

const tokenPrefix = "user:token:"

func (s *UserServiceImpl) RegisterUser(ctx context.Context, req *v1.RegisterUserReq) (resp *v1.RegisterUserResp, err error) {
	if req.Phone == "" {
		return &v1.RegisterUserResp{Code: 400, Message: "phone is required"}, nil
	}
	if req.Password == "" {
		return &v1.RegisterUserResp{Code: 400, Message: "password is required"}, nil
	}

	exist, _ := s.userDal.GetByPhone(ctx, req.Phone)
	if exist != nil {
		return &v1.RegisterUserResp{Code: 409, Message: "phone already exists"}, nil
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		logger.Errorf("bcrypt hash failed: %v", err)
		return &v1.RegisterUserResp{Code: 500, Message: "internal error"}, nil
	}

	user := &model.User{
		Password: string(hashed),
		RealName: req.RealName,
		Gender:   int8(req.Gender),
		Age:      int8(req.Age),
		Phone:    req.Phone,
		Email:    req.Email,
	}

	if err := s.userDal.Create(ctx, user); err != nil {
		logger.Errorf("create user failed: %v", err)
		return &v1.RegisterUserResp{Code: 500, Message: "internal error"}, nil
	}

	return &v1.RegisterUserResp{Code: 200, Message: "success"}, nil
}

func (s *UserServiceImpl) Login(ctx context.Context, req *v1.LoginReq) (resp *v1.LoginResp, err error) {
	if req.Phone == "" {
		return &v1.LoginResp{Code: 400, Message: "phone is required"}, nil
	}
	if req.Password == "" {
		return &v1.LoginResp{Code: 400, Message: "password is required"}, nil
	}

	user, err := s.userDal.GetByPhone(ctx, req.Phone)
	if err != nil {
		if err == sql.ErrNoRows {
			return &v1.LoginResp{Code: 401, Message: "phone or password incorrect"}, nil
		}
		logger.Errorf("login get user failed: %v", err)
		return &v1.LoginResp{Code: 500, Message: "internal error"}, nil
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		return &v1.LoginResp{Code: 401, Message: "phone or password incorrect"}, nil
	}

	token, err := s.jwtGen.Generate(user.ID, user.Phone)
	if err != nil {
		logger.Errorf("generate token failed: %v", err)
		return &v1.LoginResp{Code: 500, Message: "internal error"}, nil
	}

	// token 存 Redis
	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, user.ID)
	if err := s.redisSet(ctx, tokenKey, token, 24*time.Hour); err != nil {
		logger.Errorf("store token to redis failed: %v", err)
	}

	return &v1.LoginResp{
		Code:    200,
		Message: "success",
		Token:   token,
		Data:    toUserInfo(user),
	}, nil
}

func (s *UserServiceImpl) LoginOut(ctx context.Context, req *v1.LoginOutReq) (resp *v1.LoginOutResp, err error) {
	if req.Token != "" {
		claims, parseErr := s.jwtParser.Parse(req.Token)
		if parseErr == nil {
			tokenKey := fmt.Sprintf("%s%d", tokenPrefix, claims.UserID)
			_ = s.redisDel(ctx, tokenKey)
		}
	}
	return &v1.LoginOutResp{Code: 200, Message: "logged out"}, nil
}

func (s *UserServiceImpl) UpdateUser(ctx context.Context, req *v1.UpdateUserReq) (resp *v1.UpdateUserResp, err error) {
	if req.Id <= 0 {
		return &v1.UpdateUserResp{Code: 400, Message: "invalid user id"}, nil
	}

	updates := make(map[string]interface{})
	if req.RealName != "" {
		updates["real_name"] = req.RealName
	}
	if req.Avatar != "" {
		updates["avatar"] = req.Avatar
	}
	if req.Gender != 0 {
		updates["gender"] = int8(req.Gender)
	}
	if req.Age != 0 {
		updates["age"] = int8(req.Age)
	}
	if req.Phone != "" {
		updates["phone"] = req.Phone
	}
	if req.Email != "" {
		updates["email"] = req.Email
	}
	if req.Remark != "" {
		updates["remark"] = req.Remark
	}

	if len(updates) == 0 {
		return &v1.UpdateUserResp{Code: 400, Message: "nothing to update"}, nil
	}

	if err := s.userDal.Update(ctx, req.Id, updates); err != nil {
		if err == sql.ErrNoRows {
			return &v1.UpdateUserResp{Code: 404, Message: "user not found"}, nil
		}
		logger.Errorf("update user failed: %v", err)
		return &v1.UpdateUserResp{Code: 500, Message: "internal error"}, nil
	}

	return &v1.UpdateUserResp{Code: 200, Message: "success"}, nil
}

func (s *UserServiceImpl) DeleteUser(ctx context.Context, req *v1.DeleteUserReq) (resp *v1.DeleteUserResp, err error) {
	if req.Id <= 0 {
		return &v1.DeleteUserResp{Code: 400, Message: "invalid user id"}, nil
	}

	if err := s.userDal.Delete(ctx, req.Id); err != nil {
		if err == sql.ErrNoRows {
			return &v1.DeleteUserResp{Code: 404, Message: "user not found"}, nil
		}
		logger.Errorf("delete user failed: %v", err)
		return &v1.DeleteUserResp{Code: 500, Message: "internal error"}, nil
	}

	return &v1.DeleteUserResp{Code: 200, Message: "success"}, nil
}

func (s *UserServiceImpl) ListUser(ctx context.Context, req *v1.ListUserReq) (resp *v1.ListUserResp, err error) {
	users, total, err := s.userDal.List(ctx, req.Page, req.PageSize)
	if err != nil {
		logger.Errorf("list users failed: %v", err)
		return &v1.ListUserResp{Code: 500, Message: "internal error"}, nil
	}

	list := make([]*v1.UserInfo, 0, len(users))
	for _, u := range users {
		list = append(list, toUserInfo(u))
	}

	return &v1.ListUserResp{
		Code:     200,
		Message:  "success",
		Data:     list,
		Page:     req.Page,
		PageSize: req.PageSize,
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
