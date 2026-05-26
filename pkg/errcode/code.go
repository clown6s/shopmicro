package errcode

// =====================================================================
// 业务错误码常量 — 下游服务使用，通过 gRPC BizError 传递给 gateway
// 编码规范: 各服务分段，user=10000-19999, product=20000-29999, ...
// =====================================================================

// User 服务: 10000 - 19999
const (
	CodeUserPhoneRequired uint32 = 10001
	CodeUserPwdRequired   uint32 = 10002
	CodeUserPhoneFormat   uint32 = 10003
	CodeUserPwdWeak       uint32 = 10004
	CodeUserPhoneExists   uint32 = 10005
	CodeUserPhonePwdWrong uint32 = 10006
	CodeUserInvalidID     uint32 = 10007
	CodeUserNothingUpdate uint32 = 10008
	CodeUserNotFound      uint32 = 10009
	CodeUserTokenRequired uint32 = 10010
	CodeUserTokenInvalid  uint32 = 10011
	CodeUserTokenMismatch uint32 = 10012
	CodeUserInternal      uint32 = 10013
	CodeUserUnauthorized  uint32 = 10014
	CodeUserForbidden     uint32 = 10015
	CodeUserEmailFormat   uint32 = 10016
	CodeUserParam         uint32 = 10017 // 参数校验失败（字段超长、格式错误等）
	CodeUserPwdTooLong    uint32 = 10018 // 密码超过最大长度
	CodeUserAccountLocked uint32 = 10019 // 账号被临时锁定（登录失败次数过多）
)

// Product 服务: 20000 - 29999
const (
	CodeProductParam    uint32 = 20001 // 参数校验失败
	CodeProductNotFound uint32 = 20002 // 分类不存在
	CodeProductInternal uint32 = 20003 // 内部错误
)

// District 服务: 30000 - 39999
const (
	CodeDistrictParam    uint32 = 30001 // 参数校验失败
	CodeDistrictInternal uint32 = 30002 // 内部错误
)

// Gateway 基础设施: 50000 - 59999 (网关自身错误)
const (
	CodeGwParam          uint32 = 50001
	CodeGwServer         uint32 = 50002
	CodeGwToken          uint32 = 50003
	CodeGwNotFound       uint32 = 50004
	CodeGwResponse       uint32 = 50005
	CodeGwRateLimit      uint32 = 50006 // 请求频率限制
	CodeGwSrvUnavailable uint32 = 50007 // 下游服务不可用（熔断）
)
