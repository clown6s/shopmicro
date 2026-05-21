# District 行政区划服务

行政区划数据管理的 gRPC 微服务。基于 [Kitex](https://github.com/cloudwego/kitex) 框架。

## 功能

- **省市区三级查询**：GetProvinces、GetChildren（递归获取子级行政区）
- **全量更新**：从高德地图 API 拉取最新行政区划数据，全量替换数据库
- **singleflight 防并发**：查询缓存 + 更新时自动清除缓存
- **JWT 鉴权**：通过 UnaryJWTInterceptor 校验 gateway 传播的 Token

## 端口

| 端口 | 协议 | 说明 |
|------|------|------|
| 6000 | gRPC | Kitex 服务端口（本地开发） |
| 8898 | HTTP | 健康检查端口 |

## 配置

配置文件：`conf/config.yaml`，支持环境变量覆盖。

| 配置项 | 环境变量 | 说明 |
|--------|---------|------|
| `server.addr` | `SERVER_ADDR` | gRPC 监听地址 |
| `mysql.dsn` | `MYSQL_DSN` | MySQL DSN |
| `etcd.addrs` | `ETCD_ADDRS` | etcd 地址 |
| `amap.key` | `AMAP_KEY` | 高德地图 API Key |
| `amap.timeout` | - | API 超时（秒），默认 120 |
| `mode` | `MODE` | 运行模式 |

## 本地开发

```bash
cd district
go run . -c conf/config.yaml
```

## Docker

```bash
docker build -t shopmicro-district -f district/Dockerfile .
```
