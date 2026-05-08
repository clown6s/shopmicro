# shopmicro 开发指南

> 技术栈：Hertz + Kitex + Protobuf + Etcd + zap + MySQL(GORM)

## 项目结构

```
shopmicro/
├── go.work                  # Go workspace 联合开发
├── Makefile                 # 构建/运行命令
├── user/                    # User 微服务 (独立 go.mod)
│   ├── main.go              # Kitex gRPC 入口 :8888
│   ├── handler.go           # RPC 业务逻辑实现
│   ├── kitex_gen/           # Kitex 自动生成（勿手动修改）
│   ├── api/
│   │   └── user.proto       # Protobuf IDL 定义
│   ├── conf/
│   │   ├── conf.go          # 配置加载
│   │   └── config.yaml      # 配置文件
│   ├── model/
│   │   └── user.go          # GORM 数据模型
│   └── dal/
│       ├── db.go            # 数据库初始化
│       └── user.go          # 数据访问层
├── gateway/                 # HTTP 网关 (独立 go.mod)
│   ├── main.go              # Hertz HTTP 入口 :8080
│   ├── handler/
│   │   └── user.go          # HTTP handler
│   ├── router/
│   │   └── router.go        # 路由注册
│   ├── middleware/
│   │   └── cors.go          # 中间件
│   └── conf/
│       ├── conf.go          # 配置加载
│       └── config.yaml      # 配置文件
└── pkg/                     # 共享模块
    ├── logger/logger.go     # zap 日志
    └── errcode/errcode.go   # 统一错误码
```

---

## 一、添加新接口（完整流程）

以给 User 服务添加 `SearchUser`（按姓名搜索）接口为例。

### 1.1 修改 proto 文件

编辑 `user/api/user.proto`，添加 message 和 rpc：

```protobuf
// 新增请求 message
message SearchUserReq {
  string real_name = 1;       // 搜索关键词
  int32 page = 2;
  int32 page_size = 3;
}

// 新增响应 message
message SearchUserResp {
  repeated UserInfo users = 1;
  int64 total = 2;
}

// 在 service 中添加 rpc 方法
service UserService {
  // ... 已有方法
  rpc SearchUser(SearchUserReq) returns(SearchUserResp);  // 新增
}
```

### 1.2 重新生成 Kitex 代码

```bash
cd user
kitex -module shopmicro/user -service user -type protobuf api/user.proto
```

**注意**：此命令会覆盖 `handler.go` 和 `main.go`，建议执行前备份。

生成后 `handler.go` 会出现新的空方法：

```go
func (s *UserServiceImpl) SearchUser(ctx context.Context, req *v1.SearchUserReq) (resp *v1.SearchUserResp, err error) {
    // TODO: Your code here...
    return
}
```

### 1.3 实现 RPC 业务逻辑

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

### 1.4 添加数据访问方法

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
    if err != nil {
        return nil, 0, err
    }

    return users, total, nil
}
```

### 1.5 添加网关 HTTP 路由

在 `gateway/handler/user.go` 添加 HTTP handler：

```go
func (h *UserHandler) Search(ctx context.Context, c *app.RequestContext) {
    realName := c.Query("real_name")
    page, _ := strconv.ParseInt(c.DefaultQuery("page", "1"), 10, 32)
    pageSize, _ := strconv.ParseInt(c.DefaultQuery("page_size", "20"), 10, 32)

    resp, err := h.client.SearchUser(ctx, &api.SearchUserReq{
        RealName: realName,
        Page:     int32(page),
        PageSize: int32(pageSize),
    })
    if err != nil {
        logger.Errorf("gateway search user failed: %v", err)
        c.JSON(consts.StatusInternalServerError, Response{Code: errcode.ErrServer.Code, Msg: err.Error()})
        return
    }

    c.JSON(consts.StatusOK, Response{Code: errcode.Success.Code, Msg: errcode.Success.Msg, Data: resp})
}
```

在 `gateway/router/router.go` 注册路由：

```go
user := api.Group("/user")
{
    // ... 已有路由
    user.GET("/search", userHandler.Search)  // 新增
}
```

### 1.6 更新依赖

```bash
cd user && go mod tidy
cd gateway && go mod tidy
```

### 1.7 验证

```bash
# 启动服务后测试
curl "http://localhost:8080/api/v1/user/search?real_name=张&page=1&page_size=10"
```

---

## 二、代码生成详解

### 2.1 Kitex 代码生成命令

```bash
cd user
kitex -module shopmicro/user -service user -type protobuf api/user.proto
```

**参数说明：**

| 参数 | 说明 |
|------|------|
| `-module` | 生成的 Go 代码所属模块名，对应 `go.mod` 中的 module |
| `-service` | 服务名，生成服务端脚手架（handler.go、main.go） |
| `-type protobuf` | 使用 Protobuf 协议（非 Thrift） |

### 2.2 生成的文件

```
user/
├── kitex_gen/api/user/v1/
│   ├── user.pb.go                    # Protobuf 消息定义（Go 结构体）
│   └── userservice/
│       ├── client.go                 # gRPC 客户端接口
│       ├── server.go                 # gRPC 服务端接口
│       └── userservice.go            # 序列化/反序列化、方法路由
├── handler.go                        # 服务端实现骨架（手动填充业务逻辑）
├── main.go                           # 服务端启动入口（手动修改配置）
└── kitex_info.yaml                   # Kitex 元信息
```

### 2.3 重新生成注意事项

**`handler.go` 和 `main.go` 会被覆盖。** 建议流程：

1. 修改 `api/user.proto`
2. 备份 `handler.go` 和 `main.go`
3. 执行 `kitex` 生成
4. 将备份中的业务逻辑合并回新生成的 `handler.go`
5. 将备份中的配置（etcd、mysql 等）合并回 `main.go`

### 2.4 只生成 PB 消息（不生成服务骨架）

如果只想更新消息定义而不覆盖 handler/main：

```bash
cd user
protoc --go_out=kitex_gen/api/user/v1 --go_opt=module=shopmicro/user/kitex_gen/api/user/v1 api/user.proto
```

但这不会更新 `userservice` 包中的方法签名，如有新增 rpc 方法仍需用 kitex 重新生成。

---

## 三、网关配置详解

### 3.1 网关目录结构

```
gateway/
├── main.go              # 入口：初始化日志、Etcd Resolver、Kitex Client、Hertz 路由
├── conf/
│   ├── conf.go          # 配置结构体 + YAML 加载
│   └── config.yaml      # 配置文件
├── handler/
│   └── user.go          # User 服务 HTTP handler
├── router/
│   └── router.go        # 路由注册中心
└── middleware/
    └── cors.go          # CORS 跨域中间件
```

### 3.2 网关配置文件

`gateway/conf/config.yaml`：

```yaml
server:
  addr: ":8080"          # Hertz 监听地址

etcd:
  addrs:
    - "127.0.0.1:2379"  # Etcd 地址，用于服务发现

mode: "dev"              # 日志模式：dev(开发) / prod(生产)
```

### 3.3 网关启动流程（main.go）

```
1. 加载配置文件
2. 初始化 zap 日志
3. 创建 Etcd Resolver（服务发现）
4. 创建 Kitex gRPC Client（连接 user 服务）
5. 注册 Hertz 路由和中间件
6. 启动 Hertz HTTP 服务
```

对应代码 `gateway/main.go`：

```go
func main() {
    // 1. 加载配置
    cfg, err := conf.Load(*configPath)

    // 2. 初始化日志
    logger.Init(cfg.Mode)

    // 3. 创建 Etcd Resolver
    r, err := etcd.NewEtcdResolver(cfg.Etcd.Addrs)

    // 4. 创建 Kitex Client（通过 Etcd 发现 user 服务）
    userClient, err := userservice.NewClient("user",
        client.WithResolver(r),
        client.WithHostPorts("127.0.0.1:8888"),  // 兜底直连地址
    )

    // 5. 创建 Hertz 并注册路由
    h := server.Default(server.WithHostPorts(cfg.Server.Addr))
    h.Use(middleware.CORS())
    router.Register(h, userClient)

    // 6. 启动
    h.Spin()
}
```

### 3.4 如何添加新服务的网关代理

以添加 `order` 服务为例：

**1) 创建 `gateway/handler/order.go`：**

```go
package handler

import (
    "context"

    "github.com/cloudwego/hertz/pkg/app"
    "github.com/cloudwego/hertz/pkg/protocol/consts"

    api "shopmicro/order/kitex_gen/api/order/v1"
    orderservice "shopmicro/order/kitex_gen/api/order/v1/orderservice"

    "shopmicro/pkg/errcode"
    "shopmicro/pkg/logger"
)

type OrderHandler struct {
    client orderservice.Client
}

func NewOrderHandler(c orderservice.Client) *OrderHandler {
    return &OrderHandler{client: c}
}

type Response struct {
    Code int         `json:"code"`
    Msg  string      `json:"msg"`
    Data interface{} `json:"data,omitempty"`
}

func (h *OrderHandler) Create(ctx context.Context, c *app.RequestContext) {
    var req api.CreateOrderReq
    if err := c.BindJSON(&req); err != nil {
        c.JSON(consts.StatusBadRequest, Response{Code: errcode.ErrParam.Code, Msg: err.Error()})
        return
    }

    resp, err := h.client.CreateOrder(ctx, &req)
    if err != nil {
        logger.Errorf("gateway create order failed: %v", err)
        c.JSON(consts.StatusInternalServerError, Response{Code: errcode.ErrServer.Code, Msg: err.Error()})
        return
    }

    c.JSON(consts.StatusOK, Response{Code: errcode.Success.Code, Msg: errcode.Success.Msg, Data: resp})
}
```

**2) 在 `gateway/router/router.go` 注册路由：**

```go
func Register(h *server.Hertz, userClient userservice.Client, orderClient orderservice.Client) {
    // ... 已有 user 路由

    // 新增 order 路由
    orderHandler := handler.NewOrderHandler(orderClient)
    order := api.Group("/order")
    {
        order.POST("", orderHandler.Create)
        order.GET("/:id", orderHandler.Get)
    }
}
```

**3) 在 `gateway/main.go` 创建 order client 并传入：**

```go
// 创建 order client
orderClient, err := orderservice.NewClient("order",
    client.WithResolver(r),
    client.WithHostPorts("127.0.0.1:8889"),
)

// 注册路由时传入
router.Register(h, userClient, orderClient)
```

### 3.5 添加自定义中间件

创建 `gateway/middleware/auth.go`：

```go
package middleware

import (
    "context"
    "net/http"

    "github.com/cloudwego/hertz/pkg/app"
)

func Auth() app.HandlerFunc {
    return func(ctx context.Context, c *app.RequestContext) {
        token := c.GetHeader("Authorization")
        if token == "" {
            c.JSON(http.StatusUnauthorized, map[string]interface{}{
                "code": 401,
                "msg":  "missing token",
            })
            c.Abort()
            return
        }
        // TODO: 校验 token 逻辑
        c.Next(ctx)
    }
}
```

在 `main.go` 中使用：

```go
h.Use(middleware.CORS())
h.Use(middleware.Auth())  // 全局生效
```

或在路由组中单独使用：

```go
user := api.Group("/user", middleware.Auth())  // 仅该路由组生效
```

---

## 四、配置修改速查

| 要改什么 | 改哪个文件 |
|---------|-----------|
| 网关端口 | `gateway/conf/config.yaml` → `server.addr` |
| 网关 Etcd 地址 | `gateway/conf/config.yaml` → `etcd.addrs` |
| user 服务端口 | `user/conf/config.yaml` → `server.addr` |
| user MySQL 连接 | `user/conf/config.yaml` → `mysql.dsn` |
| user Etcd 地址 | `user/conf/config.yaml` → `etcd.addrs` |
| 新增配置字段 | 对应服务的 `conf/conf.go` 结构体 + `config.yaml` |
