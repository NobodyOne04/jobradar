package store

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// startPostgres runs a throwaway Postgres; skipped when Docker is not available.
func startPostgres(t *testing.T) string {
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

func TestMigrationsUpDownStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st, err := Open(ctx, startPostgres(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	current, latest, err := st.SchemaStatus(ctx)
	if err != nil || current != 0 || latest < 1 {
		t.Fatalf("fresh database: current=%d latest=%d err=%v", current, latest, err)
	}

	p, err := st.Migrator()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if current, latest, _ = st.SchemaStatus(ctx); current != latest {
		t.Fatalf("after up: current=%d latest=%d", current, latest)
	}
	// Running up again is a no-op: the migrate service runs on every `compose up`.
	if results, err := p.Up(ctx); err != nil || len(results) != 0 {
		t.Fatalf("second up: %d results, err=%v", len(results), err)
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("down to 0: %v", err)
	}
}
