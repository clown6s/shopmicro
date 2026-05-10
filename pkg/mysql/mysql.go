package mysql

import (
	"context"
	"database/sql"
	"time"

	"github.com/XSAM/otelsql"
	_ "github.com/go-sql-driver/mysql"
	"go.opentelemetry.io/otel"
)

// New creates a new MySQL connection with OTEL tracing.
// It returns (*sql.DB, cleanup function, error).
//
// The cleanup function closes the database connection and should be
// called during shutdown.
//
// P1-03: OTEL SpanOptions are centralized here to prevent SQL leakage.
func New(cfg Config) (*sql.DB, func(), error) {
	// P1-03 fix: disable SQL tracing to prevent data leakage
	spanOpts := otelsql.SpanOptions{
		OmitConnQuery:        true, // P1-03: omit sql.conn.query span
		OmitRows:             true, // P1-03: omit sql.rows span
		OmitConnPrepare:      true, // P1-03: omit sql.conn.prepare span
		OmitConnectorConnect: true, // P1-03: omit sql.connector.connect span
		OmitConnResetSession: true, // P1-03: omit reset session
		DisableQuery:         true, // P1-03 fix: block db.statement from traces
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

	// MySQL connection pool configuration
	db.SetMaxIdleConns(10)
	db.SetMaxOpenConns(100)
	db.SetConnMaxLifetime(time.Hour)

	// Ping MySQL to verify connection
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, nil, err
	}

	cleanup := func() { _ = db.Close() }
	return db, cleanup, nil
}
