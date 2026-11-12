package ingest_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
	"github.com/NobodyOne04/jobradar/internal/testdb"
)

// fakeAdapter serves whatever postings the test sets; payloads are {"title","location"}.
type fakeAdapter struct {
	kind     string
	mu       sync.Mutex
	postings map[string]string // external id -> JSON payload
	fetchErr error
	block    chan struct{} // if set, Fetch waits on it
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
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	var out []ingest.RawPosting
	for id, payload := range f.postings {
		out = append(out, ingest.RawPosting{ExternalID: id, URL: "https://example.test/" + id, Payload: json.RawMessage(payload)})
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

	if c := e.sync(t, e.srcA); c != (ingest.Counts{Fetched: 2, Created: 2}) {
		t.Fatalf("first sync: %+v", c)
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
