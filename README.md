# shopmicro 开发指南

> 技术栈：Hertz (HTTP 网关) + Kitex (gRPC RPC) + Protobuf + Etcd (服务发现) + MySQL + Redis + OpenTelemetry

## 项目结构

```
shopmicro/
├── go.work                  # Go workspace 联合开发（需与机器 Go 版本一致）
├── Makefile                 # 构建/运行命令
├── .gitignore
│
├── user/                    # User 微服务（独立 go.mod）
│   ├── main.go              # Kitex gRPC 服务入口 :8888
│   ├── handler/
│   │   ├── handler.go       # RPC 业务逻辑实现
│   │   └── handler_test.go  # 单元测试
│   ├── data/
│   │   ├── data.go          # 数据访问层初始化（MySQL + Redis）
│   │   └── user.go          # 用户 CRUD 操作（参数化查询）
│   ├── model/
│   │   └── user.go          # 用户数据模型
│   ├── kitex_gen/           # Kitex 自动生成（勿手动修改）
│   ├── api/
│   │   └── user.proto       # Protobuf IDL 定义
│   ├── conf/
│   │   ├── conf.go          # 配置结构体 + 环境变量覆盖
│   │   └── config.yaml      # 配置文件
│   ├── Dockerfile           # 多阶段构建（golang:1.26-alpine → alpine:3.19）
│   ├── .env.example         # 环境变量模板
│   └── .env                 # 本地开发密钥（gitignore）
│
├── gateway/                 # HTTP 网关（独立 go.mod）
│   ├── main.go              # Hertz HTTP 入口 :8080
│   ├── proxy/
│   │   ├── proxy.go         # 通用 HTTP→gRPC 代理（所有服务共用）
│   │   └── proxy_test.go    # 单元测试
│   ├── middleware/
│   │   ├── cors.go          # CORS 跨域配置
│   │   ├── ratelimit.go     # Redis 滑动窗口分布式限流
│   │   ├── ratelimit_test.go
│   │   ├── security.go      # 安全响应头（CSP/HSTS/X-Frame-Options 等）
│   │   ├── accesslog.go     # 结构化访问日志（防日志注入）
│   │   ├── trace.go         # X-Trace-Id 响应头注入
│   │   └── recovery.go      # Panic 恢复（堆栈记录）
│   ├── health/
│   │   └── health.go        # /health（存活）+ /ready（就绪）探针
│   ├── conf/
│   │   ├── conf.go          # 配置结构体 + 环境变量覆盖
│   │   └── config.yaml      # 配置文件
│   ├── Dockerfile           # 多阶段构建
│   ├── .env.example
│   └── .env
│
└── pkg/                     # 共享模块（被 gateway 和 user 共同依赖）
    ├── conf/
    │   └── conf.go          # 通用配置加载（YAML + .env + Vault）
    ├── logger/
    │   └── logger.go        # zap 日志封装
    ├── errcode/
    │   ├── code.go          # 统一错误码常量（user: 1xxxx, gw: 5xxxx）
    │   ├── errcode.go       # 网关错误码定义
    │   ├── biz_error.go     # 业务错误码结构体
    │   └── errcode_test.go
    ├── response/
    │   └── response.go      # 统一 HTTP 响应封装
    ├── jwt/
    │   ├── jwt.go           # JWT 生成器 + 解析器
    │   └── jwt_test.go
    ├── mysql/
    │   ├── mysql.go         # MySQL 连接池 + OTEL 追踪（SQL 已脱敏）
    │   ├── config.go        # 连接池配置
    │   └── config_test.go
    ├── redis/
    │   ├── redis.go         # Redis 客户端封装
    │   └── redis_test.go
    ├── otel/
    │   └── otel.go          # OpenTelemetry 初始化
    └── secrets/
        ├── provider.go      # 密钥提供者接口
        └── vault.go         # HashiCorp Vault 集成
```

---

## 一、网关架构

gateway 使用**通用代理模式**：不写死每个服务的 HTTP handler，而是通过 proto 文件动态生成 Kitex generic client，所有服务共用一套 HTTP→gRPC 转发逻辑。

```
HTTP 请求 ──→ gateway ──→ etcd 服务发现 ──→ Kitex Generic Client ──→ gRPC 服务
              │                                    │
              ├── OTel Tracing                     ├── proto 文件
              ├── Panic Recovery                   └── 泛序列化
              ├── Security Headers
              ├── CORS
              ├── Access Log（结构化）
              ├── X-Trace-Id 注入
              ├── 分布式限流（Redis 滑动窗口）
              ├── 端点级限流（per-endpoint）
              ├── JWT 认证 + Redis Token 校验
              ├── 方法白名单（per-service）
              └── 熔断器（per-service, gobreaker）
```

### 1.1 中间件执行顺序

```
Request → OTel Tracing → Recovery → CORS → Security Headers → Access Log
        → X-Trace-Id → Rate Limit (IP + User) → Proxy Handler
```

Proxy Handler 内部执行：**方法白名单 → 端点级限流 → JWT 认证 → gRPC 调用（含熔断）**

### 1.2 核心配置（gateway/main.go）

```go
p := proxy.New(
    proxy.WithJWT(cfg.JWT.Secret, cfg.JWT.Issuer),        // JWT 校验 + issuer 验证
    proxy.WithRedis(rdb),                                   // Token 黑名单 / 限流
    proxy.WithPublicMethods(                                // 公开接口（无需登录）
        "RegisterUser", "Login", "RefreshToken",
    ),
    // 方法白名单：未列出的服务/方法 → 403
    proxy.WithAllowedMethods("user",
        "RegisterUser", "Login", "RefreshToken",
        "Logout", "UpdateUser", "DeleteUser", "ListUser",
    ),
    // 熔断器（per-service）
    proxy.WithCircuitBreaker(
        cfg.CircuitBreaker.MaxRequests,
        cbInterval, cbTimeout,
        uint32(cfg.CircuitBreaker.ConsecutiveFailures),
    ),
    // 端点级限流（在 proxy handler 内执行，不区分 HTTP 方法）
    proxy.WithEndpointRateLimit(rl.NewEndpointCheckFn()),
)

// 启动时：从 etcd 发现已有服务并注册 generic client
p.DiscoverAndRegister(r, cfg.Etcd.Addrs, cfg.ProtoBase)

// 运行时：监听 etcd 新服务上线，自动注册（含指数退避重连）
p.WatchServices(watchCtx, r, cfg.Etcd.Addrs, cfg.ProtoBase)
```

### 1.3 proto 文件路径规则

```
{protoBase}/{service}/api/{service}.proto

# 例如 protoBase = "../" 时：
# user 服务 → ../user/api/user.proto
```

protoBase 在 `gateway/conf/config.yaml` 中配置，Docker 环境通过 `PROTO_BASE` 环境变量覆盖。注意：proto 文件必须真实存在于该路径，否则网关启动时会报错（fail-fast）。

### 1.4 请求路由

```
POST/GET/PUT/DELETE /api/v1/:service/:method
Authorization: Bearer {token}
```

- `:service` 对应 proto service 名称（如 `user`）
- `:method` 对应 proto rpc 方法名（如 `Login`）
- 请求体 JSON，gateway 用 JSONPbGeneric 序列化为 Protobuf 后发给 gRPC 服务

### 1.5 健康检查端点

| 端点 | 用途 | 说明 |
|------|------|------|
| `GET /health` | 存活探针 | 始终返回 200 `{"status":"ok"}` |
| `GET /ready` | 就绪探针 | 检查 Redis + Etcd 连通性，异常返回 503 |
| `GET /health`（user:8899） | 存活探针 | 检查 MySQL + Redis 连通性 |

### 1.6 统一响应格式

```json
{
  "code": 0,
  "message": "ok",
  "data": { ... }
}
```

- `code: 0` 成功
- `code: 1xxxx` 业务错误（user 服务）
- `code: 5xxxx` 网关基础设施错误

---

## 二、User 服务 API

### 2.1 接口清单

| RPC 方法 | HTTP 示例 | 认证 | 说明 |
|----------|----------|------|------|
| `RegisterUser` | `POST /api/v1/user/RegisterUser` | 否 | 注册（手机号+密码） |
| `Login` | `POST /api/v1/user/Login` | 否 | 登录，返回 token + refreshToken |
| `RefreshToken` | `POST /api/v1/user/RefreshToken` | 否 | 刷新令牌（旧 refresh token 一次性） |
| `Logout` | `POST /api/v1/user/Logout` | 是 | 注销当前设备 |
| `UpdateUser` | `POST /api/v1/user/UpdateUser` | 是 | 更新个人信息（仅限本人） |
| `DeleteUser` | `POST /api/v1/user/DeleteUser` | 是 | 注销账号（仅限本人，软删除） |
| `ListUser` | `POST /api/v1/user/ListUser` | 是 | 用户列表（分页+搜索） |

### 2.2 认证机制

- **JWT Access Token**：HS256 签名，prod 密钥≥64字符，默认 24 小时过期，支持多设备登录，支持 audience 校验
- **Refresh Token**：独立 secret（可配置），默认 7 天过期，一次性使用（GETDEL 原子防重放 + Lua 脚本原子轮换）
- **Token 校验**：gateway 通过 Redis SET（SISMEMBER）校验 token 是否有效
- **身份传递**：gateway 通过 Kitex metainfo 传递 `user_id` 和 `token` 给下游服务
- **所有权校验**：UpdateUser/DeleteUser 通过 `checkOwnership()` 校验调用者身份
- **会话吊销**：手机号变更时原子吊销所有 access token + refresh token，账户删除同理

### 2.3 请求/响应示例

```bash
# 注册
curl -X POST http://localhost:8080/api/v1/user/RegisterUser \
  -H "Content-Type: application/json" \
  -d '{"phone":"13800138000","password":"abc12345","realName":"张三"}'

# 登录
curl -X POST http://localhost:8080/api/v1/user/Login \
  -H "Content-Type: application/json" \
  -d '{"phone":"13800138000","password":"abc12345"}'
# → {"code":0,"message":"ok","data":{"id":1,"token":"...","refreshToken":"..."}}

# 刷新令牌
curl -X POST http://localhost:8080/api/v1/user/RefreshToken \
  -H "Content-Type: application/json" \
  -d '{"refreshToken":"..."}'

# 获取用户列表（需 token）
curl -X POST http://localhost:8080/api/v1/user/ListUser \
  -H "Authorization: Bearer {token}" \
  -H "Content-Type: application/json" \
  -d '{"page":1,"pageSize":20}'
```

---

## 三、添加新接口（完整流程）

以给 User 服务添加 `SearchUser`（按姓名搜索）接口为例。

### 3.1 修改 proto 文件

编辑 `user/api/user.proto`，添加 message 和 rpc：

```protobuf
message SearchUserReq {
  string real_name = 1;
  int32 page = 2;
  int32 page_size = 3;
}

message SearchUserResp {
  repeated UserInfo users = 1;
  int64 total = 2;
}

service UserService {
  // ... 已有方法
  rpc SearchUser(SearchUserReq) returns(SearchUserResp);  // 新增
}
```

### 3.2 重新生成 Kitex 代码

```bash
cd user
kitex -module shopmicro/user -service user -type protobuf api/user.proto
```

**注意**：此命令会覆盖 `handler.go` 和 `main.go`，建议执行前备份。

生成后 `handler.go` 会多出新的空方法：

```go
func (s *UserServiceImpl) SearchUser(ctx context.Context, req *v1.SearchUserReq) (resp *v1.SearchUserResp, err error) {
    // TODO: Your code here...
    return
}
```

### 3.3 实现 RPC 业务逻辑

在 `user/handler/handler.go` 中填充 `SearchUser` 方法（参考现有 `ListUser` 的实现）。

### 3.4 添加数据访问方法

在 `user/data/user.go` 添加查询方法（使用参数化查询防 SQL 注入）。

### 3.5 在网关注册新方法

**无需写新的 HTTP handler**，只需在 `gateway/main.go` 的 `WithAllowedMethods` 中添加：

```go
proxy.WithAllowedMethods("user",
    "RegisterUser", "Login", "RefreshToken",
    "Logout", "UpdateUser", "DeleteUser", "ListUser",
    "SearchUser",   // ← 新增这一行
),
```

### 3.6 验证

```bash
curl -X POST http://localhost:8080/api/v1/user/SearchUser \
  -H "Authorization: Bearer {token}" \
  -H "Content-Type: application/json" \
  -d '{"real_name":"张","page":1,"page_size":10}'
```

---

## 四、添加新服务（完整流程）

以添加 `product` 服务为例。

### 4.1 创建服务目录

```
product/
├── api/
│   └── product.proto        # Protobuf IDL
├── handler/
│   └── handler.go           # RPC 业务逻辑实现
├── main.go                  # Kitex 服务入口
├── data/
│   ├── data.go              # 数据访问层初始化
│   └── product.go           # CRUD 操作
├── model/
│   └── product.go           # 数据模型
├── conf/
│   ├── conf.go              # 配置结构体
│   └── config.yaml          # 配置文件
├── Dockerfile
├── .env.example
└── .env
```

### 4.2 创建/更新 go.work

```bash
# 已有 go.work，加新服务
go work use ./shopmicro/product
```

> 注意：`go.work` 第一行的 go 版本号必须与本机 `go version` 一致。

### 4.3 编写 proto 文件 + 生成代码

```bash
cd product
kitex -module shopmicro/product -service product -type protobuf api/product.proto
```

### 4.4 编写业务代码

- `handler/handler.go`：实现各个 RPC 方法
- `main.go`：初始化 data 层、注册到 etcd、启动 Kitex server（参考 `user/main.go`）

### 4.5 在 gateway 中注册新服务

编辑 `gateway/main.go`，在 `WithAllowedMethods` 中添加新服务的白名单：

```go
proxy.WithAllowedMethods("product",
    "CreateProduct",
    "GetProduct",
    "UpdateProduct",
    "DeleteProduct",
    "ListProduct",
),
```

如果某个方法不需要登录认证，加到 `WithPublicMethods` 中。

### 4.6 Docker 部署

在 `gateway/Dockerfile` 中添加 proto 文件拷贝：

```dockerfile
COPY --from=builder /build/product/api /app/service-protos/product/api
```

---

## 五、代码生成详解

### 5.1 Kitex 命令

```bash
cd user
kitex -module shopmicro/user -service user -type protobuf api/user.proto
```

| 参数 | 说明 |
|------|------|
| `-module` | Go 模块名，对应 go.mod 中的 module |
| `-service` | 服务名，生成服务端骨架（handler.go、main.go） |
| `-type protobuf` | 使用 Protobuf 协议 |

### 5.2 注意事项

- `handler.go` 和 `main.go` 每次 `kitex` 都会覆盖，业务代码建议写在独立的 `handler/` 子包中，或生成后及时合并备份
- `make clean` 只删除 `kitex_gen/` 目录，手写文件不受影响

---

## 六、配置速查

| 要改什么 | 改哪个文件 | 环境变量 |
|---------|-----------|---------|
| 网关端口 | `gateway/conf/config.yaml` → `server.addr` | `SERVER_ADDR` |
| 网关 protoBase | `gateway/conf/config.yaml` → `protoBase` | `PROTO_BASE` |
| 网关 Etcd 地址 | `gateway/conf/config.yaml` → `etcd.addrs` | `ETCD_ADDRS` |
| 网关 JWT Secret | `gateway/conf/config.yaml` → `jwt.secret` | `JWT_SECRET`（prod≥64字符） |
| user 服务端口 | `user/conf/config.yaml` → `server.addr` | `SERVER_ADDR` |
| user 健康检查端口 | `user/conf/config.yaml` → `healthAddr` | `HEALTH_ADDR` |
| user MySQL 连接 | `user/conf/config.yaml` → `mysql.dsn` | `MYSQL_DSN` |
| Redis 地址 | 各服务 `conf/config.yaml` → `redis.addr` | `REDIS_ADDR` |
| Redis 密码 | 各服务 `conf/config.yaml` → `redis.password` | `REDIS_PASSWORD` |
| Vault 地址 | 各服务 `conf/config.yaml` → `vault.address` | `VAULT_ADDRESS` |
| Vault Token | 各服务 `conf/config.yaml` → `vault.token` | `VAULT_TOKEN` |

配置优先级：**环境变量 > .env 文件 > config.yaml**

---

## 七、Docker 部署

### 7.1 构建镜像

```bash
# 构建网关镜像
docker build -f gateway/Dockerfile -t shopmicro-gateway .

# 构建 user 镜像
docker build -f user/Dockerfile -t shopmicro-user .
```

### 7.2 Dockerfile 特性

- 多阶段构建（golang:1.26-alpine → alpine:3.19）
- 非 root 用户运行（appuser）
- 敏感配置通过环境变量注入（无需重新构建镜像）
- Gateway Dockerfile 自动拷贝 proto 文件到容器内路径

### 7.3 环境变量

启动容器前设置以下环境变量：

```bash
# MySQL
MYSQL_DSN=root:your_password@tcp(10.211.55.5:3306)/shop_micro?charset=utf8mb4&parseTime=True&loc=Local

# Redis
REDIS_ADDR=10.211.55.5:6379
REDIS_PASSWORD=your_redis_password

# JWT (prod 至少64字符, dev 至少32字符)
JWT_SECRET=your-jwt-secret-key-at-least-64-chars-for-production

# Etcd
ETCD_ADDRS=10.211.55.5:2379

# Gateway proto 路径（Docker 内部路径）
PROTO_BASE=/app/service-protos/

# 可选：Vault 密钥管理
VAULT_ADDRESS=http://your-vault-server:8200
VAULT_TOKEN=hvs.your-vault-token
```

---

## 八、OpenTelemetry 链路追踪

### 8.1 已自动埋点的链路

| 层级 | 组件 | 埋点方式 |
|------|------|---------|
| Gateway HTTP | Hertz 请求 | `hertztracing.ServerMiddleware` 自动创建根 span |
| Gateway → RPC | Kitex gRPC 调用 | `kotelkitex.NewClientSuite` 自动 |
| User RPC | Kitex Server 处理器 | `kotelkitex.NewServerSuite` 自动 |
| MySQL | SQL 查询 | `otelsql`（**SQL 已脱敏**，防泄露） |
| Redis | Redis 调用 | `redisotel.InstrumentTracing` 自动 |

整条请求链路（HTTP → Gateway → gRPC → DB/Redis）的 trace ID 自动串联。

Gateway 响应头返回 `X-Trace-Id`，可在 Jaeger 中直接搜索。

### 8.2 手动埋点

业务逻辑中添加 span：

```go
tracer := otel.Tracer("user-handler")
ctx, span := tracer.Start(ctx, "UserService.SearchUser")
defer span.End()

span.SetAttributes(attribute.Int64("user.query.page", int64(req.Page)))
```

### 8.3 OTEL 配置

| 环境变量 | 说明 | 推荐值 |
|---------|------|--------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Collector 地址（`https://` 自动启用 TLS） | `localhost:4317` |
| `OTEL_TRACE_SAMPLE_RATE` | 采样率 0.0–1.0 | dev: `1.0`，prod: `0.1` |

---

## 九、安全架构

> 经过 7 轮安全审计 + 修复，代码层面已达到生产级别。覆盖 OWASP API 安全 Top 10 全部相关条目。

### 9.1 已实施的安全措施

| 安全特性 | 实现位置 | 说明 |
|---------|-----------|------|
| **密码哈希** | `user/handler/handler.go` | bcrypt，cost=12 |
| **JWT 安全** | `pkg/jwt/jwt.go` | HS256 签名，算法白名单防混淆，密钥 prod≥64字符 / dev≥32字符，支持 audience/issuer 校验 |
| **双令牌认证** | `user/handler/handler.go` | Access Token（默认24h）+ Refresh Token（7d），独立 secret，一次性轮换（GETDEL 防重放） |
| **参数化查询** | `user/data/user.go` | 所有 SQL 使用 `?` 占位符；Update 使用白名单 `colMap` + `blockedCols` 双重防护 |
| **输入验证** | `user/handler/handler.go` | 手机号（`^1[3-9]\d{9}$`）、邮箱、密码强度（8-128字符，含字母+数字）、各字段长度限制、性别/年龄范围校验 |
| **时序攻击防护** | `user/handler/handler.go` | Login 对不存在的用户执行 dummy bcrypt（60字符正确格式 hash），防止计时攻击枚举手机号 |
| **暴力破解防护** | `user/handler/handler.go` | 登录失败计数 + Redis Lua 原子锁定（15分钟），注册限流（每手机号5次/小时），RefreshToken 限流（每用户10次/分钟） |
| **Fail-Closed 策略** | gateway + user | Redis 宕机时所有限流拒绝请求（登录/注册/刷新/全局），不降级放行 |
| **PII 脱敏** | `user/handler/handler.go` | 手机号保留后3位（`****123`）、邮箱脱敏（`a***@example.com`）、Login 不返回自增 userId |
| **日志脱敏** | `pkg/logger/logger.go` | `Safe()` 函数自动遮蔽 password/secret/token/api_key/access_key/private_key 等 19 种敏感字段 |
| **错误信息脱敏** | `gateway/proxy/proxy.go` + `pkg/response/response.go` | 内部错误统一返回 `"internal server error"`，BizErr 消息超128字符截断，手机冲突返回模糊错误 |
| **Token 多设备** | `user/handler/handler.go` | Redis SET 存储（TxPipeline 原子 SAdd+Expire），支持多设备登录，Logout 只注销当前设备 |
| **原子 Token 轮换** | `user/handler/handler.go` | RefreshToken 使用 Lua 脚本原子更新（SREM旧RT + SADD新RT + SET映射 + EXPIRE），防崩溃导致 token 丢失或重放 |
| **原子 Token 吊销** | `user/handler/handler.go` | DeleteUser + UpdateUser(手机变更) 使用同一 Lua 脚本原子吊销所有 access token + refresh token |
| **所有权校验** | `user/handler/handler.go` | UpdateUser/DeleteUser 通过 metainfo user_id 校验，禁止越权操作 |
| **分布式限流** | `gateway/middleware/ratelimit.go` | Redis 滑动窗口（Lua 原子脚本），per-IP + 端点级可配 |
| **熔断器** | `gateway/proxy/proxy.go` | gobreaker per-service 熔断，仅对已注册服务创建（防 OOM），可配连续失败次数/超时/半开探测 |
| **服务名注入防护** | `gateway/proxy/proxy.go` | service 名称正则 `^[a-z][a-z0-9]*$`，method 名称正则 `^[A-Za-z][a-zA-Z0-9]*$` |
| **安全响应头** | `gateway/middleware/security.go` | CSP（`default-src 'self'; frame-ancestors 'none'`）、X-Frame-Options: DENY、X-Content-Type-Options: nosniff、HSTS（仅 prod）、Referrer-Policy |
| **CORS** | `gateway/middleware/cors.go` | 精确匹配 Origin（无通配符子域名），credentials + wildcard 由浏览器规范拒绝（fail-closed） |
| **IP 伪造防护** | `gateway/middleware/accesslog.go` | `safeClientIP()` 验证 X-Forwarded-For 中的 IP 格式，伪造 IP 不参与限流；限流使用 TCP 层 `ClientIP()` |
| **Panic 恢复** | `gateway` + `user` | 自定义 recovery 中间件，abort-first 策略，记录堆栈 |
| **日志注入防护** | `gateway/middleware/accesslog.go` | zap 结构化字段，防 `\n`/`\r` 注入，IP 地址脱敏 |
| **SQL 脱敏** | `pkg/mysql/mysql.go` | 禁用所有 OTEL SQL span，防止语句泄露到追踪系统 |
| **OTEL TLS** | `pkg/otel/otel.go` | endpoint `https://` 前缀自动启用 TLS，`http://` 或无 scheme 使用 insecure |
| **请求追踪** | `gateway/middleware/trace.go` | 自动生成 X-Request-Id（crypto/rand hex16），支持上游传入 |
| **健康检查脱敏** | `gateway/health` + `user/main.go` | /health 仅返回 `{"status":"ok"}`，/ready 仅返回 ready/not_ready，/healthz 不暴露依赖名称 |
| **List 不查密码** | `user/data/user.go` | ListUser 查询不 SELECT password 列，password 字段 `json:"-"` |
| **软删除** | `user/data/user.go` | DeleteUser 使用 `deleted_at` 字段，所有查询排除已删除记录 |
| **密钥管理** | `pkg/secrets/` | 支持 Vault 集成（atomic.Value 线程安全切换）+ 环境变量回退，.env 已 gitignore |
| **Docker 安全** | Dockerfile | 非 root 用户运行，多阶段构建 |
| **Graceful Shutdown** | `gateway/main.go` + `user/main.go` | WaitGroup + 超时控制，shutdown 前标记 unhealthy 通知 K8s |

### 9.2 限流配置

```yaml
# gateway/conf/config.yaml
rateLimit:
  limit: 100          # 全局：每 IP 每窗口最大请求数
  windowSec: 60       # 滑动窗口时长（秒）
  endpoints:
    user/Login:
      limit: 5        # 端点级：Login 每分钟每IP最多5次
      windowSec: 60
```

| 限流维度 | 默认规则 | 说明 |
|---------|---------|------|
| 全局 per-IP | 100次/分钟 | Redis ZSET 滑动窗口（Lua 原子） |
| 端点级 | 可配 | 在 `endpoints:` 下添加，如 `user/Login: 5/分` |
| 注册 per-Phone | 5次/小时 | user 服务侧 Redis Lua 原子计数 |
| 登录失败锁定 | 10次/15分钟 | per-Phone，Redis Lua 原子 INCR+EXPIRE |
| RefreshToken | 10次/分钟 | per-UserID，user 服务侧 Redis Lua |

**Redis 宕机策略**：全链路 fail-closed（拒绝请求），防止限流失效被暴力破解。

### 9.3 熔断器配置

```yaml
circuitBreaker:
  maxRequests: 5             # 半开状态最大探测请求数
  intervalSec: 60            # 统计间隔（秒）
  timeoutSec: 10             # 熔断恢复超时（秒）
  consecutiveFailures: 5     # 连续失败触发熔断
```

---

## 十、生产环境部署清单

### 🔴 必须完成

- [ ] **实施 RBAC（基于角色的访问控制）**
      当前 `checkOwnership()` 仅校验"本人操作本人"
      建议：添加 `role` 字段，支持 `admin`/`user` 角色，ListUser 等接口需管理员权限

- [ ] **集成 HashiCorp Vault**
      代码已包含 Vault provider（`pkg/secrets/vault.go`），需在生产环境部署 Vault 并配置 KV secrets engine

- [ ] **审计日志**
      敏感操作（登录、注册、更新、删除）应记录审计日志（用户 ID、操作类型、IP、时间戳）

### 🟡 推荐完成

- [ ] **基础设施 TLS**
      Etcd/Redis/MySQL 连接均未启用 TLS，内网部署可接受，跨网络建议开启

- [ ] **gRPC 层限流**
      当前限流仅在 gateway（HTTP 层），建议 user 服务也实施限流防止绕过 gateway 的直接访问

- [ ] **HSTS Preload**
      当前 HSTS 仅 prod 模式启用，建议添加 `preload` 子指令并提交 [hstspreload.org](https://hstspreload.org)

### ✅ 已完成

- [x] 密码安全：bcrypt cost=12，强密码策略
- [x] JWT 安全：算法混淆防护、密钥强度验证（prod≥64字符）、audience/issuer 支持
- [x] 双令牌机制：Access + Refresh 独立 secret，一次性轮换防重放
- [x] 时序攻击防护：dummy bcrypt 防止用户枚举
- [x] Fail-Closed 策略：全链路 Redis 故障时拒绝请求
- [x] 原子 Redis 操作：Lua 脚本保证限流/锁定/Token 轮换/吊销的原子性
- [x] PII 脱敏：手机号/邮箱/日志/错误信息全面脱敏
- [x] SQL 注入防护：参数化查询 + Update 白名单
- [x] 安全响应头：CSP/HSTS/X-Frame-Options/nosniff
- [x] 服务发现安全：service/method 名称正则白名单
- [x] CORS 精确匹配：禁止通配符子域名
- [x] IP 伪造防护：X-Forwarded-For 格式校验
- [x] OTEL TLS：endpoint scheme 自动判断是否启用 TLS
- [x] 健康检查脱敏：不暴露依赖名称和拓扑信息
- [x] 7 轮安全审计收敛，gateway/user/pkg 全部零新问题

---

## 十一、常见问题

**Q：gateway 启动报 `proto file not found`？**
A：检查 `gateway/conf/config.yaml` 中的 `protoBase` 路径，以及 proto 文件是否存在于 `{protoBase}/{service}/api/{service}.proto`。

**Q：`make clean` 会不会删掉手写文件？**
A：不会。`clean` 只删除 `kitex_gen/` 目录。

**Q：go.work 版本不对怎么办？**
A：运行 `go version` 确认本机 Go 版本，将 `go.work` 第一行改为对应版本（如 `go 1.26.1`）。

**Q：Docker 构建时报找不到 go.work？**
A：Dockerfile 以项目根目录 `shopmicro/` 作为 build context，确保 `docker build -f gateway/Dockerfile .` 在项目根目录执行。

**Q：熔断器触发后如何恢复？**
A：连续失败达到 `consecutiveFailures` 次后熔断，`timeoutSec` 秒后进入半开状态，允许 `maxRequests` 个探测请求，成功则恢复。

---

## 十二、Make 命令

```bash
make gen       # 重新生成 Kitex 代码（user 服务）
make user      # 启动 user 服务（:8888）
make gateway   # 启动 gateway（:8080）
make tidy      # 整理所有模块依赖
make clean     # 清理 kitex_gen/ 目录
```

---

## 十三、紧急事件响应

**JWT Secret 泄露**：生成新 secret → 更新 `JWT_SECRET` → 重启 gateway → 所有 token 失效

**Redis 被入侵**：更换密码 → 更新 `REDIS_PASSWORD` → 重启所有服务 → 所有用户需重新登录

**数据库被入侵**：隔离服务器 → 从备份恢复 → 强制所有用户重置密码
