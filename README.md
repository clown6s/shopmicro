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
kitex -module shopmicro/user -service user -type protobuf api/district.proto
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

> **以 `district`（行政区）服务为例，从零开始逐步演示。** 每一步都有对应的命令和代码。

---

### 4.1 创建服务目录结构

```bash
cd shopmicro
mkdir -p district/api district/handle district/data district/module district/conf
```

创建后的目录结构：

```
district/
├── api/                    # Protobuf IDL 定义
│   └── district.proto      # ★ 第 4.2 步编写
├── handle/                 # RPC 业务逻辑实现
│   └── handler.go          # ★ 第 4.6 步编写
├── data/                   # 数据访问层
│   ├── data.go             # MySQL + Redis 初始化（★ 第 4.5 步）
│   └── district.go         # CRUD 操作（★ 第 4.5 步）
├── module/                 # 领域模型（可选，纯数据结构）
│   └── district.go         # 领域模型定义
├── conf/
│   ├── conf.go             # 配置结构体 + 校验（★ 第 4.4 步）
│   └── config.yaml         # 配置文件（★ 第 4.3 步）
├── main.go                 # 服务入口（★ 第 4.7 步）
├── go.mod                  # Go 模块文件
├── kitex_gen/              # Kitex 自动生成（勿手动修改）
└── kitex_info.yaml         # Kitex 项目信息
```

---

### 4.2 编写 proto 文件

创建 `district/api/district.proto`：

```protobuf
syntax = "proto3";

package api.district.v1;

// ★ go_package 必须与最终 go.mod 的 module 路径一致
option go_package = "shopmicro/district/kitex_gen/api/district/v1";

service DistrictService {
    // 每个 rpc 方法对应一个接口，按需添加
    rpc GetProvinceTree (GetProvinceTreeReq) returns (GetProvinceTreeResp);
}

message GetProvinceTreeReq {
  string adCode = 1;
}

message GetProvinceTreeResp {
  uint32 code = 1;
  string message = 2;
  repeated DistrictTree data = 3;
}

message DistrictTree {
  int64 id = 1;
  string parentAdCode = 2;
  string adCode = 3;
  string name = 4;
  string level = 5;
  string center = 6;
  repeated DistrictTree children = 7;
}
```

> ⚠️ **注意**：
> - `go_package` 格式：`shopmicro/{服务名}/kitex_gen/api/{服务名}/v1`，必须与第 4.3 步的 `go.mod` module 名匹配
> - service 名使用 PascalCase（如 `DistrictService`），rpc 方法名也用 PascalCase（如 `GetProvinceTree`），Kitex 会自动转换
> - message 字段编号建议预留空间（`reserved 2 to 99`），方便后续扩展

---

### 4.3 初始化 Go 模块 + 生成 Kitex 代码

#### ① 初始化 go.mod

```bash
cd district
go mod init shopmicro/district
```

生成的 `go.mod` 内容：

```
module shopmicro/district

go 1.26.1
```

> ⚠️ **注意**：Go 版本号须与本机 `go version` 一致，且与 `go.work` 第一行一致。

#### ② 注册到 workspace

```bash
# 回到项目根目录
cd ..
go work use ./district
```

确认 `go.work` 已包含新模块：

```go
use (
	./gateway
	./pkg
	./user
	./district        ← 新增这一行
)
```

#### ③ 执行 Kitex 代码生成

```bash
cd district
kitex -module shopmicro/district -service district -type protobuf api/district.proto
```

执行后会自动生成以下内容：

```
district/
├── kitex_gen/                    # 自动生成（勿手动修改）
│   └── api/district/v1/
│       ├── district.pb.go        # Protobuf 序列化代码
│       ├── district_grpc.pb.go   # gRPC stub
│       └── districtservice/      # Kitex server/client 骨架
│           ├── server.go         # NewServer() / RegisterService()
│           ├── client.go         # 客户端工厂方法
│           └── districtservice.go# 服务接口定义
├── handler.go                    # ⚠️ 自动生成，会被覆盖！参考下面的处理方式
└── main.go                       # ⚠️ 自动生成，会被覆盖！参考下面的处理方式
```

> ⚠️ **重要注意事项**：
>
> **`kitex` 命令会覆盖已有的 `handler.go` 和 `main.go`！**
>
> 处理策略有两种，任选一种：
>
> | 策略 | 适用场景 | 操作 |
> |------|---------|------|
> | **A. 子包隔离**（推荐）| 业务逻辑较多 | 把手写代码放在 `handle/` 子包里，`handler.go` 只做转发。Kitex 每次覆盖的是空壳 `handler.go`，不影响 `handle/` 目录 |
> | B. 先生成再写 | 业务简单 | 第一次执行 kitex 后，在生成的骨架上写业务代码；后续改 proto 时先备份 `handler.go` 和 `main.go`，生成后再合并 |
>
> 本示例采用策略 A（子包隔离），这也是 `district` 和 `user` 服务实际使用的方式。

---

### 4.4 编写配置层

#### ① 创建 `conf/conf.go`

```go
package conf

import (
	"fmt"
	"strings"

	"shopmicro/pkg/conf"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/mysql"
	"shopmicro/pkg/redis"
	"shopmicro/pkg/secrets"
)

type Config struct {
	Server     conf.Server         `yaml:"server"`
	HealthAddr string              `yaml:"healthAddr"`
	Etcd       conf.Etcd           `yaml:"etcd"`
	MySQL      mysql.Config        `yaml:"mysql"`
	Redis      redis.Config        `yaml:"redis"`
	Mode       string              `yaml:"mode"`
	Vault      secrets.VaultConfig `yaml:"vault"`
}

// GetHealthAddr 返回健康检查端口，默认 :8899
func (c *Config) GetHealthAddr() string {
	if c.HealthAddr == "" {
		return ":8899"
	}
	return c.HealthAddr
}

// ApplyEnv 从环境变量覆盖敏感配置
func (c *Config) ApplyEnv() {
	// Vault 支持
	if c.Vault.Address != "" {
		if token := conf.Env("VAULT_TOKEN", ""); token != "" {
			c.Vault.Token = token
		}
		secrets.InitProvider(c.Vault)
	}
	// 环境变量覆盖
	if v := conf.Env("MYSQL_DSN", ""); v != "" { c.MySQL.DSN = v }
	if v := conf.Env("REDIS_ADDR", ""); v != "" { c.Redis.Addr = v }
	if v := conf.Env("REDIS_PASSWORD", ""); v != "" { c.Redis.Password = v }
	if v := conf.Env("SERVER_ADDR", ""); v != "" { c.Server.Addr = v }
	if v := conf.Env("ETCD_ADDRS", ""); v != "" { c.Etcd.Addrs = strings.Split(v, ",") }
}

// Validate 校验必填配置
func (c *Config) Validate() error {
	if err := c.MySQL.Validate(); err != nil { return err }

	mode := strings.ToLower(c.Mode)
	if mode != "dev" && mode != "prod" && mode != "staging" {
		return fmt.Errorf("invalid mode %q, expected dev/prod/staging", c.Mode)
	}
	if mode == "prod" && c.Redis.Password == "" {
		return fmt.Errorf("redis password required in production")
	}
	return nil
}
```

> ⚠️ **注意**：
> - `Server`、`Etcd` 类型来自 `pkg/conf`，是所有服务共享的基础配置结构
> - `MySQL.Config`、`Redis.Config`、`secrets.VaultConfig` 同理，直接复用 pkg
> - `Validate()` 中必须校验 `mode` 和生产环境密码要求

#### ② 创建 `conf/config.yaml`

```yaml
server:
  addr: ":6000"            # ★ 每个服务端口不同！user=8888, gateway=8080, district=6000

mysql:
  dsn: ""

redis:
  addr: "127.0.0.1:6379"
  password: ""
  db: 1                     # ★ 不同服务可用不同 Redis db 隔离

etcd:
  addrs:
    - "127.0.0.1:2379"

mode: "dev"

vault:
  address: ""
  token: ""
  mountPath: "secret"

healthAddr: ":8898"         # ★ 健康检查端口也要不同！user=8899, district=8898
```

> ⚠️ **注意**：
> - **端口不能冲突**：每个服务的 `server.addr` 和 `healthAddr` 都要唯一
> - 建议约定：主端口从 6000 起（district=6000, user=8888），健康检查端口从 8898 起
> - Redis `db` 在开发阶段可复用，生产环境建议各服务独立 db

---

### 4.5 编写数据访问层

#### ① 创建 `data/data.go`

```go
package data

import (
	"shopmicro/pkg/mysql"
	"shopmicro/pkg/redis"
)

type Data struct {
	DB  *sql.DB
	Rdb *redis.Client
}

func New(cfg mysql.Config, redisCfg redis.Config, mode string) (*Data, func(), error) {
	db, dbCleanup, err := mysql.New(cfg)
	if err != nil {
		return nil, nil, err
	}

	redisCfg.EnableTracing = true
	rc, redisCleanup, err := redis.New(redisCfg)
	if err != nil {
		dbCleanup()
		return nil, nil, err
	}

	cleanup := func() {
		dbCleanup()
		redisCleanup()
	}

	return &Data{DB: db, Rdb: rc}, cleanup, nil
}
```

#### ② 创建 `data/district.go`（CRUD 操作）

```go
package data

// DistrictDal 行政区数据访问对象
type DistrictDal struct {
	db *sql.DB
}

// District 返回 District 实例
func (d *Data) District() *DistrictDal {
	return &DistrictDal{db: d.DB}
}

// ListProvince 示例：查询省份列表
func (d *DistrictDal) ListProvince(ctx context.Context) ([]*module.District, error) {
	rows, err := d.db.QueryContext(ctx,
		"SELECT id, parent_ad_code, ad_code, name, level, center FROM district WHERE level = 'province'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var provinces []*module.District
	for rows.Next() {
		var m module.District
		if err := rows.Scan(&m.ID, &m.ParentAdCode, &m.AdCode, &m.Name, &m.Level, &m.Center); err != nil {
			return nil, err
		}
		provinces = append(provinces, &m)
	}
	return provinces, nil
}
```

> ⚠️ **注意**：
> - 所有 SQL 必须用参数化查询（`?` 占位符）或白名单字段映射，禁止字符串拼接防 SQL 注入
> - `data.New()` 返回 `(data, cleanup, error)`，cleanup 函数在 `main.go` 中 defer 调用
> - `mysql.New()` 和 `redis.New()` 已内置 OTEL 追踪，无需手动埋点

---

### 4.6 编写 RPC Handler（业务逻辑）

创建 `handle/handler.go`：

```go
package handle

import (
	"context"
	"shopmicro/district/data"
	v1 "shopmicro/district/kitex_gen/api/district/v1"
	"shopmicro/pkg/logger"
)

// DistrictServiceImpl 实现 IDL 定义的 DistrictService 接口
type DistrictServiceImpl struct {
	districtDal *data.DistrictDal
	rdb         *redis.Client
}

// NewDistrictHandler 创建 handler 实例（由 main.go 调用）
func NewDistrictHandler(d *data.Data) *DistrictServiceImpl {
	return &DistrictServiceImpl{
		rdb:         d.Rdb,
		districtDal: d.District(),
	}
}

// GetProvinceTree 获取省市区树形数据
func (s *DistrictServiceImpl) GetProvinceTree(ctx context.Context, req *v1.GetProvinceTreeReq) (resp *v1.GetProvinceTreeResp, err error) {
	// ★ 从 req 读参数，不要从 ctx.Value 读！（ctx.Value 取不到会 panic）
	logger.Info("GetProvinceTree called", logger.String("adCode", req.AdCode))

	resp = &v1.GetProvinceTreeResp{
		Code:    0,
		Message: "ok",
		Data:    []*v1.DistrictTree{},
	}
	// TODO: 实现具体的树形查询逻辑
	return
}
```

> ⚠️ **注意（踩坑记录）**：
>
> | 问题 | 错误写法 | 正确写法 |
> |------|---------|---------|
> | 读参数 | `ctx.Value("adCode").(string)` → **panic: nil** | `req.AdCode` |
> | 日志字段类型 | `zap.String("key", value.(string))` | `zap.String("key", req.Field)` |
>
> Kitex 生成的 `req` 对象已经包含 proto 定义的所有字段，**永远从 `req` 读取请求参数**。`ctx` 仅用于传递 metainfo（如 `user_id`、`token`），不用于传递业务参数。
>
> 如果需要读取 gateway 通过 metainfo 传来的用户信息：
> ```go
> userID := metautil.GetMetaData(ctx, "user_id")
> ```

---

### 4.7 编写 main.go（服务入口）

完整的 `main.go` 结构（对齐 user 服务模式）：

```go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	server "github.com/cloudwego/kitex/server"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	kotelkitex "github.com/kitex-contrib/obs-opentelemetry/tracing"
	"github.com/kitex-contrib/registry-etcd"

	v1 "shopmicro/district/kitex_gen/api/district/v1/districtservice"

	pkgconf "shopmicro/pkg/conf"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/otel"

	"shopmicro/district/conf"
	"shopmicro/district/data"
	"shopmicro/district/handle"
)

func Run(configPath string) error {
	// ① 加载配置
	var cfg conf.Config
	if err := pkgconf.Load(configPath, &cfg); err != nil { return err }
	cfg.ApplyEnv()
	if err := cfg.Validate(); err != nil { return err }

	// ② 初始化 logger
	logger.Init(cfg.Mode)
	defer logger.L.Sync()

	// ③ 初始化 otel tracer
	shutdownTracer := otel.InitTracer("district-service", cfg.Mode)
	defer shutdownTracer(context.Background())

	// ④ 初始化 data 层（MySQL + Redis）
	dataProvider, cleanup, err := data.New(cfg.MySQL, cfg.Redis, cfg.Mode)
	if err != nil { return err }
	defer cleanup()

	// ⑤ etcd 注册中心
	r, err := etcd.NewEtcdRegistry(cfg.Etcd.Addrs, etcd.WithDialTimeoutOpt(5*time.Second))
	if err != nil { return err }

	// ⑥ 解析监听地址
	addr, _ := net.ResolveTCPAddr("tcp", cfg.Server.Addr)

	// ⑦ 创建 Kitex Server
	svr := v1.NewServer(
		handle.NewDistrictHandler(dataProvider),
		server.WithServerBasicInfo(&rpcinfo.EndpointBasicInfo{ServiceName: "district"}),
		server.WithRegistry(r),
		server.WithServiceAddr(addr),
		server.WithMetaHandler(transmeta.MetainfoServerHandler),
		server.WithSuite(kotelkitex.NewServerSuite(kotelkitex.WithEnableGRPCMetadata())),
	)

	// ⑧ HTTP 健康检查服务器（K8s 探针用）
	// buildHealthMux 封装了 /health + /healthz 路由
	healthSrv := &http.Server{Addr: cfg.GetHealthAddr(), Handler: buildHealthMux(dataProvider), ...}
	go healthSrv.ListenAndServe()

	// ⑨ 优雅关闭（signal + WaitGroup + 超时）
	// ...（完整代码见 district/main.go）

	// ⑩ 启动
	logger.Infof("district service started on %s", cfg.Server.Addr)
	return svr.Run()
}

func main() {
	configPath := flag.String("c", "conf/config.yaml", "config file path")
	flag.Parse()
	if err := Run(*configPath); err != nil {
		log.Fatalf("run district service failed: %v", err)
	}
}
```

**main.go 关键点清单**：

| # | 组件 | 必须？ | 说明 |
|---|------|--------|------|
| ① | `pkgconf.Load` + `cfg.ApplyEnv` | ✅ | 加载 YAML + 环境变量覆盖 |
| ② | `logger.Init` | ✅ | zap 日志初始化 |
| ③ | `otel.InitTracer` | ✅ | 链路追踪，参数为 `{service名}-{service}` |
| ④ | `data.New` | ✅ | 数据层初始化，返回 cleanup 函数 |
| ⑤ | `etcd.NewEtcdRegistry` | ✅ | 注册到 etcd，gateway 才能发现 |
| ⑥ | `net.ResolveTCPAddr` | ✅ | 解析地址 |
| ⑦ | `v1.NewServer` | ✅ | Kitex 服务端，需注入 handler + registry + tracing |
| ⑧ | HTTP 健康检查 | ✅ | K8s 存活/就绪探针必需 |
| ⑨ | 优雅关闭 | ✅ | WaitGroup + signal.Notify + 超时控制 |
| ⑩ | Panic Recovery middleware | 推荐 | 防止 handler panic 导致进程崩溃 |

---

### 4.8 在网关注册新服务

编辑 `gateway/main.go`，找到 proxy 初始化位置，添加两处配置：

#### ① 方法白名单（必须有，否则返回 403）

```go
p := proxy.New(
    // ... 其他选项 ...

    // ★ district 服务方法白名单
    proxy.WithAllowedMethods("district",
        "GetProvinceTree",
        // 后续新增方法在这里追加
    ),
)
```

> ⚠️ **关键规则**：
> - `WithAllowedMethods` 一旦被调用过一次（哪怕只有一个服务），**所有未注册的服务都会返回 403**
> - 每新增一个 RPC 方法，都必须在此处追加，否则该方法返回 `"access denied"` 403

#### ② 公开接口白名单（不需要登录的方法）

如果某个方法允许未登录用户访问，加到 `WithPublicMethods` 中：

```go
proxy.WithPublicMethods(
    "RegisterUser",
    "Login",
    "RefreshToken",
    "GetProvinceTree",     // ★ 新增：district 不需要登录
),
```

> ⚠️ **注意**：`WithPublicMethods` 是全局的（跨服务），只需写方法名，不带服务名前缀。

#### ③ Gateway Dockerfile 拷贝 proto 文件

如果是 Docker 部署，还需在 `gateway/Dockerfile` 中添加：

```dockerfile
# 拷贝 district 服务的 proto 文件到容器内
COPY --from=builder /build/district/api /app/service-protos/district/api
```

gateway 启动时会根据 `protoBase` 路径读取 proto 文件来创建 generic client，**proto 文件不存在会导致启动失败**（fail-fast）。

---

### 4.9 Makefile 更新（可选）

在项目根目录 `Makefile` 中添加新服务的构建/运行命令：

```makefile
# 新增 district 相关命令
district:
	cd district && go run main.go

gen-district:
	cd district && kitex -module shopmicro/district -service district -type protobuf api/district.proto
```

---

### 4.10 验证全流程

#### ① 启动依赖服务

```bash
# 确保 etcd、MySQL、Redis 正常运行
etcd --listen-client-urls http://127.0.0.1:2379 &
```

#### ② 启动 district 服务

```bash
cd district && go run main.go
# 输出：district service started on :6000 (health: :8898)
```

验证健康检查：

```bash
curl http://localhost:8898/health
# → {"status":"ok"}
```

#### ③ 重启 gateway

```bash
# Ctrl+C 停掉旧 gateway，重新运行
cd gateway && go run main.go
# 输出中应看到：service registered: district (proto: ../district/api/district.proto)
```

#### ④ 发送测试请求

```bash
# 公开接口，无需 token
curl http://localhost:8080/api/v1/district/getProvinceTree \
  -H "Content-Type: application/json" \
  -d '{"adCode":""}'
# → {"code":0,"message":"ok","data":[]}
```

#### ⑤ 检查日志

- Gateway access log 应显示 `status: 200`（不再是 403/401/500）
- District 服务日志应输出 `GetProvinceTree called`
- Jaeger 中应能看到完整链路：HTTP(gateway) → gRPC(district)

---

### 4.11 常见错误速查表

| 错误现象 | 原因 | 解决方法 |
|----------|------|---------|
| **403 Forbidden** | 未在 `WithAllowedMethods` 注册该服务 | 在 `gateway/main.go` 添加 `proxy.WithAllowedMethods("district", ...)` |
| **401 Unauthorized** | 方法不在 `WithPublicMethods` 中，走了 JWT 校验 | 在 `WithPublicMethods` 中添加方法名 |
| **500 / interface conversion panic** | `ctx.Value("xxx").(type)` 取到 nil 直接断言炸了 | 改为从 `req` 对象读参数 |
| **Gateway 启动报 `proto file not found`** | proto 文件路径与 `protoBase` 不匹配 | 检查 `config.yaml` 中 `protoBase` 是否指向正确根目录 |
| **`go work use` 后编译报找不到 pkg`** | `go.work` 文件路径用了相对路径但实际不正确 | 确认 `go.work` 中路径格式（Windows 用 `./district` 或绝对路径） |
| **服务注册到 etcd 但 gateway 发现不到** | ServiceName 不匹配 | Kitex `server.WithServerBasicInfo.ServiceName` 必须与 URL 中的 `:service` 一致（小写） |
| **`kitex` 生成后编译报错** | `handler.go` 或 `main.go` 被覆盖 | 采用子包隔离策略（`handle/` 包），或先生成再备份 |
| **Docker 中 gateway 报 proto not found** | Dockerfile 缺少 COPY proto | 参考 4.8 ③ 添加 COPY 指令 |

---

### 4.12 后续新增 RPC 方法的步骤总结

当已有服务（如 district）需要新增接口时，流程简化为：

1. **修改 proto 文件** — 在 `district.api.district.v1.DistrictService` 下添加新的 `rpc`
2. **执行 `kitex` 重新生成代码**（备份 `handler.go`/`main.go` 再恢复）
3. **实现 handler 方法** — 在 `handle/handler.go` 中填充新方法体
4. **更新 `gateway/main.go`** — 在 `WithAllowedMethods` 中追加新方法名；如果是公开接口还要加 `WithPublicMethods`
5. **重启两个服务并测试**

> ⚠️ **最容易忘的就是第 4 步** —— 不加 `WithAllowedMethods` 就是 403，不加 `WithPublicMethods` 就是 401。

---

## 五、代码生成详解

### 5.1 Kitex 命令

```bash
cd user
kitex -module shopmicro/user -service user -type protobuf api/district.proto
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
