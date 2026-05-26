# 错误码规范

网关统一使用 `{"code": int, "message": string, "data": ...}` JSON 格式响应。

---

## 错误码分段规则

| 范围 | 类型 | 说明 |
|------|------|------|
| `0` | 成功 | 无错误 |
| `10000–19999` | 用户服务业务错误 | user 服务 |
| `20000–29999` | 商品服务业务错误 | product 服务 |
| `30000–39999` | 行政区划服务业务错误 | district 服务 |
| `50000–59999` | 网关基础设施错误 | gateway 自身产生 |

---

## 网关层错误码

| 常量 | 值 | HTTP Status | 说明 |
|------|-----|-------------|------|
| `CodeGwParam` | `50001` | 400 | 参数格式错误（字段缺失、格式无效） |
| `CodeGwServer` | `50002` | 500 | 服务器内部错误 |
| `CodeGwToken` | `50003` | 401 | 认证失败（token 缺失/无效/已吊销） |
| `CodeGwNotFound` | `50004` | 404 | 服务或资源未找到 |
| `CodeGwResponse` | `50005` | 500 | 下游响应解析异常 |
| `CodeGwRateLimit` | `50006` | 429 | 请求频率限制 |
| `CodeGwSrvUnavailable` | `50007` | 503 | 下游服务不可用（熔断） |
| `CodeGwForbidden` | `50008` | 403 | 无权限（不在白名单或方法不允许） |
| `CodeGwMethodNotAllowed` | `50009` | 405 | HTTP 方法不允许 |
| `CodeGwUnsupportedMediaType` | `50010` | 415 | Content-Type 不支持 |
| `CodeGwPayloadTooLarge` | `50011` | 413 | 请求体过大 |

---

## 用户服务错误码

| 常量 | 值 | 说明 |
|------|-----|------|
| `CodeUserPhoneRequired` | `10001` | 手机号必填 |
| `CodeUserPwdRequired` | `10002` | 密码必填 |
| `CodeUserPhoneFormat` | `10003` | 手机号格式错误 |
| `CodeUserPwdWeak` | `10004` | 密码强度不足 |
| `CodeUserPhoneExists` | `10005` | 手机号已注册 |
| `CodeUserPhonePwdWrong` | `10006` | 手机号或密码错误 |
| `CodeUserInvalidID` | `10007` | 无效的用户 ID |
| `CodeUserNothingUpdate` | `10008` | 无任何更新内容 |
| `CodeUserNotFound` | `10009` | 用户不存在 |
| `CodeUserTokenRequired` | `10010` | token 必填 |
| `CodeUserTokenInvalid` | `10011` | token 无效 |
| `CodeUserTokenMismatch` | `10012` | token 与用户不匹配 |
| `CodeUserInternal` | `10013` | 用户服务内部错误 |
| `CodeUserUnauthorized` | `10014` | 未授权 (未登录) |
| `CodeUserForbidden` | `10015` | 无权限操作 (非本人数据) |
| `CodeUserEmailFormat` | `10016` | 邮箱格式错误 |
| `CodeUserParam` | `10017` | 参数校验失败（字段超长、格式错误等） |
| `CodeUserPwdTooLong` | `10018` | 密码超过最大长度 |
| `CodeUserAccountLocked` | `10019` | 账号被临时锁定（登录失败次数过多） |

---

## 下游服务如何返回错误

下游 gRPC 服务 **不要** 直接返回 HTTP 状态码，应通过 `errcode.NewBizError` 返回带业务错误码的 `error`：

```go
// user/handler/handler.go 示例
return nil, errPhonePwdWrong
// 底层会被 kitex 包装为 gRPC error，网关通过 ParseBizError 解析
```

网关层通过 `errcode.ParseBizError(err)` 从 gRPC 错误消息中提取 `code` 和 `message`，再映射为正确的 HTTP 状态码返回给调用方。

---

## 响应示例

**成功：**
```json
{"code": 0, "message": "ok", "data": {...}}
```

**业务错误 (用户服务)：**
```json
{"code": 10006, "message": "phone or password incorrect"}
```

**网关错误：**
```json
{"code": 50003, "message": "token revoked"}
```
