package handler

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/go-sql-driver/mysql"
	goredis "github.com/redis/go-redis/v9"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/jwt"
	"shopmicro/pkg/logger"
	"shopmicro/user/conf"
	"shopmicro/user/data"
)

func newBiz(code uint32, msg string) *errcode.BizError { return errcode.NewBizError(code, msg) }
func newBizHTTP(code uint32, msg string, httpStatus int) *errcode.BizError {
	return errcode.BizErrorHTTPStatus(code, msg, httpStatus)
}

var (
	errPhoneRequired    = newBiz(errcode.CodeUserPhoneRequired, "phone is required")
	errPasswordRequired = newBiz(errcode.CodeUserPwdRequired, "password is required")
	errPhoneFormat      = newBiz(errcode.CodeUserPhoneFormat, "invalid phone format")
	errPasswordWeak     = newBiz(errcode.CodeUserPwdWeak, "password must be 8-128 characters with at least one letter and one digit")
	errPasswordTooLong  = newBiz(errcode.CodeUserPwdTooLong, "password must not exceed 128 characters")
	errPhoneExists      = newBiz(errcode.CodeUserPhoneExists, "phone already exists")
	errInternal         = newBizHTTP(errcode.CodeUserInternal, "internal error", 500)
	errPhonePwdWrong    = newBiz(errcode.CodeUserPhonePwdWrong, "phone or password incorrect")
	errInvalidUserID    = newBiz(errcode.CodeUserInvalidID, "invalid user id")
	errNothingUpdate    = newBiz(errcode.CodeUserNothingUpdate, "nothing to update")
	errUserNotFound     = newBizHTTP(errcode.CodeUserNotFound, "user not found", 404)
	errUnauthorized     = newBizHTTP(errcode.CodeUserUnauthorized, "unauthorized: missing user identity", 401)
	errForbidden        = newBizHTTP(errcode.CodeUserForbidden, "forbidden: cannot operate other user's data", 403)
	errEmailFormat      = newBiz(errcode.CodeUserEmailFormat, "invalid email format")
	errAccountLocked    = newBizHTTP(errcode.CodeUserAccountLocked, "account temporarily locked due to too many failed login attempts", 423)
	errRegisterTooMany  = newBizHTTP(errcode.CodeUserParam, "too many registration attempts, please try again later", 429) // CodeUserParam 保持向后兼容，消息已区分
	errRefreshTooMany   = newBizHTTP(errcode.CodeUserParam, "too many refresh attempts, please try again later", 429)      // CodeUserParam 保持向后兼容，消息已区分
)

// P1-21：手机号正则 — 中国手机号格式
var phoneRegex = regexp.MustCompile(`^1[3-9]\d{9}$`)

// emailRegex 校验基本邮箱格式（RFC 5322 简化版）。
// 注意：不支持国际化域名 (IDN)，如需支持应在入库前转换为 Punycode。
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// passwordLetterRe 和 passwordDigitRe 拆分为两个检查，因为 Go 的 RE2 引擎
// 不支持前瞻断言 ((?=...))。使用 (?=.*[a-zA-Z])(?=.*\d) 的单一正则方式
// 在 Go 的 regexp 包中不可用。
var passwordLetterRe = regexp.MustCompile(`[a-zA-Z]`)
var passwordDigitRe = regexp.MustCompile(`\d`)

// passwordValid 检查密码长度和复杂度（参数由 conf.Security 驱动）。
func (s *UserServiceImpl) passwordValid(pwd string) bool {
	return len(pwd) >= s.sec.PasswordMinLen && len(pwd) <= s.sec.PasswordMaxLen &&
		passwordLetterRe.MatchString(pwd) &&
		passwordDigitRe.MatchString(pwd)
}

// UserServiceImpl 实现用户服务的 gRPC handler。
type UserServiceImpl struct {
	userDal   UserStore
	jwtGen    *jwt.Generator
	jwtExpHrs int
	rtGen     *jwt.Generator // 刷新令牌生成器
	rtExpHrs  int            // 刷新令牌过期时间（小时）
	rdb       RedisOps
	sec       conf.Security
}

func NewUserHandler(d *data.Data, jwtSecret string, jwtExpHours int, jwtIssuer string, jwtRefreshSecret string, sec conf.Security) (*UserServiceImpl, error) {
	// P2-16 修复：防止 jwtExpHours 为零导致 rtExpHrs 异常
	if jwtExpHours <= 0 {
		jwtExpHours = 24
	}
	gen, err := jwt.NewGenerator(jwtSecret, time.Duration(jwtExpHours)*time.Hour, jwtIssuer)
	if err != nil {
		return nil, fmt.Errorf("init jwt generator failed: %w", err)
	}
	// 刷新令牌：默认访问令牌过期时间的 7 倍，最少 24 小时
	rtExpHrs := jwtExpHours * 7
	if rtExpHrs < 24 {
		rtExpHrs = 168 // 至少 7 天
	}

	// 使用相同密钥会破坏双令牌架构的设计目的。
	if jwtRefreshSecret == "" {
		return nil, fmt.Errorf("JWT_REFRESH_SECRET is required")
	}
	if jwtRefreshSecret == jwtSecret {
		return nil, fmt.Errorf("JWT_REFRESH_SECRET must differ from JWT_SECRET — using the same secret defeats the dual-token architecture")
	}
	rtGen, err := jwt.NewGenerator(jwtRefreshSecret, time.Duration(rtExpHrs)*time.Hour, jwtIssuer)
	if err != nil {
		return nil, fmt.Errorf("init refresh token generator failed: %w", err)
	}
	return &UserServiceImpl{
		userDal:   d.User(),
		jwtGen:    gen,
		jwtExpHrs: jwtExpHours,
		rtGen:     rtGen,
		rtExpHrs:  rtExpHrs,
		rdb:       d.Rdb,
		sec:       sec,
	}, nil
}

// =========================
// Redis Key 前缀常量
// 集中定义，便于全局搜索和维护命名空间一致性。
// =========================

const tokenPrefix = "shopmicro:user:token:"
const refreshTokenPrefix = "shopmicro:user:refresh:"
const tokenSetKeyPrefix = "shopmicro:user:rtset:"
const loginFailKeyPrefix = "shopmicro:login:fail:"

// P2-06/P2-07：共享的频率限制 Lua 脚本（原子性 INCR + 条件 EXPIRE）
const rateLimitScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 then
    redis.call('EXPIRE', KEYS[1], ARGV[2])
end
if tonumber(count) > tonumber(ARGV[1]) then
    return 1
end
return 0
`

// P2-06：注册频率限制 — 按手机号，每小时最多 N 次（配置驱动）
const registerRateLimitKey = "shopmicro:register:rate:"

// P2-07：RefreshToken 频率限制 — 按用户 ID，每分钟最多 N 次（配置驱动）
const refreshRateLimitKey = "shopmicro:refresh:rate:"

// P1-05：账号级别暴力破解保护（参数由 conf.Security 驱动）
const loginFailIncrScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 then
    redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return count
`

// R2-P2-04：用于撤销用户所有令牌（访问令牌 + 刷新令牌）的原子性 Lua 脚本。
// KEYS[1]=tokenKey, KEYS[2]=rtSetKey
// ARGV[1]=refreshTokenPrefix（用于构造 RT 反查 key），避免 Lua 内硬编码前缀
const revokeTokensScript = `
local tokenKey = KEYS[1]
local rtSetKey = KEYS[2]
local rtPrefix = ARGV[1]
local rtTokens = redis.call('SMEMBERS', rtSetKey)
for _, rt in ipairs(rtTokens) do
    local rtKey = rtPrefix .. rt
    redis.call('DEL', rtKey)
end
redis.call('DEL', rtSetKey)
redis.call('DEL', tokenKey)
return 1
`

// checkLoginAttempts 检查手机号是否有过多的近期登录失败记录。
func (s *UserServiceImpl) checkLoginAttempts(ctx context.Context, phone string) error {
	key := fmt.Sprintf("%s%s", loginFailKeyPrefix, phone)
	count, err := s.rdb.Get(ctx, key)
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil
		}
		logger.Warnf("login: redis unavailable for rate check, failing closed (phone=%s): %v", maskPhone(phone), err)
		return errAccountLocked
	}
	if count == "" {
		return nil // 空值视为无失败记录
	}
	if n, err := strconv.Atoi(count); err != nil {
		// P1-07: Redis 返回非数字值（数据损坏或竞态写入），
		// 按失败关闭处理，防止绕过暴力破解保护。
		logger.Warnf("login: invalid redis counter value for phone %s: %q (err=%v)", maskPhone(phone), count, err)
		return errAccountLocked
	} else if n >= s.sec.LoginMaxFailures {
		return errAccountLocked
	}
	return nil
}

// recordLoginFailure 递增手机号的登录失败计数器。
func (s *UserServiceImpl) recordLoginFailure(ctx context.Context, phone string) {
	key := fmt.Sprintf("%s%s", loginFailKeyPrefix, phone)
	if _, err := s.rdb.RunScript(ctx, loginFailIncrScript, []string{key}, s.sec.LoginLockoutSec); err != nil {
		logger.Warnf("login: failed to record login failure for phone %s: %v", maskPhone(phone), err)
	}
}

// clearLoginFailures 在成功登录后清除登录失败计数器。
func (s *UserServiceImpl) clearLoginFailures(ctx context.Context, phone string) {
	key := fmt.Sprintf("%s%s", loginFailKeyPrefix, phone)
	_ = s.rdb.Del(ctx, key)
}

// checkOwnership 验证调用者的 user_id 是否与请求的用户 ID 匹配。
func (s *UserServiceImpl) checkOwnership(ctx context.Context, targetID int64) error {
	userIDStr, ok := metainfo.GetPersistentValue(ctx, "user_id")
	if !ok || userIDStr == "" {
		return errUnauthorized
	}
	callerID, err := parseInt64(userIDStr)
	// R7-P1-02: 拒绝负数 ID — parseInt64 允许负数通过，负数 ID 会导致 Redis key 异常
	if err != nil || callerID <= 0 {
		return errUnauthorized
	}
	if callerID != targetID {
		return errForbidden
	}
	return nil
}

// requireAdmin 验证调用者已通过 gateway 认证（user_id 存在）。
// TODO: 等待 JWT Claims 加入 role 字段后，改为检查 role == "admin"。
func requireAdmin(ctx context.Context) error {
	userIDStr, ok := metainfo.GetPersistentValue(ctx, "user_id")
	if !ok || userIDStr == "" {
		return errForbidden
	}
	if _, err := parseInt64(userIDStr); err != nil {
		return errForbidden
	}
	return nil
}

// =========================
// 辅助函数
// =========================

// isDuplicateKeyError 检查错误是否为 MySQL 重复键错误（错误码 1062）。
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1062
	}
	return false
}

// parseInt64 使用 strconv 将字符串解析为 int64。
func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// maskPhone 脱敏手机号：保留前3后4，中间用 * 代替（中国手机号 11 位）。
// 其他长度的号码：保留前3后2。极短号码(≤3)全掩码。
func maskPhone(phone string) string {
	n := len(phone)
	if n <= 3 {
		return "***"
	}
	if n == 11 {
		// 中国手机号标准格式
		return phone[:3] + "****" + phone[7:]
	}
	if n < 7 {
		// 短号码：显示前3 + 掩码
		return phone[:3] + "***"
	}
	// 其他长度：前3 + 掩码 + 后2
	return phone[:3] + "****" + phone[n-2:]
}

// maskEmail 脱敏邮箱：a***@example.com
func maskEmail(email string) string {
	if email == "" {
		return ""
	}
	at := strings.Index(email, "@")
	if at < 1 {
		return "***"
	}
	return email[:1] + "***" + email[at:]
}
