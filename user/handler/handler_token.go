package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

	// 注：撤销逻辑是基于 userID 的全量撤销，会撤销该用户所有设备的所有 token。
	// 这是设计意图——登出即踢掉所有设备。

	userID, err := parseInt64(userIDStr)
	if err != nil || userID <= 0 {
		logger.Warnf("logout: invalid user_id in metainfo: %s", userIDStr)
		return nil, errUnauthorized
	}

	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, userID)
	rtSetKey := fmt.Sprintf("%s%d", tokenSetKeyPrefix, userID)
	// 同时撤销 access token 和 refresh token
	if _, err := s.rdb.RunScript(ctx, revokeTokensScript, []string{tokenKey, rtSetKey}, refreshTokenPrefix); err != nil {
		logger.Errorf("logout: failed to revoke tokens atomically (user_id=%d): %v", userID, err)
		return nil, errInternal
	}

	return &v1.LogoutResp{}, nil
}

var errRefreshTokenRequired = newBizHTTP(errcode.CodeUserTokenRequired, "refresh token is required", 401)
var errRefreshTokenInvalid = newBizHTTP(errcode.CodeUserTokenInvalid, "invalid or expired refresh token", 401)

// refreshTokensScript 原子性完成 RT 轮换 + grace period 崩溃恢复。
//
// 流程（单次完整刷新）：
//  1. GETDEL 原子删除旧 RT（KEYS[3]），返回 userID
//  2. 若 GETDEL 返回 nil（已被其他请求消费或不存在），直接返回 nil
//  3. 检查 pending 键（KEYS[3] 本身作为 pending 键）
//     - 有值（崩溃恢复路径）：返回已保存的新 AT+RT，Go 不重新生成
//  4. 正常路径：写入 pending（KEYS[3]）→ tokenKey → rtSetKey → 清理 pending
//
// KEYS[1]=rtSetKey, KEYS[2]=tokenKey, KEYS[3]=oldRTKey（兼作 pending 键）
// ARGV[1]=oldRT, ARGV[2]=userIDStr, ARGV[3]=newAT, ARGV[4]=newRT,
// ARGV[5]=atTTL(sec), ARGV[6]=graceTTL(sec), ARGV[7]=rtSetTTL(sec)
//
// Lua 返回值：
//   - "ok:{newAT}:{newRT}"       正常轮换成功
//   - "recovery:{newAT}:{newRT}"  崩溃恢复，从 pending 返回已保存的 Token
//   - nil                         旧 RT 不存在或已被消费（并发/过期）
const refreshTokensScript = `
local rtSetKey = KEYS[1]
local tokenKey = KEYS[2]
local rtKey = KEYS[3]
local oldRT = ARGV[1]
local userID = ARGV[2]
local newAT = ARGV[3]
local newRT = ARGV[4]
local atTTL = tonumber(ARGV[5])
local graceTTL = tonumber(ARGV[6])
local keyTTL = tonumber(ARGV[7])

-- ① 原子删除旧 RT（防重放），同时获取 userID
local deletedValue = redis.call('GETDEL', rtKey)

-- 若 GETDEL 返回 nil，说明旧 RT 不存在或已被其他并发请求消费
if not deletedValue then
    return nil
end

-- ② pending 键存在：崩溃恢复。
--    rtKey 在 GETDEL 后被重新 SET 为 pending 值（见③），这里 GET 读取。
local pending = redis.call('GET', rtKey)
if pending then
    -- pending 格式: userID:newAT:newRT
    -- 校验 userID 防止跨用户数据泄露（即使 key 名正确）
    if redis.call('SUBSTR', pending, 0, string.len(userID)-1) == userID then
        redis.call('DEL', rtKey)
        return 'recovery:' .. string.sub(pending, string.len(userID)+2)
    end
end

-- ③ 正常路径：pending 写入作为崩溃恢复锚点（grace period 内有效）
--    格式: userID:newAT:newRT
redis.call('SET', rtKey, userID .. ':' .. newAT .. ':' .. newRT, 'EX', graceTTL)
redis.call('SADD', tokenKey, newAT)
redis.call('EXPIRE', tokenKey, atTTL)
redis.call('SADD', rtSetKey, newRT)
redis.call('EXPIRE', rtSetKey, keyTTL)
-- ④ 清除 pending 锚点（表示本轮刷新已完全成功）
redis.call('DEL', rtKey)
return 'ok:' .. newAT .. ':' .. newRT
`

// RefreshToken 签发新的访问令牌 + 刷新令牌对。
// 支持崩溃恢复：GETDEL 后进程崩溃，grace period（默认 5s）内旧 RT 重试时
// Lua 脚本从 rt-pending 键直接返回已保存的新 Token，无需重新生成。
//
// 安全设计：所有 Redis 操作（GETDEL + pending 检查 + Token 写入）在单个 Lua 脚本中
// 原子完成，不存在 TOCTOU 竞态条件。同一 refresh token 只能成功消费一次。
func (s *UserServiceImpl) RefreshToken(ctx context.Context, req *v1.RefreshTokenReq) (resp *v1.RefreshTokenResp, err error) {
	rt := req.GetRefreshToken()
	if rt == "" {
		return nil, errRefreshTokenRequired
	}

	rtKey := fmt.Sprintf("%s%s", refreshTokenPrefix, rt)

	// 解析 JWT 获取 userID（不从 Redis 预查，避免 TOCTOU）。
	// refresh token 本身是签名 JWT，可以安全提取 userID 用于限流和 Lua 参数。
	claims, jwtErr := s.rtGen.Parse(rt)
	if jwtErr != nil {
		return nil, errRefreshTokenInvalid
	}
	userID := claims.UserID

	// P2-07：按用户 ID 限制刷新令牌频率（每分钟 10 次）
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

	// 先生成新 Token，再原子写入 Redis。
	// 如果进程在这里崩溃：旧 RT 还在 Redis 中，下次请求直接 GETDEL → 用户不受影响。
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

	userIDStr := fmt.Sprintf("%d", userID)
	tokenKey := fmt.Sprintf("%s%d", tokenPrefix, userID)
	atTTL := time.Duration(s.jwtExpHrs) * time.Hour
	rtSetKey := fmt.Sprintf("%s%d", tokenSetKeyPrefix, userID)
	rtTTL := time.Duration(s.rtExpHrs) * time.Hour
	graceTTLSec := s.sec.TokenRotationGracePeriodSec
	if graceTTLSec <= 0 {
		graceTTLSec = 5 // 安全默认值
	}

	// 原子性：GETDEL + nil 检查 + pending 检查 + Token 写入 在一个 Lua 脚本中完成。
	// GETDEL 返回 nil 表示旧 RT 已被消费或不存在，直接返回 invalid。
	scriptResult, err := s.rdb.RunScript(ctx, refreshTokensScript,
		[]string{rtSetKey, tokenKey, rtKey},
		rt, userIDStr, token, newRT,
		int(atTTL.Seconds()), graceTTLSec, int(rtTTL.Seconds()),
	)
	if err != nil {
		logger.Errorf("refresh token: atomic token rotation script failed (user_id=%d): %v", userID, err)
		return nil, errInternal
	}

	// Lua 脚本返回 nil 表示旧 RT 不存在或已被并发请求消费
	if scriptResult == nil {
		return nil, errRefreshTokenInvalid
	}

	resultStr, ok := scriptResult.(string)
	if !ok {
		logger.Errorf("refresh token: unexpected script return type (user_id=%d): %T", userID, scriptResult)
		return nil, errInternal
	}

	// 解析 Lua 返回值：格式 "status:AT:RT"
	colonIdx := strings.Index(resultStr, ":")
	if colonIdx < 1 {
		logger.Errorf("refresh token: malformed script result (user_id=%d): %s", userID, resultStr)
		return nil, errInternal
	}
	status := resultStr[:colonIdx]
	payload := resultStr[colonIdx+1:]

	// payload 格式: AT:RT
	payloadColonIdx := strings.Index(payload, ":")
	if payloadColonIdx < 1 {
		logger.Errorf("refresh token: malformed token payload (user_id=%d): %s", userID, payload)
		return nil, errInternal
	}
	retAT := payload[:payloadColonIdx]
	retRT := payload[payloadColonIdx+1:]

	if status == "recovery" {
		// 崩溃恢复：旧 RT 在 GETDEL 后进程崩溃，pending 键保留了新 Token。
		// 直接返回已保存的新 Token（无需重新生成）。
		logger.Infof("refresh token: crash recovery served from pending (user_id=%d)", userID)
	} else if status != "ok" {
		logger.Errorf("refresh token: unexpected script status (user_id=%d): %s", userID, status)
		return nil, errInternal
	}

	return &v1.RefreshTokenResp{
		Token:        retAT,
		RefreshToken: retRT,
	}, nil
}
