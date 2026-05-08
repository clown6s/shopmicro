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
)

// Product 服务: 20000 - 29999 (预留)

// Gateway 基础设施: 50000 - 59999 (网关自身错误)
const (
	CodeGwParam    uint32 = 50001
	CodeGwServer   uint32 = 50002
	CodeGwToken    uint32 = 50003
	CodeGwNotFound uint32 = 50004
	CodeGwResponse uint32 = 50005
)
