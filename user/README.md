# User 用户服务

用户认证与管理的 gRPC 微服务。基于 [Kitex](https://github.com/cloudwego/kitex) 框架。

## 功能

- **注册/登录**：手机号注册，SHA-256 预哈希 + bcrypt（cost 12）存储
- **JWT 认证**：Access Token（短期）+ Refresh Token（长期），Lua 脚本原子轮换
- **暴力破解防护**：Redis 计数器，5 次失败锁定 15 分钟
- **用户 CRUD**：创建、查询、更新、软删除
- **登出**：撤销用户全部设备的 Token

## 端口

| 端口 | 协议 | 说明 |
|------|------|------|
| 6002 | gRPC | Kitex 服务端口（本地开发） |
| 8899 | HTTP | 健康检查端口 |

## 配置

配置文件：`conf/config.yaml`，支持环境变量覆盖。

| 配置项 | 环境变量 | 说明 |
|--------|---------|------|
| `server.addr` | `SERVER_ADDR` | gRPC 监听地址 |
| `mysql.dsn` | `MYSQL_DSN` | MySQL DSN |
| `redis.addr` | `REDIS_ADDR` | Redis 地址 |
| `redis.password` | `REDIS_PASSWORD` | Redis 密码 |
| `jwt.secret` | `JWT_SECRET` | JWT 签名密钥（≥64字符） |
| `jwt.expiration` | - | AT 有效期（小时），默认 24 |
| `etcd.addrs` | `ETCD_ADDRS` | etcd 地址 |
| `mode` | `MODE` | 运行模式 |

## 本地开发

```bash
cd user
go run . -c conf/config.yaml
```

## Docker

```bash
docker build -t shopmicro-user -f user/Dockerfile .
```
