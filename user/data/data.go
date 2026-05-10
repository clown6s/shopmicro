package data

import (
	"context"
	"database/sql"
	"time"

	"shopmicro/user/conf"

	"github.com/XSAM/otelsql"
	_ "github.com/go-sql-driver/mysql"
	"go.opentelemetry.io/otel"

	"shopmicro/pkg/redis"
)

type Data struct {
	DB  *sql.DB
	Rdb *redis.Client
}

func New(cfg conf.MySQL, redisCfg redis.Config, mode string) (*Data, func(), error) {

	// P1-03 fix: disable SQL tracing to prevent data leakage
	spanOpts := otelsql.SpanOptions{
		OmitConnQuery:        true, // 创建 sql.conn.query  span
		OmitRows:             true, // P1-03: omit sql.rows span
		OmitConnPrepare:      true, // P1-03: omit sql.conn.prepare span
		OmitConnectorConnect: true, // P1-03: omit sql.connector.connect span
		OmitConnResetSession: true, // P1-03: omit reset session
		DisableQuery:         true, // P1-03 fix: block db.statement from traces
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

	// ping mysql
	if err := db.PingContext(context.Background()); err != nil {
		return nil, nil, err
	}

	// redis (tracing enabled)
	redisCfg.EnableTracing = true
	rc, redisCleanup, err := redis.New(redisCfg)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}

	cleanup := func() {
		_ = db.Close()
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
