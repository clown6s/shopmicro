# 错误码规范

网关统一使用 `{"code": int, "message": string, "data": ...}` JSON 格式响应。

---

## 错误码分段规则

| 范围 | 类型 | 说明 |
|------|------|------|
| `0` | 成功 | 无错误 |
| `10000–19999` | 用户服务业务错误 | user 服务 |
| `20000–29999` | 商品服务业务错误 | product 服务 (预留) |
| `50000–59999` | 网关基础设施错误 | gateway 自身产生 |

> ⚠️ **历史遗留**：部分网关错误码暂用 `50001–50007`（非 5xxxx），下一大版本将统一迁移至 `5xxxx`。

---

## 网关层错误码 (当前实现)

| 常量 | 值 | HTTP Status | 说明 |
|------|-----|-------------|------|
| `ErrParam` (`CodeGwParam`) | `50001` | 400 | 参数错误 |
| `ErrServer` (`CodeGwServer`) | `50002` | 500 | 服务器内部错误 |
| `ErrToken` (`CodeGwToken`) | `50003` | 401 | token 无效 / 缺失 / 已吊销 |
| `ErrNotFound` (`CodeGwNotFound`) | `50004` | 404 | 服务未发现 |
| `ErrResponse` (`CodeGwResponse`) | `50005` | 500 | 下游响应异常 |
| `ErrRateLimit` (`CodeGwRateLimit`) | `50006` | 429 | 请求频率限制 |
| `ErrServiceUnavail` (`CodeGwSrvUnavailable`) | `50007` | 503 | 下游服务不可用 (熔断) |

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
