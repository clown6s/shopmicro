# shopmicro 开发指南

> 技术栈：Hertz (HTTP 网关) + Kitex (gRPC RPC) + Protobuf + Etcd (服务发现) + MySQL + OpenTelemetry

## 项目结构

```
shopmicro/
├── go.work                  # Go workspace 联合开发（需与机器 Go 版本一致）
├── Makefile                 # 构建/运行命令
│
├── user/                    # User 微服务（独立 go.mod）
│   ├── main.go              # Kitex gRPC 服务入口 :8888（手写）
│   ├── handler.go           # RPC 业务逻辑实现（手写）
│   ├── kitex_gen/           # Kitex 自动生成（勿手动修改）
│   ├── api/
│   │   └── user.proto       # Protobuf IDL 定义
│   ├── dal/                 # 数据访问层
│   └── conf/                # 配置
│
├── gateway/                 # HTTP 网关（独立 go.mod）
│   ├── main.go              # Hertz HTTP 入口 :8080
│   ├── proxy/
│   │   └── proxy.go         # 通用 HTTP→gRPC 代理（所有服务共用）
│   ├── middleware/          # 中间件（CORS、JWT 校验、熔断、限流）
│   └── conf/
│
└── pkg/                     # 共享模块（被 gateway 和 user 共同依赖）
    ├── logger/              # zap 日志封装
    ├── errcode/             # 统一业务错误码
    ├── response/            # 统一 HTTP 响应封装
    ├── jwt/                 # JWT 解析器
    ├── redis/               # Redis 客户端
    └── otel/                # OpenTelemetry 初始化
```

---

## 一、网关架构

gateway 使用**通用代理模式**：不写死每个服务的 HTTP handler，而是通过 proto 文件动态生成 Kitex generic client，所有服务共用一套 HTTP→gRPC 转发逻辑。

```
HTTP 请求 ──→ gateway ──→ etcd 服务发现 ──→ Kitex Generic Client ──→ gRPC 服务
              │                                    │
              ├── JWT 认证                         ├── proto 文件
              ├── 方法白名单                        └── 泛序列化
              ├── 熔断器（per-service）
              └── Redis 限流
```

### 1.1 核心配置（gateway/main.go）

```go
p := proxy.New(
    proxy.WithJWT(cfg.JWT.Secret),              // JWT 校验
    proxy.WithRedis(rdb),                       // Token 黑名单 / 限流
    proxy.WithPublicMethods("RegisterUser", "Login"),  // 公开接口（无需登录）

    // 方法白名单：未列出的服务/方法 → 403
    proxy.WithAllowedMethods("user",
        "RegisterUser", "Login", "Logout",
        "UpdateUser", "DeleteUser", "ListUser",
    ),
)

// 启动时：从 etcd 发现已有服务并注册 generic client
p.DiscoverAndRegister(r, cfg.Etcd.Addrs, cfg.ProtoBase)

// 运行时：监听 etcd 新服务上线，自动注册
p.WatchServices(watchCtx, r, cfg.Etcd.Addrs, cfg.ProtoBase)
```

### 1.2 proto 文件路径规则

```
{protoBase}/{service}/api/{service}.proto

# 例如 protoBase = "../" 时：
# user 服务 → ../user/api/user.proto
```

protoBase 在 `gateway/conf/config.yaml` 中配置。注意：proto 文件必须真实存在于该路径，否则网关启动时会报错（fail-fast）。

### 1.3 请求路由

```
POST/GET/PUT/DELETE /api/v1/:service/:method
Authorization: Bearer {token}
```

- `:service` 对应 proto service 名称（如 `user`）
- `:method` 对应 proto rpc 方法名（如 `Login`）
- 请求体 JSON，gateway 用 JSONPbGeneric 序列化为 Protobuf 后发给 gRPC 服务

---

## 二、添加新接口（完整流程）

以给 User 服务添加 `SearchUser`（按姓名搜索）接口为例。

### 2.1 修改 proto 文件

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

### 2.2 重新生成 Kitex 代码

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

### 2.3 实现 RPC 业务逻辑

在 `user/handler.go` 中填充 `SearchUser` 方法：

```go
func (s *UserServiceImpl) SearchUser(ctx context.Context, req *v1.SearchUserReq) (resp *v1.SearchUserResp, err error) {
    users, total, err := userDal.SearchByName(ctx, req.RealName, req.Page, req.PageSize)
    if err != nil {
        logger.Errorf("search users failed: %v", err)
        return nil, err
    }

    list := make([]*v1.UserInfo, 0, len(users))
    for _, u := range users {
        list = append(list, modelToProto(u))
    }

    return &v1.SearchUserResp{Users: list, Total: total}, nil
}
```

### 2.4 添加数据访问方法

在 `user/dal/user.go` 添加查询方法：

```go
func (d *UserDal) SearchByName(ctx context.Context, name string, page, pageSize int32) ([]*model.User, int64, error) {
    var users []*model.User
    var total int64

    db := d.db.WithContext(ctx).Model(&model.User{}).
        Where("real_name LIKE ? AND deleted_at IS NULL", "%"+name+"%")

    if err := db.Count(&total).Error; err != nil {
        return nil, 0, err
    }

    offset := (page - 1) * pageSize
    err := db.Offset(int(offset)).Limit(int(pageSize)).Order("id DESC").Find(&users).Error
    return users, total, nil
}
```

### 2.5 在网关注册新方法

**无需写新的 HTTP handler**，只需在 `gateway/main.go` 的 `WithAllowedMethods` 中添加：

```go
proxy.WithAllowedMethods("user",
    "RegisterUser", "Login", "Logout",
    "UpdateUser", "DeleteUser", "ListUser",
    "SearchUser",   // ← 新增这一行
),
```

### 2.6 验证

```bash
# 启动服务后测试
curl -X POST http://localhost:8080/api/v1/user/SearchUser \
  -H "Authorization: Bearer {token}" \
  -H "Content-Type: application/json" \
  -d '{"real_name":"张","page":1,"page_size":10}'
```

---

## 三、添加新服务（完整流程）

以添加 `product` 服务为例。

### 3.1 创建服务目录

```
product/
├── api/
│   └── product.proto        # Protobuf IDL
├── handler.go               # 手写，kitex 不会覆盖
├── main.go                  # 手写，kitex 不会覆盖
├── dal/
│   └── product.go
├── model/
│   └── product.go
└── conf/
    └── config.yaml
```

### 3.2 创建/更新 go.work

使用 go.work 统一管理多模块，不需要 `replace` 指令。

**第一次创建**（在项目根目录的上一级执行）：

```bash
cd ..
go work init \
  ./shopmicro/gateway \
  ./shopmicro/pkg \
  ./shopmicro/product \
  ./shopmicro/user
```

**已有 go.work**，只需把新服务加进去：

```bash
go work use ./shopmicro/product
```

> 注意：`go.work` 第一行的 go 版本号必须与本机 `go version` 一致。

### 3.3 编写 proto 文件

`product/api/product.proto`（参考 `user/api/user.proto`），
定义好 service、message 和 rpc 方法。

### 3.4 生成 Kitex 代码

```bash
cd product
kitex -module shopmicro/product -service product -type protobuf api/product.proto
```

### 3.5 编写手写文件

- `product/handler.go`：实现各个 RPC 方法的业务逻辑
- `product/main.go`：初始化 data 层、注册到 etcd、启动 Kitex server（参考 `user/main.go`）

### 3.6 部署 proto 文件到 gateway 可读路径

proto 文件需要能被 gateway 读取，路径格式为：

```
{gateway.conf.protoBase}/{service}/api/{service}.proto
```

假设 `gateway/conf/config.yaml` 中 `protoBase: "../"`，则需要把 proto 放到：

```
shopmicro/
└── product/
    └── api/
        └── product.proto      ← gateway 以 ../product/api/product.proto 访问
```

### 3.7 在 gateway 中注册新服务

编辑 `gateway/main.go`：

```go
p := proxy.New(
    proxy.WithJWT(cfg.JWT.Secret),
    proxy.WithRedis(rdb),
    proxy.WithPublicMethods(...),

    // 已有 user 白名单...
    proxy.WithAllowedMethods("user", ...),

    // 新增 product 白名单
    proxy.WithAllowedMethods("product",
        "CreateProduct",
        "GetProduct",
        "UpdateProduct",
        "DeleteProduct",
        "ListProduct",
    ),
)
```

如果某个方法（如商品查询）不需要登录认证，改为：

```go
proxy.WithPublicMethods("PublicProductMethod")
```

### 3.8 启动并验证

```bash
# 终端 1：启动 user
make user

# 终端 2：启动 gateway（product 服务注册到 etcd 后，gateway 会自动发现）
make gateway

# 验证
curl http://localhost:8080/api/v1/product/GetProduct \
  -H "Authorization: Bearer {token}" \
  -d '{"id":1}'
```

---

## 四、代码生成详解

### 4.1 Kitex 命令

```bash
cd user
kitex -module shopmicro/user -service user -type protobuf api/user.proto
```

| 参数 | 说明 |
|------|------|
| `-module` | Go 模块名，对应 go.mod 中的 module |
| `-service` | 服务名，生成服务端骨架（handler.go、main.go） |
| `-type protobuf` | 使用 Protobuf 协议 |

### 4.2 生成的文件

```
user/
├── kitex_gen/api/user/v1/
│   ├── user.pb.go                    # Protobuf 消息（Go 结构体）
│   └── userservice/
│       ├── client.go                 # gRPC 客户端接口
│       ├── server.go                 # gRPC 服务端接口
│       └── userservice.go            # 序列化/反序列化
├── handler.go                        # 服务端骨架（**会被覆盖**）
├── main.go                           # 启动入口（**会被覆盖**）
└── kitex_info.yaml                   # Kitex 元信息
```

### 4.3 注意事项

- `handler.go` 和 `main.go` 每次 `kitex` 都会覆盖，业务代码建议写在独立的 `biz/` 层，或生成后及时合并备份
- 如只需更新 message 而不更新服务骨架：用 `protoc` 单独生成 pb.go

---

## 五、配置速查

| 要改什么 | 改哪个文件 |
|---------|-----------|
| 网关端口 | `gateway/conf/config.yaml` → `server.addr` |
| 网关 protoBase | `gateway/conf/config.yaml` → `protoBase` |
| 网关 Etcd 地址 | `gateway/conf/config.yaml` → `etcd.addrs` |
| 网关 JWT Secret | `gateway/conf/config.yaml` → `jwt.secret` |
| user 服务端口 | `user/conf/config.yaml` → `server.addr` |
| user MySQL 连接 | `user/conf/config.yaml` → `mysql.dsn` |
| 新增配置字段 | 对应服务 `conf/conf.go` 结构体 + `config.yaml` |

---

## 五、OpenTelemetry 埋点

### 5.1 已自动埋点的链路

| 层级 | 组件 | 埋点方式 |
|------|------|---------|
| Gateway HTTP | Hertz 请求 | `hertztracing.ServerMiddleware` 自动创建根 span |
| Gateway → RPC | Kitex gRPC 调用 | `kotelkitex.NewClientSuite` 自动 |
| User RPC | Kitex Server 处理器 | `kotelkitex.NewServerSuite` 自动 |
| MySQL | SQL 查询 | `otelsql`（全部 span 类型开启） |
| Redis | Redis 调用 | `redisotel.InstrumentTracing` 自动 |

整条请求链路（HTTPS → Gateway → gRPC → DB/Redis）的 trace ID 自动串联。

### 5.2 手动埋点：在 handler 中添加 span

业务逻辑（`user/handler.go`）默认没有 span，需要手动开启：

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/trace"
)

func (s *UserServiceImpl) SearchUser(ctx context.Context, req *v1.SearchUserReq) (resp *v1.SearchUserResp, err error) {
    // 从 context 获取 tracer（全局已初始化，直接用 otel.Tracer()）
    tracer := otel.Tracer("user-handler")

    // 开启子 span，跟随 HTTP→gRPC 的根 span
    ctx, span := tracer.Start(ctx, "UserService.SearchUser")
    defer span.End()

    // 添加业务属性（key-value metadata）
    span.SetAttributes(
        attribute.Int64("user.query.page", int64(req.Page)),
        attribute.Int64("user.query.page_size", int64(req.PageSize)),
        attribute.String("user.query.real_name", req.RealName),
    )

    // 业务逻辑
    users, total, err := userDal.SearchByName(ctx, req.RealName, req.Page, req.PageSize)
    if err != nil {
        // 记录错误，自动标记 span 状态为 Error
        span.RecordError(err)
        return nil, err
    }

    span.SetAttributes(attribute.Int64("user.result.total", total))

    list := make([]*v1.UserInfo, 0, len(users))
    for _, u := range users {
        list = append(list, modelToProto(u))
    }

    return &v1.SearchUserResp{Users: list, Total: total}, nil
}
```

### 5.3 在中间件中埋点

在 gateway 中间件里加 span，例如记录关键决策点：

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/trace"
)

func JWTMiddleware() app.HandlerFunc {
    tracer := otel.Tracer("gateway")
    return func(ctx context.Context, c *app.RequestContext) {
        ctx, span := tracer.Start(ctx, "JWT.Validate")
        defer span.End()

        tokenStr, ok := bearerToken(string(c.GetHeader("Authorization")))
        if !ok {
            span.SetAttributes(attribute.String("jwt.result", "missing"))
            span.RecordError(errors.New("missing token"))
            c.Abort()
            return
        }

        claims, err := jwtParser.Parse(tokenStr)
        if err != nil {
            span.SetAttributes(attribute.String("jwt.result", "invalid"))
            span.RecordError(err)
            c.Abort()
            return
        }

        span.SetAttributes(
            attribute.String("jwt.result", "valid"),
            attribute.Int64("user.id", claims.UserID),
        )
        c.Next(ctx)
    }
}
```

### 5.4 Span 常见操作

| 操作 | 代码 |
|------|------|
| 开启子 span | `ctx, span := tracer.Start(ctx, "operation.name")` |
| 添加属性 | `span.SetAttributes(attribute.String("key", "value"))` |
| 记录错误 | `span.RecordError(err)` |
| 添加日志事件 | `span.AddEvent("cache miss", trace.WithAttributes(...))` |
| 结束 span | `defer span.End()` |

### 5.5 查看 trace

1. 启动 collector（如 Jaeger）：`docker run -p 16686:16686 -p 4317:4317 jaegertracing/all-in-one`
2. 设置环境变量：`OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317`
3. 访问 Jaeger UI：`http://localhost:16686`
4. Gateway 响应头会返回 `X-Trace-Id`，在 Jaeger 里直接搜索 trace ID

### 5.6 配置项

| 环境变量 | 说明 | 默认值 |
|---------|------|--------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Collector 地址 | `localhost:4317` |
| `OTEL_TRACE_SAMPLE_RATE` | 采样率 0.0–1.0 | `0.1`（生产），`1.0`（dev 模式） |
| `OTEL_EXPORTER_OTLP_INSECURE` | 是否跳过 TLS 验证 | `true`（非生产建议配证书） |

---

## 六、常见问题

**Q：gateway 启动报 `proto file not found`？**  
A：检查 `gateway/conf/config.yaml` 中的 `protoBase` 路径，以及 proto 文件是否真实存在于 `{protoBase}/{service}/api/{service}.proto`。

**Q：`make clean` 会不会删掉手写文件？**  
A：不会。`clean` 只删除 `kitex_gen/` 目录，`handler.go` 和 `main.go` 是手写的，kitex 生成时不会覆盖它们。

**Q：go.work 版本不对怎么办？**  
A：运行 `go version` 确认本机 Go 版本，将 `go.work` 第一行改为对应版本（如 `go 1.26.1`）。

---

## 七、安全架构

> ⚠️ **生产环境部署前必读**

### 7.1 已修复的安全漏洞

经过全面安全审计，以下漏洞已被修复：

| 编号 | 严重等级 | 问题描述 | 修复状态 |
|------|----------|------------|------------|
| P1-01 | 🔴 高危 | Rate Limit IP 欺骗（限流绕过） | ✅ 已修复 |
| P1-02 | 🔴 高危 | 日志注入（`\n` `\r` 注入假日志） | ✅ 已修复 |
| P1-03 | 🔴 高危 | OTEL SQL 语句泄露到追踪系统 | ✅ 已修复 |
| P1-04 | 🔴 高危 | ListUser 缺少管理员权限检查（权限绕过） | ✅ 已修复 |
| P2-03 | 🟡 中危 | Redis 宕机时限流降级 fail-closed | ✅ 已修复 |

详细修复方案和代码，参见项目根目录的 `SECURITY_AUDIT_REPORT.md`。

---

### 7.2 已实施的安全最佳实践

| 安全特性 | 实现位置 | 说明 |
|---------|-----------|------|
| **密码哈希** | `user/handler/handler.go` | 使用 bcrypt 算法，cost=12 |
| **JWT 安全** | `pkg/jwt/jwt.go` | HS256 签名，防止算法混淆攻击，密钥长度验证（≥32字符） |
| **参数化查询** | `user/data/user.go` | 所有 SQL 查询使用 `?` 占位符，防止 SQL 注入 |
| **输入验证** | `user/handler/handler.go` | 手机号格式校验（中国手机号）、密码强度策略 |
| **分布式限流** | `gateway/middleware/ratelimit.go` | Redis 滑动窗口算法，支持端点级限流配置 |
| **熔断器** | `gateway/middleware/breaker.go` | 基于 `gobreaker` 的每服务熔断器 |
| **Panic 恢复** | `gateway/main.go`, `user/main.go` | Hertz/Kitex 内置 recovery 中间件 |
| **CORS 保护** | `gateway/middleware/cors.go` | 支持凭据时禁止通配符 Origin（浏览器安全策略） |
| **TOCTOU 修复** | `user/handler/handler.go` | 依赖 DB 唯一索引 + `isDuplicateKeyError` 检查，移除竞态条件 |
| **Token 管理** | `user/handler/handler.go` | Redis SET 存储多设备 token，支持 logout 单点注销 |

---

### 7.3 生产环境部署清单

#### 🔵 高优先级（必须完成）

- [ ] **实施 RBAC（基于角色的访问控制）**  
      当前 `checkAdmin()` 硬编码用户 ID 1 为管理员  
      建议：创建 `role` 表，支持 `admin`/`user` 角色  
      文件：`user/handler/handler.go` (line 224)

- [ ] **集成 HashiCorp Vault**  
      代码已包含 Vault provider 实现（`pkg/secrets/vault.go`）  
      需要：在生产环境部署 Vault 并配置 KV secrets engine  
      文件：`gateway/conf/config.yaml`, `user/conf/config.yaml`

- [ ] **添加安全响应头中间件**  
      缺少 `Content-Security-Policy`, `X-Frame-Options`, `X-Content-Type-Options` 等安全头  
      建议：在 gateway 中添加 security headers 中间件

#### 🔵 中优先级（推荐完成）

- [ ] **实施审计日志**  
      敏感操作（用户更新、删除）应记录审计日志  
      建议：创建 `audit_log` 表，记录操作类型、用户 ID、时间戳、IP 地址

- [ ] **gRPC 层限流**  
      当前限流仅在 gateway（HTTP 层）实施  
      建议：在 user 服务（gRPC 层）也实施限流，防止绕过 gateway 的直接访问

- [ ] **输入验证增强**  
      当前仅验证 `phone` 和 `password`  
      建议：对所有用户输入实施严格验证（email 格式、real_name 长度、remark 内容等）

---

### 7.4 密钥管理

#### ✅ 已实施的保护

- `.env` 文件已在 `.gitignore` 中，不会被提交到 git
- 项目提供 `.env.example` 模板文件，包含安全警告
- 支持 HashiCorp Vault 作为密钥管理后端（代码已实现）

#### 🔑 环境变量清单

启动服务前，必须设置以下环境变量（或写入 `.env` 文件）：

```bash
# MySQL
MYSQL_DSN=root:your_password@tcp(10.211.55.5:3306)/shop_micro?charset=utf8mb4&parseTime=True&loc=Local

# Redis
REDIS_ADDR=10.211.55.5:6379
REDIS_PASSWORD=your_redis_password

# JWT (至少32字符，生产环境必须使用强密钥)
JWT_SECRET=your-jwt-secret-key-at-least-32-chars

# OTEL
OTEL_EXPORTER_OTLP_ENDPOINT=10.211.55.5:4317
```

#### ⚠️ 安全警告

1. **禁止提交真实密钥到 git**  
   `.env` 文件包含明文密钥，已被 `.gitignore` 排除  
   验证：运行 `git status --short gateway/.env user/.env`，应该无输出

2. **生产环境使用 Vault**  
   在 `config.yaml` 中配置 Vault 地址和 token：
   ```yaml
   vault:
     address: "http://your-vault-server:8200"
     token: "hvs.your-vault-token"
     mountPath: "secret"
   ```

3. **定期轮换密钥**  
   - JWT secret：重启 gateway 后生效（会使所有 token 失效）
   - Redis password：更新 Redis 配置后重启 Redis
   - MySQL password：更新 `MYSQL_DSN` 后重启 user 服务

---

### 7.5 OTEL 配置（安全更新）

⚠️ **重要变更**：为防止 SQL 语句泄露到追踪系统，`user/data/data.go` 中已禁用 SQL 语句追踪：

```go
spanOpts := otelsql.SpanOptions{
    OmitConnQuery:        true,  // 不创建 sql.conn.query span
    OmitRows:             true,  // 不创建 sql.rows span
    OmitConnPrepare:      true,  // 不创建 sql.conn.prepare span
    OmitConnectorConnect: true,  // 不创建 sql.connector.connect span
    OmitConnResetSession: true,  // 不创建 reset session span
    DisableQuery:         true,  // 屏蔽 db.statement（防泄露）
}
```

**生产环境建议**：

| 环境变量 | 说明 | 推荐值 |
|---------|------|--------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Collector 地址 | 生产环境使用真实地址，启用 TLS |
| `OTEL_TRACE_SAMPLE_RATE` | 采样率 0.0–1.0 | 生产环境 `0.1`（10%），dev 模式 `1.0` |
| `OTEL_EXPORTER_OTLP_INSECURE` | 是否跳过 TLS 验证 | 生产环境 **必须** 设为 `false` |

---

### 7.6 安全头配置（待实施）

建议在 gateway 中添加安全响应头中间件：

```go
// TODO: 在 gateway/middleware/ 创建 security_headers.go
func SecurityHeaders() app.HandlerFunc {
    return func(ctx context.Context, c *app.RequestContext) {
        c.Header("Content-Security-Policy", "default-src 'self'")
        c.Header("X-Frame-Options", "DENY")
        c.Header("X-Content-Type-Options", "nosniff")
        c.Header("X-XSS-Protection", "1; mode=block")
        c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
        c.Next(ctx)
    }
}
```

---

### 7.7 速率限制配置

Gateway 使用 Redis 滑动窗口算法实施分布式限流，配置在 `gateway/conf/config.yaml`：

```yaml
# 分布式限流配置 (Redis 滑动窗口)
rateLimit:
  limit: 100          # 窗口内最大请求数
  windowSec: 60       # 滑动窗口时长（秒）
  
  # 端点级限流 — Login 端点更严格
  endpoints:
    user/Login:
      limit: 5            # 每分钟每IP最多5次登录尝试
      windowSec: 60
```

**限流策略**：

| 端点 | 限流规则 | 说明 |
|-------|----------|------|
| 全局 | 100次/分钟/IP | 默认规则 |
| `user/Login` | 5次/分钟/IP | 防止暴力破解 |
| 其他敏感端点 | 可配置 | 在 `endpoints:` 下添加 |

**Redis 宕机处理**：  
限流器配置为 **fail-closed** 模式 —— Redis 不可用时**拒绝请求**，而不是降级到本地限流（防止分布式限流失效）。

---

### 7.8 审计日志（待实施）

建议创建 `audit_log` 表，记录敏感操作：

```sql
CREATE TABLE audit_log (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT NOT NULL,
    action VARCHAR(50) NOT NULL,
    target_type VARCHAR(50),
    target_id BIGINT,
    ip_address VARCHAR(45),
    user_agent TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_user_id (user_id),
    INDEX idx_action (action),
    INDEX idx_created_at (created_at)
);
```

需要记录的操作：

- 用户登录（`Login`）
- 用户注册（`RegisterUser`）
- 更新用户信息（`UpdateUser`）
- 删除用户（`DeleteUser`）
- 管理员操作（`ListUser`）

---

### 7.9 安全测试清单

部署前建议完成以下安全测试：

- [ ] **认证绕过测试**  
      尝试访问需要登录的接口（如 `UpdateUser`） without token  
      期望：返回 401 Unauthorized

- [ ] **权限提升测试**  
      普通用户尝试访问 `ListUser` 接口  
      期望：返回 403 Forbidden

- [ ] **限流有效性测试**  
      对 `user/Login` 发送超过 5次/分钟的请求  
      期望：返回 429 Too Many Requests

- [ ] **SQL 注入测试**  
      在 `phone` 或 `real_name` 参数中插入 SQL 注入 payload  
      期望：参数化查询阻止注入，返回参数错误

- [ ] **XSS 测试**  
      在 `real_name` 或 `remark` 中插入 `<script>alert(1)</script>`  
      期望：输出被 HTML 转义

- [ ] **JWT 安全测试**  
      尝试修改 JWT header 的 `alg` 为 `none` 或使用 RSA 公钥签名  
      期望：`pkg/jwt/jwt.go` 中的算法白名单阻止非 HMAC 签名

---

### 7.10 紧急事件响应

**如果怀疑 JWT secret 泄露**：

1. 生成新的 JWT secret（至少 32 字符随机字符串）
2. 更新 `JWT_SECRET` 环境变量或 Vault
3. 重启 gateway 服务
4. 所有现有 token 将立即失效，用户需要重新登录

**如果怀疑 Redis 被入侵**：

1. 立即更换 Redis password
2. 更新 `REDIS_PASSWORD` 环境变量或 Vault
3. 重启 gateway 和 user 服务
4. 所有用户需要重新登录（token 失效）

**如果怀疑数据库被入侵**：

1. 隔离受影响的服务器
2. 从备份恢复数据库
3. 强制所有用户重置密码（bcrypt hash 可能已泄露）
4. 审计 `audit_log`（如果已实施）

---

## 八、完整安全审计报告

详细的安全审计结果、修复方案和代码，参见项目根目录的 `SECURITY_AUDIT_REPORT.md`。

报告内容：

- 所有发现的安全漏洞及修复方案
- 已实施的安全最佳实践清单
- 剩余技术债和改进建议
- 编译验证结果

---

**文档结束**  
如有安全问题，请联系项目维护者。
