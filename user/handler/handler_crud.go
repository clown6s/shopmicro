package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"shopmicro/user/data"
	v1 "shopmicro/user/kitex_gen/api/user/v1"
	"shopmicro/user/module"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/logger"
)

// UpdateUser 更新调用者自身的个人资料字段。
// L2 修复：使用类型化的 data.UserUpdate 替代 map[string]interface{}。
func (s *UserServiceImpl) UpdateUser(ctx context.Context, req *v1.UpdateUserReq) (*v1.UpdateUserResp, error) {
	if req.GetId() <= 0 {
		return nil, errInvalidUserID
	}

	if err := s.checkOwnership(ctx, req.GetId()); err != nil {
		return nil, err
	}

	// L2 修复：构建类型化的更新结构体而非 map[string]interface{}
	var upd data.UserUpdate

	if req.RealName != nil {
		if len(req.GetRealName()) > 64 {
			return nil, newBiz(errcode.CodeUserParam, "realName too long (max 64)")
		}
		upd.SetRealName(req.GetRealName())
	}
	if req.Avatar != nil {
		if len(req.GetAvatar()) > 512 {
			return nil, newBiz(errcode.CodeUserParam, "avatar too long (max 512)")
		}
		upd.SetAvatar(req.GetAvatar())
	}
	if req.Gender != nil {
		g := int8(req.GetGender())
		if g < 0 || g > 3 {
			return nil, newBiz(errcode.CodeUserParam, "gender must be 0-3")
		}
		upd.SetGender(g)
	}
	if req.Age != nil {
		a := req.GetAge()
		if a < 0 || a > 150 {
			return nil, newBiz(errcode.CodeUserParam, "age must be 0-150")
		}
		upd.SetAge(int16(a))
	}
	if req.Phone != nil {
		v := req.GetPhone()
		if !phoneRegex.MatchString(v) {
			return nil, errPhoneFormat
		}
		upd.SetPhone(v)
	}
	if req.Email != nil {
		v := req.GetEmail()
		if v != "" && !emailRegex.MatchString(v) {
			return nil, errEmailFormat
		}
		if len(v) > 128 {
			return nil, newBiz(errcode.CodeUserParam, "email too long (max 128)")
		}
		upd.SetEmail(v)
	}
	if req.Remark != nil {
		if len(req.GetRemark()) > 500 {
			return nil, newBiz(errcode.CodeUserParam, "remark too long (max 500)")
		}
		upd.SetRemark(req.GetRemark())
	}

	updates := upd.Fields()
	if len(updates) == 0 {
		return nil, errNothingUpdate
	}

	// R5-P1-06: 先撤销旧 token（轻操作），再更新 DB（重操作）。
	// 原因：如果先更新 DB 再撤销 Redis，DB 成功但 Redis 失败时
	// 会出现"数据已变更但旧 token 仍有效"的不一致状态。
	// 反过来：Redis 失败直接返回错误（DB 未动），Redis 成功但 DB 失败
	// 仅导致用户需要重新登录（安全优先），数据保持不变。
	if upd.Phone != nil {
		tokenKey := fmt.Sprintf("%s%d", tokenPrefix, req.GetId())
		rtSetKey := fmt.Sprintf("%s%d", tokenSetKeyPrefix, req.GetId())
		if _, err := s.rdb.RunScript(ctx, revokeTokensScript, []string{tokenKey, rtSetKey}, refreshTokenPrefix); err != nil {
			logger.Warnf("update user: failed to revoke tokens before DB update (user_id=%d): %v", req.GetId(), err)
			return nil, errInternal
		}
	}

	if err := s.userDal.Update(ctx, req.GetId(), updates); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errUserNotFound
		}
		if isDuplicateKeyError(err) {
			if upd.Phone != nil {
				return nil, errPhoneExists
			}
			if upd.Email != nil {
				return nil, newBiz(errcode.CodeUserParam, "email already exists")
			}
			// 其他唯一约束冲突（如 future columns），记录原始错误便于排查
			logger.Warnf("update user: unexpected duplicate key (user_id=%d): %v", req.GetId(), err)
			return nil, newBiz(errcode.CodeUserParam, "duplicate key violation")
		}
		logger.Errorf("update user failed: %v", err)
		return nil, errInternal
	}

	return &v1.UpdateUserResp{}, nil
}

// DeleteUser 软删除调用者自身的账号。
func (s *UserServiceImpl) DeleteUser(ctx context.Context, req *v1.DeleteUserReq) (*v1.DeleteUserResp, error) {
	if req.GetId() <= 0 {
		return nil, errInvalidUserID
	}

	if err := s.checkOwnership(ctx, req.GetId()); err != nil {
		return nil, err
	}

	user, err := s.userDal.GetByID(ctx, req.GetId())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errUserNotFound
		}
		logger.Errorf("delete user: failed to get user (user_id=%d): %v", req.GetId(), err)
		return nil, errInternal
	}

	if err := s.userDal.Delete(ctx, req.GetId()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errUserNotFound
		}
		logger.Errorf("delete user failed: %v", err)
		return nil, errInternal
	}

	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, req.GetId())
	rtSetKey := fmt.Sprintf("%s%d", tokenSetKeyPrefix, req.GetId())
	// 软删除已提交 DB，Redis 撤销失败不返回错误（降级为 warning）。
	// 原因：返回错误会让客户端重试，但 GetByID 因 deleted_at!=nil 返回 "用户不存在"，
	// 造成更严重的困惑。旧 token 短暂有效远比 "用户不存在" 好处理。
	if _, err := s.rdb.RunScript(ctx, revokeTokensScript, []string{tokenKey, rtSetKey}, refreshTokenPrefix); err != nil {
		logger.Warnf("delete user: failed to revoke tokens (user_id=%d), user already deleted: %v", req.GetId(), err)
	}

	logger.Infof("user deleted: user_id=%d", req.GetId())

	failKey := fmt.Sprintf("%s%s", loginFailKeyPrefix, user.Phone)
	if err := s.rdb.Del(ctx, failKey); err != nil {
		logger.Warnf("user: failed to clear login failures on delete user (phone=%s): %v", maskPhone(user.Phone), err)
	}

	return &v1.DeleteUserResp{}, nil
}

// ListUser 分页列出所有用户并支持搜索。
// 仅通过 gateway 认证的管理员可调用。
// TODO: 等待 role 字段加入 JWT Claims 后，改为检查 claims.Role == "admin"。
func (s *UserServiceImpl) ListUser(ctx context.Context, req *v1.ListUserReq) (*v1.ListUserResp, error) {
	// 权限校验：ListUser 为管理员接口，至少要求通过 gateway 认证
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}

	// 校正分页参数，确保响应中返回实际使用的值
	page := req.GetPage()
	pageSize := req.GetPageSize()
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 20 {
		pageSize = 20
	}

	if name := req.GetName(); len(name) > 64 {
		return nil, newBizHTTP(errcode.CodeUserParam, "name too long (max 64)", 400)
	}
	if phone := req.GetPhone(); len(phone) > 20 {
		return nil, newBizHTTP(errcode.CodeUserParam, "phone too long (max 20)", 400)
	}

	users, total, err := s.userDal.List(ctx, page, pageSize, req.GetName(), req.GetPhone())
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
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	}, nil
}

// toUserInfoMasked 返回手机号和邮箱已脱敏的用户信息。
func toUserInfoMasked(u *module.User) *v1.UserInfo {
	gender := v1.Gender(u.Gender)
	age := int32(u.Age)
	return &v1.UserInfo{
		Id:       u.ID,
		RealName: u.RealName,
		Avatar:   u.Avatar,
		Gender:   &gender,
		Age:      &age,
		Phone:    maskPhone(u.Phone),
		Email:    maskEmail(u.Email),
		Remark:   u.Remark,
	}
}
