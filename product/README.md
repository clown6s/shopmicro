# Product 商品分类服务

商品分类管理的 gRPC 微服务。基于 [Kitex](https://github.com/cloudwego/kitex) 框架。

## 功能

- **分类树查询**：GetCategoryTree（获取完整分类树）
- **分类 CRUD**：CreateCategory、UpdateCategory、DeleteCategory
- **软删除**：DeleteCategory 级联软删除所有子孙分类（CTE 递归）
- **singleflight 防并发**：查询缓存 + 写入时自动清除缓存
- **JWT 鉴权**：通过 UnaryJWTInterceptor 校验 gateway 传播的 Token

## 端口

| 端口 | 协议 | 说明 |
|------|------|------|
| 6001 | gRPC | Kitex 服务端口（本地开发） |
| 8897 | HTTP | 健康检查端口 |

## 配置

配置文件：`conf/config.yaml`，支持环境变量覆盖。

| 配置项 | 环境变量 | 说明 |
|--------|---------|------|
| `server.addr` | `SERVER_ADDR` | gRPC 监听地址 |
| `mysql.dsn` | `MYSQL_DSN` | MySQL DSN |
| `etcd.addrs` | `ETCD_ADDRS` | etcd 地址 |
| `mode` | `MODE` | 运行模式 |

## 本地开发

```bash
cd product
go run . -c conf/config.yaml
```

## Docker

```bash
docker build -t shopmicro-product -f product/Dockerfile .
```
