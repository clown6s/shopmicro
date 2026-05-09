package data

import (
	"context"
	"database/sql"
	"time"

	"shopmicro/user/conf"

	"github.com/XSAM/otelsql"
	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
)

type Data struct {
	DB  *sql.DB
	Rdb *redis.Client
}

func New(cfg conf.MySQL, redisCfg conf.Redis, mode string) (*Data, func(), error) {

	// 显式配置 SpanOptions：开启所有 SQL 相关 span
	spanOpts := otelsql.SpanOptions{
		OmitConnQuery:        false, // 创建 sql.conn.query  span
		OmitRows:             false, // 创建 sql.rows span
		OmitConnPrepare:      false, // 创建 sql.conn.prepare span
		OmitConnectorConnect: false, // 创建 sql.connector.connect span
		OmitConnResetSession: false,
		DisableQuery:         false, // 不屏蔽 db.statement
	}

	db, err := otelsql.Open(
		"mysql",
		cfg.DSN,

		otelsql.WithTracerProvider(
			otel.GetTracerProvider(),
		),

		otelsql.WithSpanOptions(spanOpts),
	)

	if err != nil {
		return nil, nil, err
	}

	// mysql pool
	db.SetMaxIdleConns(10)
	db.SetMaxOpenConns(100)
	db.SetConnMaxLifetime(time.Hour)

	// ping mysql（用 Background，不创建 ping span 也可以）
	if err := db.PingContext(context.Background()); err != nil {
		return nil, nil, err
	}

	// redis
	rdb := redis.NewClient(
		&redis.Options{
			Addr:     redisCfg.Addr,
			Password: redisCfg.Password,
			DB:       redisCfg.DB,
		},
	)

	// redis tracing
	if err := redisotel.InstrumentTracing(rdb); err != nil {
		_ = db.Close()
		return nil, nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// ping redis
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = db.Close()
		_ = rdb.Close()
		return nil, nil, err
	}

	cleanup := func() {
		_ = db.Close()
		_ = rdb.Close()
	}

	return &Data{
		DB:  db,
		Rdb: rdb,
	}, cleanup, nil
}

func (d *Data) User() *UserDal {
	return &UserDal{db: d.DB}
}
