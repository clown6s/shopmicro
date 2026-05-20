# ============================================================
# ShopMicro 基础设施部署指南
# ============================================================

## 版本

| 服务    | 版本    | 端口  | 容器 IP    |
|---------|---------|-------|------------|
| MySQL   | 8.4     | 3306  | 172.30.0.10 |
| Redis   | 8.6.2   | 6379  | 172.30.0.20 |
| etcd    | 3.6.9   | 2379  | 172.30.0.30 |

## 快速启动

```bash
cd deploy

# 启动所有服务
docker compose up -d

# 查看状态
docker compose ps

# 查看日志
docker compose logs -f mysql
docker compose logs -f redis
```

## 启动

三个基础设施服务互相独立，无依赖关系，Docker Compose 会并行启动。各服务 healthcheck 通过后才视为就绪。

## 数据持久化

所有数据存储在 Docker 命名卷中:
- `shopmicro-mysql-data` → MySQL 数据 + binlog
- `shopmicro-redis-data` → AOF + RDB
- `shopmicro-etcd-data` → etcd 快照 + WAL

数据备份:
```bash
# MySQL 备份
docker exec shopmicro-mysql mysqldump -u root -p'${MYSQL_ROOT_PASSWORD}' --all-databases --single-transaction > backup_mysql.sql

# Redis 备份（触发 RDB 快照）
docker exec shopmicro-redis redis-cli -a '${REDIS_PASSWORD}' BGSAVE
docker cp shopmicro-redis:/data/dump.rdb ./backup_redis.rdb

# etcd 备份
docker exec shopmicro-etcd etcdctl snapshot save /tmp/etcd-backup.db
docker cp shopmicro-etcd:/tmp/etcd-backup.db ./backup_etcd.db
```

## 常用操作

```bash
# 停止所有服务（数据保留）
docker compose stop

# 停止并删除容器（数据保留）
docker compose down

# 停止并删除数据（危险！）
docker compose down -v

# 重启单个服务
docker compose restart mysql

# 查看资源使用
docker stats shopmicro-mysql shopmicro-redis shopmicro-etcd
```

## 安全加固（生产必须）

1. **TLS**: 所有服务启用 TLS，修改各配置文件取消注释 TLS 段
2. **防火墙**: 仅开放服务端口给微服务网段，管理端口不暴露
3. **密码**: 修改 `.env` 中所有默认密码
4. **MySQL**: 考虑删除 root 远程访问，仅使用 'shopmicro' 用户
5. **Redis**: FLUSHDB/FLUSHALL/DEBUG 已通过 rename-command 禁用

## 网络架构

```
                    ┌─────────────────────────────┐
                    │    shopmicro-infra          │
                    │    172.30.0.0/24            │
  ┌─────────────────┼─────────────────────────────┼─────────────────┐
  │                 │                             │                 │
  │   MySQL:3306    │   Redis:6379   etcd:2379    │                 │
  │  172.30.0.10   │  172.30.0.20  172.30.0.30   │                 │
  │                 │                             │                 │
  └─────────────────┼─────────────────────────────┼─────────────────┘
                    │                             │
                    │         宿主机               │
                    │    10.211.55.6               │
                    └─────────────────────────────┘
```
