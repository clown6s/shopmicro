package data

import (
	"database/sql"

	"shopmicro/pkg/mysql"
	"shopmicro/pkg/redis"
)

type Data struct {
	DB  *sql.DB
	Rdb *redis.Client
}

func New(cfg mysql.Config, redisCfg redis.Config, mode string) (*Data, func(), error) {
	// Use pkg/mysql for database connection (OTEL config centralized)
	db, dbCleanup, err := mysql.New(cfg)
	if err != nil {
		return nil, nil, err
	}

	// redis (tracing enabled)
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

	return &Data{
		DB:  db,
		Rdb: rc,
	}, cleanup, nil
}

func (d *Data) User() *UserDal {
	return &UserDal{db: d.DB}
}
