package data

import (
	"database/sql"

	"shopmicro/pkg/mysql"
	"shopmicro/pkg/redis"
)

type Data struct {
	DB      *sql.DB
	Rdb     *redis.Client
	userDal *UserDal // 缓存以避免每次调用都分配
}

func New(cfg mysql.Config, redisCfg redis.Config) (*Data, func(), error) {
	// 使用 pkg/mysql 进行数据库连接（OTEL 配置集中管理）
	db, dbCleanup, err := mysql.New(cfg)
	if err != nil {
		return nil, nil, err
	}

	// redis（启用链路追踪）
	redisCfg.EnableTracing = true
	rc, redisCleanup, err := redis.New(redisCfg)
	if err != nil {
		dbCleanup()
		return nil, nil, err
	}

	cleanup := func() {
		// 先关闭 Redis：关闭期间可能需要 Redis 撤销 token
		redisCleanup()
		dbCleanup()
	}

	d := &Data{
		DB:  db,
		Rdb: rc,
	}
	d.userDal = &UserDal{db: db}
	return d, cleanup, nil
}

// User 返回缓存的 UserDal 实例。
// 线程安全 — UserDal 是无状态的（仅持有 *sql.DB）。
func (d *Data) User() *UserDal {
	return d.userDal
}
