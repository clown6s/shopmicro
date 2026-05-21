# Gateway 网关服务

API 网关，所有外部 HTTP 请求的统一入口。基于 [Hertz](https://github.com/cloudwego/hertz) 框架，通过 [Kitex Generic](https://www.cloudwego.io/docs/kitex/tutorials/advanced-feature/generic-call/) 将 HTTP 请求代理到下游 gRPC 微服务。

## 架构概览

```
                    ┌─────────────┐
   HTTP Request ──► │   Gateway   │ ──gRPC──► User Service
                    │  (Hertz)    │ ──gRPC──► District Service
                    └─────────────┘ ──gRPC──► Product Service
                          │
                          ▼
                   Redis / etcd
```

## 核心功能

### 请求代理

- 基于 Proto 文件的 Generic 调用，HTTP → gRPC 自动转换
- 服务发现：通过 etcd 自动发现下游服务实例
- 方法级超时与重试配置
- 响应码统一映射（`response.HttpStatus()`），防止下游服务控制网关 HTTP 状态码

### 中间件链

| 中间件 | 说明 |
|--------|------|
| `TraceMiddleware` | 生成 `X-Request-Id`，响应头返回追踪 ID |
| `AccessLogMiddleware` | 访问日志，生产模式仅记录非 2xx 请求 |
| `RateLimitMiddleware` | IP 级令牌桶限流 + Redis 分布式限流 |
| `AuthMiddleware` | JWT 鉴权 + metainfo 传播（token/user_id/request_id/trace_id） |
| `PreGzipMiddleware` | 预压缩响应缓存 |
| `CircuitBreakerMiddleware` | 熔断器（gobreaker），统计显著性判断 + 区分基础设施/业务错误 |

### 限流策略

- **IP 级本地限流**：令牌桶算法，内存存储，定期清理过期条目（10min TTL）
- **Redis 分布式限流**：滑动窗口，用于跨实例共享限流状态
- **Login 端点专项限流**：5 次/60s/IP，防暴力破解

### 熔断保护

- 半开状态探测：`MaxRequests=10`
- 触发条件：请求数 ≥ 20 且失败率 ≥ 50%
- 仅基础设施错误计入熔断（网络超时、连接拒绝等），业务错误不影响

### 健康检查

| 端点 | 说明 |
|------|------|
| `GET /health` | 存活探针（Liveness） |
| `GET /ready` | 就绪探针（Readiness），检查 Redis + etcd 连通性 |

## 配置

配置文件：`conf/config.yaml`，支持环境变量覆盖。

| 配置项 | 环境变量 | 说明 | 默认值 |
|--------|---------|------|--------|
| `server.addr` | `SERVER_ADDR` | 监听地址 | `:8080` |
| `redis.addr` | `REDIS_ADDR` | Redis 地址 | `localhost:6379` |
| `redis.password` | `REDIS_PASSWORD` | Redis 密码 | 空 |
| `jwt.secret` | `JWT_SECRET` | JWT 签名密钥（≥64字符） | - |
| `etcd.addrs` | `ETCD_ADDRS` | etcd 地址（逗号分隔） | `localhost:2379` |
| `mode` | `MODE` | 运行模式 `dev`/`prod`/`staging` | `dev` |
| `access_log.level` | - | 访问日志级别 | dev=`all`, prod=`errorOnly` |

## 路由配置

在 `routes/routes.go` 中定义：

- **公开方法**（无需 JWT）：各服务的只读查询接口
- **认证方法**（需 JWT）：写入/修改接口
- **HTTP 方法映射**：GET/POST 到 gRPC 方法名
- **IP 白名单**：健康检查端点仅允许内网访问

## 优雅关闭

收到 `SIGINT`/`SIGTERM` 后：
1. 标记不健康（就绪探针返回 503）
2. 停止接受新连接
3. 等待存量请求处理完毕（10s 超时）
4. 停止后台 goroutine（限流器清理、预压缩清理、metrics 更新）
5. 关闭 Redis/etcd 连接

## 本地开发

```bash
# 确保 Redis、etcd 已启动
cd gateway
go run . -c conf/config.yaml
```

## Docker

```bash
# 构建上下文为项目根目录（需要 go.work 和 pkg/）
docker build -t shopmicro-gateway -f gateway/Dockerfile .
docker run -p 8080:8080 \
  -e SERVER_ADDR=":8080" \
  -e REDIS_ADDR="host.docker.internal:6379" \
  -e ETCD_ADDRS="host.docker.internal:2379" \
  -e JWT_SECRET="your-jwt-secret-at-least-64-chars-long" \
  shopmicro-gateway
```
