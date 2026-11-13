package ingest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/metrics"
	"github.com/NobodyOne04/jobradar/internal/store"
	"github.com/NobodyOne04/jobradar/internal/testdb"
)

// fakeAdapter serves whatever postings the test sets; payloads are {"title","location"}.
type fakeAdapter struct {
	kind     string
	mu       sync.Mutex
	postings map[string]string // external id -> JSON payload
	fetchErr error
	block    chan struct{}        // if set, Fetch waits on it
	expires  map[string]time.Time // per external id
}

func (f *fakeAdapter) Kind() string { return f.kind }

func (f *fakeAdapter) Fetch(ctx context.Context, _ store.Source) ([]ingest.RawPosting, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fetchErr != nil && !errors.Is(f.fetchErr, ingest.ErrTruncated) {
		return nil, f.fetchErr
	}
	var out []ingest.RawPosting
	for id, payload := range f.postings {
		p := ingest.RawPosting{ExternalID: id, URL: "https://example.test/" + id, Payload: json.RawMessage(payload)}
		if t, ok := f.expires[id]; ok {
			p.ExpiresAt = &t
		}
		out = append(out, p)
	}
	if errors.Is(f.fetchErr, ingest.ErrTruncated) {
		return out, f.fetchErr // postings and the error, as a truncating adapter does
	}
	return out, nil
}

func (f *fakeAdapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var p struct{ Title, Location string }
	if err := json.Unmarshal(raw.Payload, &p); err != nil {
		return ingest.NormalizedJob{}, err
	}
	return ingest.NormalizedJob{
		Title: p.Title, LocationText: p.Location, DescriptionHTML: "<p>" + p.Title + "</p>",
		RemotePolicy: "unknown", EmploymentType: "unknown",
	}, nil
}

func (f *fakeAdapter) set(postings map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postings = postings
}

type env struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	syncer *ingest.Syncer
	a, b   *fakeAdapter // kinds greenhouse and lever
	srcA   store.Source
	srcB   store.Source
	ctx    context.Context
}

func setup(t *testing.T) *env {
	t.Helper()
	st := testdb.Migrated(t)
	ctx := context.Background()
	q := store.New(st.Pool)
	e := &env{pool: st.Pool, q: q, ctx: ctx,
		a: &fakeAdapter{kind: "greenhouse"}, b: &fakeAdapter{kind: "lever"}}
	e.syncer = &ingest.Syncer{Pool: st.Pool, Adapters: ingest.NewRegistry(e.a, e.b),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var err error
	if e.srcA, err = q.CreateSource(ctx, store.CreateSourceParams{Kind: "greenhouse", BoardToken: "acme",
		DisplayName: "Acme, Inc.", Config: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if e.srcB, err = q.CreateSource(ctx, store.CreateSourceParams{Kind: "lever", BoardToken: "acme",
		DisplayName: "ACME", Config: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) sync(t *testing.T, src store.Source) ingest.Counts {
	t.Helper()
	c, err := e.syncer.Sync(e.ctx, src)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return c
}

func (e *env) count(t *testing.T, sql string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(e.ctx, sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSyncIsIdempotentAndTracksChanges(t *testing.T) {
	e := setup(t)
	e.a.set(map[string]string{
		"1": `{"title":"Data Engineer","location":"Remote"}`,
		"2": `{"title":"Analytics Engineer","location":"Berlin"}`,
	})

	runsBefore := testutil.ToFloat64(metrics.SyncRuns.WithLabelValues("greenhouse", "succeeded"))
	createdBefore := testutil.ToFloat64(metrics.Postings.WithLabelValues("greenhouse", "created"))
	if c := e.sync(t, e.srcA); c != (ingest.Counts{Fetched: 2, Created: 2}) {
		t.Fatalf("first sync: %+v", c)
	}
	if got := testutil.ToFloat64(metrics.SyncRuns.WithLabelValues("greenhouse", "succeeded")) - runsBefore; got != 1 {
		t.Errorf("sync runs metric +%v, want +1", got)
	}
	if got := testutil.ToFloat64(metrics.Postings.WithLabelValues("greenhouse", "created")) - createdBefore; got != 2 {
		t.Errorf("created postings metric +%v, want +2", got)
	}
	if c := e.sync(t, e.srcA); c != (ingest.Counts{Fetched: 2, Unchanged: 2}) {
		t.Fatalf("second sync must change nothing: %+v", c)
	}

	// A changed payload updates the job; a vanished posting closes its job.
	e.a.set(map[string]string{"1": `{"title":"Senior Data Engineer","location":"Remote"}`})
	if c := e.sync(t, e.srcA); c != (ingest.Counts{Fetched: 1, Updated: 1, Closed: 1}) {
		t.Fatalf("change sync: %+v", c)
	}
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE closed_at IS NULL`); n != 1 {
		t.Errorf("open jobs = %d, want 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE title = 'Senior Data Engineer'`); n != 1 {
		t.Error("job title not updated")
	}

	// It comes back: the posting and its job reopen.
	e.a.set(map[string]string{
		"1": `{"title":"Senior Data Engineer","location":"Remote"}`,
		"2": `{"title":"Analytics Engineer","location":"Berlin"}`,
	})
	e.sync(t, e.srcA)
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE closed_at IS NULL`); n != 2 {
		t.Errorf("open jobs after reappearing = %d, want 2", n)
	}

	runs, err := e.q.ListRuns(e.ctx, store.ListRunsParams{SourceID: e.srcA.ID, Limit: 10})
	if err != nil || len(runs) != 4 || runs[0].Status != "succeeded" || runs[0].FinishedAt == nil {
		t.Errorf("runs: %+v, %v", runs, err)
	}
}

func TestCrossSourceDuplicateLinksToOneJob(t *testing.T) {
	e := setup(t)
	// Same company (legal suffix and case differ), same title and location.
	e.a.set(map[string]string{"gh-1": `{"title":"Data Engineer","location":"Remote, EU"}`})
	e.b.set(map[string]string{"lv-9": `{"title":"Data engineer","location":"remote eu"}`})
	e.sync(t, e.srcA)
	e.sync(t, e.srcB)
	if n := e.count(t, `SELECT count(*) FROM jobs`); n != 1 {
		t.Fatalf("jobs = %d, want 1", n)
	}
	if n := e.count(t, `SELECT count(DISTINCT job_id) FROM source_postings`); n != 1 {
		t.Fatal("postings not linked to the same job")
	}

	// The job stays open while either source still lists it.
	e.a.set(map[string]string{})
	e.sync(t, e.srcA)
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE closed_at IS NULL`); n != 1 {
		t.Error("job closed although the other source still lists it")
	}
	e.b.set(map[string]string{})
	e.sync(t, e.srcB)
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE closed_at IS NULL`); n != 0 {
		t.Error("job still open after both sources dropped it")
	}
}

func TestBadPostingMakesRunPartialAndClosesNothing(t *testing.T) {
	e := setup(t)
	e.a.set(map[string]string{
		"1": `{"title":"Data Engineer","location":"Remote"}`,
		"2": `{"title":"Analytics Engineer","location":"Berlin"}`,
	})
	e.sync(t, e.srcA)

	e.a.set(map[string]string{"1": `{"title":"","location":"Remote"}`}) // empty title: rejected
	c, err := e.syncer.Sync(e.ctx, e.srcA)
	if err != nil {
		t.Fatal(err)
	}
	if c.Closed != 0 {
		t.Errorf("a partial run must not close postings: %+v", c)
	}
	runs, _ := e.q.ListRuns(e.ctx, store.ListRunsParams{SourceID: e.srcA.ID, Limit: 1})
	if runs[0].Status != "partial" || runs[0].Error == nil {
		t.Errorf("run: %+v", runs[0])
	}
}

func TestFetchFailureIsRecorded(t *testing.T) {
	e := setup(t)
	e.a.fetchErr = errors.New("board not found")
	if _, err := e.syncer.Sync(e.ctx, e.srcA); err == nil {
		t.Fatal("want an error")
	}
	runs, _ := e.q.ListRuns(e.ctx, store.ListRunsParams{SourceID: e.srcA.ID, Limit: 1})
	if runs[0].Status != "failed" || runs[0].Error == nil || *runs[0].Error != "fetch: board not found" {
		t.Errorf("run: %+v", runs[0])
	}
}

func TestSchedulerLockAndDueSources(t *testing.T) {
	e := setup(t)
	e.a.set(map[string]string{"1": `{"title":"Data Engineer","location":"Remote"}`})
	sched := &ingest.Scheduler{Syncer: e.syncer, Concurrency: 2, Log: e.syncer.Log}

	// While one sync holds the source's lock, a second one is refused.
	e.a.block = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- sched.SyncLocked(e.ctx, e.srcA) }()
	for e.count(t, `SELECT count(*) FROM sync_runs WHERE status = 'running'`) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if err := sched.SyncLocked(e.ctx, e.srcA); !errors.Is(err, ingest.ErrSyncRunning) {
		t.Errorf("concurrent sync: got %v, want ErrSyncRunning", err)
	}
	close(e.a.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// Source A just succeeded, so only B is due.
	due, err := e.q.ListDueSources(e.ctx, 3600)
	if err != nil || len(due) != 1 || due[0].ID != e.srcB.ID {
		t.Errorf("due: %+v, %v", due, err)
	}
	// A disabled source is never due.
	off := false
	if _, err := e.q.UpdateSource(e.ctx, store.UpdateSourceParams{ID: e.srcB.ID, Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if due, _ := e.q.ListDueSources(e.ctx, 3600); len(due) != 0 {
		t.Errorf("due after disabling: %+v", due)
	}
}

// Regression: each sync used to hold one connection for its lock and take another for its
// queries, so Concurrency syncs on a pool of Concurrency connections waited forever.
func TestConcurrentSyncsFitInASmallPool(t *testing.T) {
	e := setup(t)
	e.a.set(map[string]string{"1": `{"title":"Data Engineer","location":"Remote"}`})
	e.b.set(map[string]string{"2": `{"title":"Analytics Engineer","location":"Berlin"}`})

	cfg := e.pool.Config()
	cfg.MaxConns = 2
	small, err := pgxpool.NewWithConfig(e.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	syncer := &ingest.Syncer{Pool: small, Adapters: e.syncer.Adapters, Log: e.syncer.Log}
	sched := &ingest.Scheduler{Syncer: syncer, Concurrency: 2, Log: syncer.Log}

	ctx, cancel := context.WithTimeout(e.ctx, 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for _, src := range []store.Source{e.srcA, e.srcB, e.srcA} { // 3 requests, 2 slots
		wg.Go(func() {
			if err := sched.SyncLocked(ctx, src); err != nil && !errors.Is(err, ingest.ErrSyncRunning) {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("sync did not finish: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM jobs`); n != 2 {
		t.Errorf("jobs = %d, want 2", n)
	}
}

func TestTruncatedListingStoresButClosesNothing(t *testing.T) {
	e := setup(t)
	e.a.set(map[string]string{
		"1": `{"title":"Data Engineer","location":"Remote"}`,
		"2": `{"title":"Analytics Engineer","location":"Berlin"}`,
	})
	e.sync(t, e.srcA)

	// Only "1" fits in the truncated listing; "2" may still be live past the cut.
	e.a.set(map[string]string{
		"1": `{"title":"Data Engineer","location":"Remote"}`,
		"3": `{"title":"ML Engineer","location":"Remote"}`,
	})
	e.a.fetchErr = fmt.Errorf("%w at 25 pages", ingest.ErrTruncated)
	c, err := e.syncer.Sync(e.ctx, e.srcA)
	if err != nil {
		t.Fatal(err)
	}
	if c.Created != 1 || c.Closed != 0 {
		t.Errorf("counts: %+v", c)
	}
	runs, _ := e.q.ListRuns(e.ctx, store.ListRunsParams{SourceID: e.srcA.ID, Limit: 1})
	if runs[0].Status != "partial" || runs[0].Error == nil || !strings.Contains(*runs[0].Error, "truncated") {
		t.Errorf("run: %+v", runs[0])
	}
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE closed_at IS NULL`); n != 3 {
		t.Errorf("open jobs = %d, want 3", n)
	}
}

func TestExpiredPostingsCloseEvenWhenListed(t *testing.T) {
	e := setup(t)
	e.a.set(map[string]string{
		"live":    `{"title":"Data Engineer","location":"Remote"}`,
		"expired": `{"title":"Old Engineer","location":"Remote"}`,
	})
	e.a.expires = map[string]time.Time{
		"live":    time.Now().Add(24 * time.Hour),
		"expired": time.Now().Add(-time.Hour),
	}
	e.a.fetchErr = ingest.ErrTruncated // even a truncated run applies expiry
	c, err := e.syncer.Sync(e.ctx, e.srcA)
	if err != nil {
		t.Fatal(err)
	}
	if c.Created != 2 || c.Closed != 1 {
		t.Errorf("counts: %+v", c)
	}
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE closed_at IS NULL AND title = 'Data Engineer'`); n != 1 {
		t.Error("live job closed")
	}
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE closed_at IS NOT NULL AND title = 'Old Engineer'`); n != 1 {
		t.Error("expired job still open")
	}
}

// Migration 00003 hashes plain dedup keys stored before it; they must equal what Go computes
// now, or new postings would stop linking to those jobs.
func TestMigrationHashesOldDedupKeysLikeGo(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.URL(t), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.Migrator()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO jobs (title, company_name, company_key, content_hash, dedup_key)
		VALUES ('Data Engineer', 'Acme', 'acme', 'h', 'acme|data engineer|berlin germany')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 3); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := st.Pool.QueryRow(ctx, `SELECT dedup_key FROM jobs`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := ingest.DedupKey("acme", "Data Engineer", "Berlin, Germany"); got != want {
		t.Errorf("migrated key %s, Go key %s", got, want)
	}
}
