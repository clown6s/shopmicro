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
	"shopmicro/pkg/redis"
)

// getMeta retrieves a persistent metadata value from the gRPC context.
func getMeta(ctx context.Context, key string) (string, bool) {
	return metainfo.GetPersistentValue(ctx, key)
}

// Logout removes the caller's specific token from Redis.
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
	if err := s.rdb.SRem(ctx, tokenKey, tokenStr); err != nil {
		logger.Errorf("logout: failed to remove token from redis (user_id=%d): %v", userID, err)
		return nil, errInternal
	}

	return &v1.LogoutResp{}, nil
}

var errRefreshTokenRequired = newBiz(errcode.CodeUserTokenRequired, "refresh token is required")
var errRefreshTokenInvalid = newBiz(errcode.CodeUserTokenInvalid, "invalid or expired refresh token")

// refreshTokensLua atomically rotates refresh tokens.
var refreshTokensLua = redis.NewScript(`
redis.call('SREM', KEYS[1], ARGV[1])
redis.call('SADD', KEYS[1], ARGV[2])
redis.call('SET', KEYS[2], ARGV[3], 'EX', ARGV[5])
redis.call('EXPIRE', KEYS[1], ARGV[4])
return 1
`)

// RefreshToken issues a new access token + refresh token pair.
func (s *UserServiceImpl) RefreshToken(ctx context.Context, req *v1.RefreshTokenReq) (resp *v1.RefreshTokenResp, err error) {
	rt := req.GetRefreshToken()
	if rt == "" {
		return nil, errRefreshTokenRequired
	}

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
		return nil, errRefreshTooMany
	} else if limited != nil {
		if result, ok := limited.(int64); ok && result == 1 {
			return nil, errRefreshTooMany
		}
	}

	// R4-P1-03: Verify user still exists and is not deleted
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
	ttl := time.Duration(s.jwtExpHrs) * time.Hour
	if err := s.rdb.SAddExpire(ctx, tokenKey, ttl, token); err != nil {
		logger.Errorf("refresh token: store new access token failed: %v", err)
		return nil, errInternal
	}

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
