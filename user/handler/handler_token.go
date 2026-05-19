package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bytedance/gopkg/cloud/metainfo"

	v1 "shopmicro/user/kitex_gen/api/user/v1"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/logger"
)

// getMeta 从 gRPC 上下文中获取持久化的元数据值。
func getMeta(ctx context.Context, key string) (string, bool) {
	return metainfo.GetPersistentValue(ctx, key)
}

// Logout 从 Redis 中移除调用者的特定令牌。
func (s *UserServiceImpl) Logout(ctx context.Context, req *v1.LogoutReq) (resp *v1.LogoutResp, err error) {
	userIDStr, ok := getMeta(ctx, "user_id")
	if !ok || userIDStr == "" {
		logger.Warn("logout: user_id not found in metainfo, request may bypass gateway")
		return nil, errUnauthorized
	}

	tokenStr, ok := getMeta(ctx, "token")
	if !ok || tokenStr == "" {
		logger.Warn("logout: token not found in metainfo, request may bypass gateway")
		return nil, errUnauthorized
	}

	userID, err := parseInt64(userIDStr)
	if err != nil || userID <= 0 {
		logger.Warnf("logout: invalid user_id in metainfo: %s", userIDStr)
		return nil, errUnauthorized
	}

	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, userID)
	rtSetKey := fmt.Sprintf("%s%d", "shopmicro:user:rtset:", userID)
	// 同时撤销 access token 和 refresh token
	if _, err := s.rdb.RunScript(ctx, revokeTokensScript, []string{tokenKey, rtSetKey}); err != nil {
		logger.Errorf("logout: failed to revoke tokens atomically (user_id=%d): %v", userID, err)
		return nil, errInternal
	}

	return &v1.LogoutResp{}, nil
}

var errRefreshTokenRequired = newBizHTTP(errcode.CodeUserTokenRequired, "refresh token is required", 401)
var errRefreshTokenInvalid = newBizHTTP(errcode.CodeUserTokenInvalid, "invalid or expired refresh token", 401)

// refreshTokensScript 原子性地轮换刷新令牌 + 存储新 access token。
// KEYS[1]=rtSetKey, KEYS[2]=newRTKey, KEYS[3]=tokenKey
const refreshTokensScript = `
redis.call('SREM', KEYS[1], ARGV[1])
redis.call('SADD', KEYS[1], ARGV[2])
redis.call('SET', KEYS[2], ARGV[3], 'EX', ARGV[5])
redis.call('EXPIRE', KEYS[1], ARGV[4])
redis.call('SADD', KEYS[3], ARGV[6])
redis.call('EXPIRE', KEYS[3], ARGV[7])
return 1
`

// RefreshToken 签发新的访问令牌 + 刷新令牌对。
func (s *UserServiceImpl) RefreshToken(ctx context.Context, req *v1.RefreshTokenReq) (resp *v1.RefreshTokenResp, err error) {
	rt := req.GetRefreshToken()
	if rt == "" {
		return nil, errRefreshTokenRequired
	}

	// 原子消费旧 refresh token（防重放）。
	// 权衡：GetDel 后如果进程崩溃，旧 RT 已消费但新 AT/RT 未写入，用户需重新登录。
	// 这是标准的 token rotation 安全设计（RFC 6749 §6）。
	rtKey := fmt.Sprintf("%s%s", refreshTokenPrefix, rt)
	userIDStr, err := s.rdb.GetDel(ctx, rtKey)
	if err != nil {
		// R5-P1-02: Redis 故障时记录日志，区分网络错误和 token 不存在
		logger.Warnf("refresh token: redis GetDel failed: %v", err)
		return nil, errRefreshTokenInvalid
	}
	if userIDStr == "" {
		return nil, errRefreshTokenInvalid
	}

	userID, err := parseInt64(userIDStr)
	if err != nil || userID <= 0 {
		// R4-P1-04: 记录异常 userIDStr，便于排查数据一致性问题
		if userIDStr != "" {
			logger.Warnf("refresh token: invalid userID from Redis (raw=%q, err=%v)", userIDStr, err)
		}
		return nil, errRefreshTokenInvalid
	}

	// P2-07：按用户 ID 限制刷新令牌频率（每分钟 10 次）
	// R7-P2-04: 使用 fmt.Sprintf("%d", userID) 而非原始 userIDStr，
	// 确保 key 一致性（避免 Redis 中存储非规范化值如前导零）
	refreshRateKey := refreshRateLimitKey + fmt.Sprintf("%d", userID)
	limited, rlErr := s.rdb.RunScript(ctx, rateLimitScript, []string{refreshRateKey}, s.sec.RefreshLimitMax, s.sec.RefreshLimitWindow)
	if rlErr != nil {
		logger.Warnf("refresh token: rate limit check failed (user_id=%d): %v", userID, rlErr)
		return nil, errRefreshTooMany
	} else if limited != nil {
		if result, ok := limited.(int64); ok && result == 1 {
			return nil, errRefreshTooMany
		}
	}

	// R4-P1-03：验证用户仍然存在且未被删除
	if _, err := s.userDal.GetByID(ctx, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errRefreshTokenInvalid
		}
		logger.Errorf("refresh token: check user existence failed (user_id=%d): %v", userID, err)
		return nil, errInternal
	}

	token, err := s.jwtGen.Generate(userID)
	if err != nil {
		logger.Errorf("refresh token: generate new access token failed: %v", err)
		return nil, errInternal
	}

	newRT, err := s.rtGen.Generate(userID)
	if err != nil {
		logger.Errorf("refresh token: generate new refresh token failed: %v", err)
		return nil, errInternal
	}

	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, userID)
	atTTL := time.Duration(s.jwtExpHrs) * time.Hour
	rtSetKey := fmt.Sprintf("%s%d", "shopmicro:user:rtset:", userID)
	newRTKey := fmt.Sprintf("%s%s", refreshTokenPrefix, newRT)
	rtTTL := time.Duration(s.rtExpHrs) * time.Hour
	// 原子性：AT 写入 + RT 轮换 在同一个 Lua 脚本中完成
	if _, err := s.rdb.RunScript(ctx, refreshTokensScript,
		[]string{rtSetKey, newRTKey, tokenKey},
		rt, newRT, fmt.Sprintf("%d", userID), int(rtTTL.Seconds()), int(rtTTL.Seconds()), token, int(atTTL.Seconds()),
	); err != nil {
		logger.Errorf("refresh token: atomic token rotation failed (user_id=%d): %v", userID, err)
		return nil, errInternal
	}

	return &v1.RefreshTokenResp{
		Token:        token,
		RefreshToken: newRT,
	}, nil
}
