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
```

| 服务 | 说明 | 框架 | 端口 |
|------|------|------|------|
| Gateway | API 网关，JWT 鉴权、限流、熔断 | Hertz | 7000 |
| User | 用户注册/登录、Token 管理 | Kitex | 6002 |
| District | 行政区划查询与更新 | Kitex | 6000 |
| Product | 商品分类管理 | Kitex | 6001 |

## 项目结构

```
shopmicro/
├── gateway/          # API 网关
├── user/             # 用户服务
├── district/         # 行政区划服务
├── product/          # 商品分类服务
├── pkg/              # 公共模块（jwt/logger/mysql/redis/otel/...）
├── proto/            # Protobuf IDL 文件
├── scripts/          # SQL 脚本
├── deploy/           # 生产部署配置（基础设施 docker-compose、MySQL/Redis 配置）
├── docker-compose.yml
├── go.work
└── Makefile
```

## 技术栈

- **Go 1.26** / Go Workspace Monorepo
- **Hertz** HTTP 网关 + **Kitex** gRPC 框架（字节跳动）
- **etcd** 服务发现
- **MySQL 8** + **Redis 7**
- **OpenTelemetry** 链路追踪
- **Protobuf** 接口定义 + Kitex Generic 泛化调用

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
- MySQL 8.0
- Redis 7
- etcd 3.5
- [kitex](https://github.com/cloudwego/kitex) CLI

### 启动基础设施

```bash
# 方式一：Docker（推荐开发用）
cd deploy && docker-compose up -d

# 方式二：本地已安装
# 确保 MySQL、Redis、etcd 运行中
```

### 启动服务

```bash
# 方式一：Makefile
make user
make district
make product
make gateway

# 方式二：手动
cd user && go run . -c conf/config.yaml
cd district && go run . -c conf/config.yaml
cd product && go run . -c conf/config.yaml
cd gateway && go run . -c conf/config.yaml
```

### 测试

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

## Docker 部署

### 1. 部署基础设施

```bash
cd deploy
cp .env.example .env   # 编辑填入密码
docker-compose up -d
```

### 2. 构建服务镜像

```bash
# 在项目根目录执行，build context 为项目根目录
docker build -t shopmicro-gateway -f gateway/Dockerfile .
docker build -t shopmicro-user -f user/Dockerfile .
docker build -t shopmicro-district -f district/Dockerfile .
docker build -t shopmicro-product -f product/Dockerfile .
```

### 3. 配置环境变量

在项目根目录创建 `.env`：

```env
MYSQL_DSN=root:password@tcp(172.30.0.10:3306)/shopmall?charset=utf8mb4&parseTime=true&loc=Local
REDIS_ADDR=172.30.0.20:6379
REDIS_PASSWORD=your_redis_password
ETCD_ADDRS=172.30.0.30:2379
JWT_SECRET=your-jwt-secret-at-least-64-chars-long
AMAP_KEY=your-amap-key
INTERNAL_TOKEN=your-internal-token
```

### 4. 启动所有服务

```bash
docker-compose up -d
```

### 5. 验证

```bash
curl http://localhost:7000/health
# → {"status":"ok"}

curl http://localhost:8899/health   # user 健康检查
curl http://localhost:8898/health   # district 健康检查
curl http://localhost:8897/health   # product 健康检查
```

## 端口规划

| 服务 | 开发端口 | Docker 映射 | 健康检查 |
|------|---------|------------|---------|
| Gateway | :8080 | 7000:7000 | `/health` `/ready` |
| User | :6002 | 8888:8888 | :8899/health |
| District | :6000 | 8900:8888 | :8898/health |
| Product | :6001 | 8901:8888 | :8897/health |
| MySQL | 3306 | 3306 | - |
| Redis | 6379 | 6379 | - |
| etcd | 2379 | 2379 | - |

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
