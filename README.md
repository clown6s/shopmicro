# ShopMicro

电商微服务系统，Go 技术栈，Hertz 网关 + Kitex gRPC 微服务 + etcd 服务发现。

## 架构

```
                       ┌─────────────┐
  Client ──HTTP──────► │   Gateway   │ ──gRPC──► User
                       │   :7000     │ ──gRPC──► District
                       └─────────────┘ ──gRPC──► Product
                             │
                    ┌────────┼────────┐
                 MySQL    Redis    etcd
                             │
                    ┌────────┼────────┐
                  Jaeger    Loki    Grafana
```

## 服务列表

| 服务 | 说明 | 框架 | 端口 |
|------|------|------|------|
| Gateway | API 网关，JWT 鉴权、限流、熔断、PreGzip 缓存 | Hertz | 7000 |
| User | 用户注册/登录、Token 管理 | Kitex | 8899 |
| District | 行政区划查询与更新（高德地图数据源） | Kitex | 8898 |
| Product | 商品分类管理（三级树形结构） | Kitex | 8897 |

## 基础设施

| 组件 | 版本 | IP | 端口 | 说明 |
|------|------|-----|------|------|
| MySQL | 8.4 | 172.30.0.10 | 3306 | 业务数据库 |
| Redis | 8.6.2 | 172.30.0.20 | 6379 | 缓存 + Token 存储 |
| etcd | 3.6.9 | 172.30.0.30 | 2379/2380 | 服务发现 |
| Jaeger | 2.3.0 | 172.30.0.40 | 16686/4317/4318 | 链路追踪（OTLP + Badger） |
| Loki | 2.9.10 | 172.30.0.50 | 3100 | 日志聚合存储 |
| Promtail | 2.9.10 | 172.30.0.51 | - | 日志采集（Docker Socket） |
| Grafana | 11.5.2 | 172.30.0.60 | 3000 | 可视化面板（Loki + Jaeger） |

## 观测平台

- **Grafana**: http://\<host\>:3000（admin / admin@zzp）
  - Explore → Loki 数据源：查询日志 `{container="shopmicro-gateway"}`
  - Explore → Jaeger 数据源：链路追踪
- **Jaeger UI**: http://\<host\>:16686

## 项目结构

```
shopmicro/
├── gateway/          # API 网关
├── user/             # 用户服务
├── district/         # 行政区划服务
├── product/          # 商品分类服务
├── pkg/              # 公共模块（jwt/logger/mysql/redis/otel/...）
├── proto/            # Thrift IDL 文件
├── deploy/           # 部署配置
│   ├── docker-compose.yml  # 全栈编排（含基础设施）
│   ├── mysql/        # MySQL 配置
│   ├── redis/        # Redis 配置
│   ├── jaeger/       # Jaeger 配置
│   ├── loki/         # Loki 配置
│   ├── promtail/     # Promtail 配置
│   ├── grafana/      # Grafana 数据源配置
│   └── scripts/      # MySQL 初始化脚本
├── docker-compose.yml
├── go.work
└── Makefile
```

## 技术栈

- **Go 1.26** / Go Workspace Monorepo
- **Hertz** HTTP 网关 + **Kitex** gRPC 框架（字节跳动）
- **Thrift** 接口定义 + Kitex Generic 泛化调用
- **etcd** 服务发现
- **MySQL 8** + **Redis 8**
- **OpenTelemetry** → Jaeger 链路追踪（gateway 100% 采样，其他 10%）
- **Loki + Promtail + Grafana** 日志收集与可视化
- **PreGzip 缓存**：Gateway 预压缩 + 写操作自动失效

## 缓存策略

| 层级 | 位置 | 机制 | 失效方式 |
|------|------|------|---------|
| L1 | 下游服务 | Singleflight 防缓存击穿 | 重启清除 |
| L2 | Gateway | PreGzip 预压缩缓存（sync.Map） | POST/PUT/DELETE 按服务前缀清缓存 |
| L3 | Redis | 业务缓存（Token 等） | TTL 过期 |

## API 格式

```
POST /api/v1/{service}/{method}
Authorization: Bearer {token}
Content-Type: application/json

{"key": "value"}
```

统一响应：`{"code": 0, "message": "ok", "data": {...}}`

## 快速开始

### 前置依赖

- Go 1.26+
- MySQL 8.0+
- Redis 7+
- etcd 3.5+
- [kitex](https://github.com/cloudwego/kitex) CLI

### 一键部署（Docker Compose）

```bash
# 启动所有基础设施 + 微服务
docker compose up -d

# 单独重建某个服务
docker compose up -d --build --no-deps user

# 只重启（不重新构建）
docker compose up -d --no-deps user
```

### 本地开发

```bash
# 启动基础设施
docker compose up -d mysql redis etcd jaeger loki promtail grafana

# 启动服务（方式一：Makefile）
make user && make district && make product && make gateway

# 启动服务（方式二：手动）
cd user && go run . -c conf/config.yaml
cd district && go run . -c conf/config.yaml
cd product && go run . -c conf/config.yaml
cd gateway && go run . -c conf/config.yaml
```

## API 测试

```bash
# 注册
curl -X POST http://localhost:7000/api/v1/user/RegisterUser \
  -H "Content-Type: application/json" \
  -d '{"phone":"13800138000","password":"abc12345"}'

# 登录
curl -X POST http://localhost:7000/api/v1/user/Login \
  -H "Content-Type: application/json" \
  -d '{"phone":"13800138000","password":"abc12345"}'

# 查询省份（公开接口，无需 token）
curl -X POST http://localhost:7000/api/v1/district/GetProvinces \
  -H "Content-Type: application/json" -d '{}'

# 查询分类树（公开接口）
curl -X POST http://localhost:7000/api/v1/product/GetCategoryTree \
  -H "Content-Type: application/json" -d '{}'
```

## 端口规划

| 服务 | 容器端口 | Docker 映射 | 健康检查 |
|------|---------|------------|---------|
| Gateway | 7000 | 7000:7000 | /health |
| User | 8899 | - | :8899/health |
| District | 8898 | - | :8898/health |
| Product | 8897 | - | :8897/health |
| MySQL | 3306 | 3306:3306 | mysqladmin ping |
| Redis | 6379 | 6379:6379 | redis-cli ping |
| etcd | 2379 | 2379:2379 | etcdctl endpoint health |
| Jaeger | 16686/4317/4318 | 16686/4317/4318 | healthcheckv2 :13133 |
| Loki | 3100 | 3100:3100 | /ready |
| Grafana | 3000 | 3000:3000 | - |

## 持久化卷

| 卷名 | 容器挂载点 | 说明 |
|------|-----------|------|
| shopmicro-mysql-data | /var/lib/mysql | MySQL 数据 |
| shopmicro-redis-data | /data | Redis 持久化 |
| shopmicro-etcd-data | /etcd-data | etcd 数据 |
| shopmicro-jaeger-data | /tmp/jaeger | Jaeger Badger 追踪数据 |
| shopmicro-loki-data | /loki | Loki 日志存储 |
| shopmicro-grafana-data | /var/lib/grafana | Grafana 面板/配置 |

## Make 命令

```bash
make gen          # 重新生成 Kitex 代码
make user         # 启动 user 服务
make district     # 启动 district 服务
make product      # 启动 product 服务
make gateway      # 启动 gateway
make tidy         # 整理依赖
make clean        # 清理 kitex_gen/
```

## 各服务详细文档

| 服务 | 文档 |
|------|------|
| Gateway | [gateway/README.md](gateway/README.md) |
| User | [user/README.md](user/README.md) |
| District | [district/README.md](district/README.md) |
| Product | [product/README.md](product/README.md) |
| 生产部署 | [deploy/README.md](deploy/README.md) |
| 错误码规范 | [pkg/errcode/README.md](pkg/errcode/README.md) |
