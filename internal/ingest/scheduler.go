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

	once  sync.Once
	slots chan struct{} // at most Concurrency syncs at once, scheduled or manual
}

// Run checks for due sources every Tick until ctx is cancelled, then waits for running syncs.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.Tick)
	defer ticker.Stop()
	for {
		due, err := store.New(s.Syncer.Pool).ListDueSources(ctx, s.Interval.Seconds())
		if err != nil && ctx.Err() == nil {
			s.Log.Error("list due sources", "error", err)
		}
		var wg sync.WaitGroup
		for _, src := range due {
			wg.Go(func() {
				if err := s.SyncLocked(ctx, src); err != nil && !errors.Is(err, ErrSyncRunning) &&
					ctx.Err() == nil {
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

// SyncLocked runs one sync of src. It waits for one of Concurrency slots (shared by the
// schedule and manual syncs), then holds the source's Postgres advisory lock so two syncs of
// one source never overlap, even across processes. The whole sync runs on the one connection
// that holds the lock: a sync never needs a second connection, so a full pool cannot
// deadlock it.
func (s *Scheduler) SyncLocked(ctx context.Context, src store.Source) error {
	s.once.Do(func() { s.slots = make(chan struct{}, max(1, s.Concurrency)) })
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}

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
	_, err = s.Syncer.SyncOn(ctx, conn, src)
	return err
}
