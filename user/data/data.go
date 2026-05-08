package data

import (
	"context"
	"database/sql"
	"time"

	"shopmicro/user/conf"

	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

type Data struct {
	DB  *sql.DB
	Rdb *redis.Client
}

func New(cfg conf.MySQL, redisCfg conf.Redis, mode string) (*Data, func(), error) {
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, nil, err
	}

	db.SetMaxIdleConns(10)
	db.SetMaxOpenConns(100)
	db.SetConnMaxLifetime(time.Hour)

	if err := db.PingContext(context.Background()); err != nil {
		return nil, nil, err
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     redisCfg.Addr,
		Password: redisCfg.Password,
		DB:       redisCfg.DB,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = db.Close()
		return nil, nil, err
	}

	cleanup := func() {
		_ = db.Close()
		_ = rdb.Close()
	}
	return &Data{DB: db, Rdb: rdb}, cleanup, nil
}

func (d *Data) User() *UserDal {
	return &UserDal{db: d.DB}
}
