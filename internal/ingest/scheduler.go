package ingest

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/NobodyOne04/jobradar/internal/store"
)

const defaultDBTimeout = 10 * time.Second

// ErrSyncRunning means another sync of the same source holds its lock.
var ErrSyncRunning = errors.New("a sync of this source is already running")

// Scheduler syncs every enabled source whose last successful sync is older than Interval.
// Catch-up after downtime needs no special code: overdue sources are simply due (ADR 0005).
type Scheduler struct {
	Syncer      *Syncer
	Interval    time.Duration // SYNC_INTERVAL
	Concurrency int           // SYNC_CONCURRENCY
	Tick        time.Duration // how often to look for due sources
	Log         *slog.Logger
}

// Run checks for due sources every Tick until ctx is cancelled, then waits for running syncs.
func (s *Scheduler) Run(ctx context.Context) {
	sem := make(chan struct{}, s.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()

	ticker := time.NewTicker(s.Tick)
	defer ticker.Stop()
	for {
		due, err := store.New(s.Syncer.Pool).ListDueSources(ctx, s.Interval.Seconds())
		if err != nil && ctx.Err() == nil {
			s.Log.Error("list due sources", "error", err)
		}
		for _, src := range due {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			wg.Go(func() {
				defer func() { <-sem }()
				if err := s.SyncLocked(ctx, src); err != nil && !errors.Is(err, ErrSyncRunning) {
					s.Log.Warn("sync failed", "source_id", src.ID, "error", err)
				}
			})
		}
		// Due sources stay due until their sync succeeds, so wait for this batch before
		// listing again; otherwise a slow sync would be started twice.
		wg.Wait()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// SyncLocked runs one sync while holding the source's Postgres advisory lock, so the
// scheduler and a manual POST /sources/{id}/sync never overlap, even across processes.
func (s *Scheduler) SyncLocked(ctx context.Context, src store.Source) error {
	conn, err := s.Syncer.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	// Session-level lock: it lives on this connection, held until unlock or release.
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", src.ID).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return ErrSyncRunning
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultDBTimeout)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", src.ID)
	}()
	_, err = s.Syncer.Sync(ctx, src)
	return err
}
