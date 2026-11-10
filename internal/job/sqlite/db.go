// Package sqlite is the SQLite-backed implementation of the job persistence
// ports. It also owns the schema migrations, which are embedded so that a
// freshly built binary can prepare its own database.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/url"

	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"

	// Registers the pure-Go "sqlite" driver. No cgo, so the binary
	// cross-compiles and ships as a single file.
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// driverName is the database/sql driver registered by modernc.org/sqlite.
const driverName = "sqlite"

// Open opens the database at path, applies any pending migrations, and returns
// a handle ready for use.
//
// The connection pool is pinned to a single connection. SQLite serialises
// writers anyway, and a single connection makes the connection-scoped pragmas
// below apply to every statement without any pooling surprises.
func Open(ctx context.Context, path string) (*sqlx.DB, error) {
	db, err := sqlx.Open(driverName, dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to database %s: %w", path, err)
	}
	if err := migrate(ctx, db.DB); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// dsn builds a modernc.org/sqlite connection string with the pragmas jonus
// relies on: write-ahead logging for concurrent readers, a busy timeout so
// brief lock contention retries instead of failing, and foreign keys enabled
// so that deleting a job removes its tracks.
func dsn(path string) string {
	u := &url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	return u.String()
}

// migrate brings the schema up to date.
func migrate(ctx context.Context, db *sql.DB) error {
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("configure migrations: %w", err)
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
