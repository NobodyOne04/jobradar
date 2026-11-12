package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/NobodyOne04/jobradar/internal/store"
	"github.com/NobodyOne04/jobradar/internal/testdb"
)

func TestMigrationsUpDownStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st, err := store.Open(ctx, testdb.URL(t), 4)
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
