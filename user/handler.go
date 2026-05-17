package main

import (
	"context"
	v1 "shopmicro/user/kitex_gen/api/user/v1"
)

// UserServiceImpl implements the last service interface defined in the IDL.
type UserServiceImpl struct{}

// RegisterUser implements the UserServiceImpl interface.
func (s *UserServiceImpl) RegisterUser(ctx context.Context, req *v1.RegisterUserReq) (resp *v1.RegisterUserResp, err error) {
	// TODO: Your code here...
	return
}

// Login implements the UserServiceImpl interface.
func (s *UserServiceImpl) Login(ctx context.Context, req *v1.LoginReq) (resp *v1.LoginResp, err error) {
	// TODO: Your code here...
	return
}

// RefreshToken implements the UserServiceImpl interface.
func (s *UserServiceImpl) RefreshToken(ctx context.Context, req *v1.RefreshTokenReq) (resp *v1.RefreshTokenResp, err error) {
	// TODO: Your code here...
	return
}

// Logout implements the UserServiceImpl interface.
func (s *UserServiceImpl) Logout(ctx context.Context, req *v1.LogoutReq) (resp *v1.LogoutResp, err error) {
	// TODO: Your code here...
	return
}

// UpdateUser implements the UserServiceImpl interface.
func (s *UserServiceImpl) UpdateUser(ctx context.Context, req *v1.UpdateUserReq) (resp *v1.UpdateUserResp, err error) {
	// TODO: Your code here...
	return
}

// DeleteUser implements the UserServiceImpl interface.
func (s *UserServiceImpl) DeleteUser(ctx context.Context, req *v1.DeleteUserReq) (resp *v1.DeleteUserResp, err error) {
	// TODO: Your code here...
	return
}

// ListUser implements the UserServiceImpl interface.
func (s *UserServiceImpl) ListUser(ctx context.Context, req *v1.ListUserReq) (resp *v1.ListUserResp, err error) {
	// TODO: Your code here...
	return
}
