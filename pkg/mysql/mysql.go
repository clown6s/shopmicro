package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/XSAM/otelsql"
	_ "github.com/go-sql-driver/mysql"
	"go.opentelemetry.io/otel"
)

// New 创建一个带有 OTEL 链路追踪的 MySQL 连接。
// 返回 (*sql.DB, 清理函数, 错误)。
//
// 清理函数会关闭数据库连接，应在关闭期间调用。
//
// P1-03: OTEL SpanOptions 集中在此处管理以防止 SQL 信息泄漏。
func New(cfg Config) (*sql.DB, func(), error) {
	// 先校验配置，提供清晰的错误信息
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}

	// P1-03 修复：禁用 SQL 追踪以防止数据泄漏
	spanOpts := otelsql.SpanOptions{
		OmitConnQuery:        true, // P1-03: 省略 sql.conn.query span
		OmitRows:             true, // P1-03: 省略 sql.rows span
		OmitConnPrepare:      true, // P1-03: 省略 sql.conn.prepare span
		OmitConnectorConnect: true, // P1-03: 省略 sql.connector.connect span
		OmitConnResetSession: true, // P1-03: 省略 reset session
		DisableQuery:         true, // P1-03 修复: 阻止 db.statement 出现在追踪中
	}

	db, err := otelsql.Open(
		"mysql",
		cfg.DSN,
		otelsql.WithTracerProvider(otel.GetTracerProvider()),
		otelsql.WithSpanOptions(spanOpts),
	)
	if err != nil {
		return nil, nil, err
	}

	// MySQL 连接池配置
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetime) * time.Second)
	db.SetConnMaxIdleTime(time.Duration(cfg.ConnMaxIdleTime) * time.Second)

	// 优雅等待 MySQL 就绪：容器环境下 MySQL 可能尚未完全启动，
	// 指数退避重试最多 30 秒，避免进程反复 crash loop。
	deadline := time.Now().Add(30 * time.Second)
	interval := 1 * time.Second
	var lastErr error
	for time.Now().Before(deadline) {
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
		lastErr = db.PingContext(pingCtx)
		pingCancel()
		if lastErr == nil {
			break
		}
		log.Printf("mysql not ready, retrying in %v: %v", interval, lastErr)
		time.Sleep(interval)
		if interval < 5*time.Second {
			interval *= 2
		}
	}
	if lastErr != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("mysql ping failed after 30s: %w", lastErr)
	}

	cleanup := func() { _ = db.Close() }
	return db, cleanup, nil
}
