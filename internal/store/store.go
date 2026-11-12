// Package store owns Postgres access: the connection pool and the schema migrations.
// It imports no domain package (ADR 0002).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/NobodyOne04/jobradar/migrations"
)

// Store holds the pool. Query methods (sqlc) arrive with the first tables.
type Store struct {
	Pool *pgxpool.Pool
	db   *sql.DB // database/sql view of the same pool, for goose
}

// Open connects and verifies the connection. maxConns sizes the pool unless the URL sets
// pool_max_conns; pgxpool's own default, max(4, CPUs), is too small for concurrent syncs.
func Open(ctx context.Context, url string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("database config: %w", err)
	}
	if !strings.Contains(url, "pool_max_conns") {
		cfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("database config: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database unreachable: %w", err)
	}
	return &Store{Pool: pool, db: stdlib.OpenDBFromPool(pool)}, nil
}

// Close releases every connection.
func (s *Store) Close() {
	_ = s.db.Close()
	s.Pool.Close()
}

// Migrator runs the embedded migrations.
func (s *Store) Migrator() (*goose.Provider, error) {
	return goose.NewProvider(goose.DialectPostgres, s.db, migrations.FS)
}

// SchemaStatus reports the applied and the latest embedded migration versions.
func (s *Store) SchemaStatus(ctx context.Context) (current, latest int64, err error) {
	p, err := s.Migrator()
	if err != nil {
		return 0, 0, err
	}
	if current, err = p.GetDBVersion(ctx); err != nil {
		return 0, 0, err
	}
	sources := p.ListSources()
	if len(sources) > 0 {
		latest = sources[len(sources)-1].Version
	}
	return current, latest, nil
}
