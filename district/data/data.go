package data

import (
	"database/sql"

	"shopmicro/pkg/mysql"
	"shopmicro/pkg/redis"
)

type Data struct {
	DB          *sql.DB
	Rdb         *redis.Client
	districtDal *DistrictDal // 缓存以避免每次调用重新分配
}

func New(cfg mysql.Config, redisCfg redis.Config) (*Data, func(), error) {
	// 使用 pkg/mysql 进行数据库连接（OTEL 配置集中管理）
	db, dbCleanup, err := mysql.New(cfg)
	if err != nil {
		return nil, nil, err
	}

	// redis（已启用链路追踪）
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

	d := &Data{
		DB:  db,
		Rdb: rc,
	}
	d.districtDal = &DistrictDal{db: db}
	return d, cleanup, nil
}

// District 返回缓存的 DistrictDal 实例。
// 线程安全 —— DistrictDal 是无状态的（仅持有 *sql.DB）。
func (d *Data) District() *DistrictDal {
	return d.districtDal
}
