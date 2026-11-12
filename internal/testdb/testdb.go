// Package testdb starts a throwaway, migrated Postgres for tests (testcontainers).
package testdb

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/NobodyOne04/jobradar/internal/store"
)

// URL starts an empty Postgres and returns its connection string. Skips without Docker.
func URL(t *testing.T) string {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:18-alpine",
		postgres.WithDatabase("jobradar"),
		postgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, pg)
	if err != nil {
		t.Fatal(err)
	}
	url, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return url
}

// Migrated starts Postgres, applies every migration and returns the open store.
func Migrated(t *testing.T) *store.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st, err := store.Open(ctx, URL(t), 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	p, err := st.Migrator()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}
