package matching_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NobodyOne04/jobradar/internal/matcher"
	"github.com/NobodyOne04/jobradar/internal/matching"
	"github.com/NobodyOne04/jobradar/internal/store"
	"github.com/NobodyOne04/jobradar/internal/testdb"
)

// fakeMLflow serves the champion alias; version "" means no champion, down means 500s.
type fakeMLflow struct {
	mu      sync.Mutex
	version string
	reason  string
	down    bool
}

func (f *fakeMLflow) set(version, reason string, down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version, f.reason, f.down = version, reason, down
}

func (f *fakeMLflow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.down:
		w.WriteHeader(500)
	case r.URL.Path == "/api/2.0/mlflow/registered-models/alias" && f.version == "":
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error_code":"RESOURCE_DOES_NOT_EXIST","message":"not found"}`))
	case r.URL.Path == "/api/2.0/mlflow/registered-models/alias":
		_, _ = fmt.Fprintf(w, `{"model_version":{"version":%q,"run_id":"run-%s","tags":[{"key":"promoted_reason","value":%q},{"key":"gate","value":"passed"}]}}`,
			f.version, f.version, f.reason)
	default: // runs/get
		_, _ = w.Write([]byte(`{"run":{"data":{"metrics":[{"key":"ndcg_at_10","value":0.8}],"params":[{"key":"matcher_version","value":"0.2.1"}]}}}`))
	}
}

// fakeMatcher scores every job 50 except titles "bad" (an item-level error), or answers 503.
type fakeMatcher struct {
	jobs    atomic.Int32 // jobs received
	batches atomic.Int32
	down    atomic.Bool
}

func (f *fakeMatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/version" {
		_, _ = w.Write([]byte(`{"matcher_version":"0.2.1","api_versions":["v1"]}`))
		return
	}
	if f.down.Load() {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"title":"not ready","status":503}`))
		return
	}
	f.batches.Add(1)
	body, _ := io.ReadAll(r.Body)
	var req matcher.MatchRequest
	_ = json.Unmarshal(body, &req)
	var results []map[string]any
	for _, j := range req.Jobs {
		f.jobs.Add(1)
		if j.Title == "bad" {
			results = append(results, map[string]any{"job_id": j.JobID, "error": "invalid job", "errors": []any{}})
			continue
		}
		results = append(results, map[string]any{"job_id": j.JobID, "score": 50, "band": "weak",
			"confidence": "low", "completeness": 0.5, "hard_constraints": "unknown", "explanations": []any{}})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"model_version": req.ModelVersion, "matcher_version": "0.2.1", "results": results})
}

type env struct {
	q      *store.Queries
	loop   *matching.Loop
	mlflow *fakeMLflow
	m      *fakeMatcher
	ctx    context.Context
}

func setup(t *testing.T) (*env, *store.Store) {
	t.Helper()
	st := testdb.Migrated(t)
	ctx := context.Background()
	q := store.New(st.Pool)
	mlflow, m := &fakeMLflow{}, &fakeMatcher{}
	mlSrv, mSrv := httptest.NewServer(mlflow), httptest.NewServer(m)
	t.Cleanup(mlSrv.Close)
	t.Cleanup(mSrv.Close)
	client := matcher.New(mSrv.URL, "token-0123456789abcdef")
	client.Attempts = 1
	loop := &matching.Loop{Pool: st.Pool, Registry: matching.NewRegistry(mlSrv.URL), Matcher: client,
		Pinned: "0.2.1", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), MaxBatches: 20}

	p, err := q.CreateProfile(ctx, "anna")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertRevision(ctx, store.InsertRevisionParams{ProfileID: p.ID, Revision: 1, SchemaVersion: 1,
		Data: json.RawMessage(`{"schema_version":1}`)}); err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"Data Engineer", "Analytics Engineer", "bad"} {
		if _, err := q.InsertJob(ctx, store.InsertJobParams{Title: title, CompanyName: "Acme", CompanyKey: "acme",
			RemotePolicy: "unknown", Countries: []string{}, EmploymentType: "unknown",
			ContentHash: "h1", DedupKey: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	return &env{q: q, loop: loop, mlflow: mlflow, m: m, ctx: ctx}, st
}

func count(t *testing.T, st *store.Store, sql string) int {
	t.Helper()
	var n int
	if err := st.Pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFollowRecordsEveryChampionChange(t *testing.T) {
	e, st := setup(t)
	e.loop.Follow(e.ctx)
	if s := e.loop.State(); s["champion"] != "none" || s["mlflow"] != "ok" {
		t.Fatalf("fresh: %v", s)
	}
	e.mlflow.set("1", "first model", false)
	e.loop.Follow(e.ctx)
	e.loop.Follow(e.ctx) // unchanged: no second activation
	e.mlflow.set("2", "new weights", false)
	e.loop.Follow(e.ctx)
	if n := count(t, st, `SELECT count(*) FROM model_activations`); n != 2 {
		t.Fatalf("activations = %d, want 2", n)
	}
	acts, _ := e.q.ListActivations(e.ctx, 10)
	if acts[0].Version != "2" || *acts[0].PreviousVersion != "1" || *acts[0].Reason != "new weights" || *acts[0].Gate != "passed" {
		t.Errorf("latest activation: %+v", acts[0])
	}
	// MLflow down: keep using version 2, report it.
	e.mlflow.set("", "", true)
	e.loop.Follow(e.ctx)
	if s := e.loop.State(); s["champion"] != "2" || s["mlflow"] != "unreachable" {
		t.Errorf("MLflow down: %v", s)
	}
}

func TestMatchBackfillAndInstantRollback(t *testing.T) {
	e, st := setup(t)
	e.loop.Follow(e.ctx)
	if err := e.loop.MatchPending(e.ctx); err != nil || e.m.batches.Load() != 0 {
		t.Fatalf("no champion must not match: %v, %d batches", err, e.m.batches.Load())
	}

	e.mlflow.set("1", "first", false)
	e.loop.Follow(e.ctx)
	if err := e.loop.MatchPending(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT count(*) FROM match_results`); n != 2 {
		t.Fatalf("results = %d, want 2 (the bad job is an item error)", n)
	}
	// Nothing new: the rejected job is not sent again, the others are done.
	before := e.m.batches.Load()
	_ = e.loop.MatchPending(e.ctx)
	if e.m.batches.Load() != before {
		t.Error("matched again with nothing pending")
	}

	// A job's content changes: only it is matched again.
	if _, err := st.Pool.Exec(e.ctx, `UPDATE jobs SET content_hash = 'h2' WHERE title = 'Data Engineer'`); err != nil {
		t.Fatal(err)
	}
	jobsBefore := e.m.jobs.Load()
	_ = e.loop.MatchPending(e.ctx)
	if got := e.m.jobs.Load() - jobsBefore; got != 1 {
		t.Errorf("rematched %d jobs, want 1", got)
	}

	// New champion: everything is matched again (backfill), the bad job too (a new version may
	// accept it).
	e.mlflow.set("2", "v2", false)
	e.loop.Follow(e.ctx)
	_ = e.loop.MatchPending(e.ctx)
	if n := count(t, st, `SELECT count(*) FROM match_results r JOIN model_versions v ON v.id = r.model_version_id WHERE v.version = '2'`); n != 2 {
		t.Errorf("v2 results = %d, want 2", n)
	}

	// Rollback to 1: its results are still there, nothing is recomputed.
	e.mlflow.set("1", "v2 was worse", false)
	e.loop.Follow(e.ctx)
	jobsBefore = e.m.jobs.Load()
	_ = e.loop.MatchPending(e.ctx)
	if e.m.jobs.Load() != jobsBefore {
		t.Errorf("rollback recomputed %d jobs", e.m.jobs.Load()-jobsBefore)
	}
	if e.loop.State()["champion"] != "1" {
		t.Errorf("champion after rollback: %v", e.loop.State())
	}
}

func TestBreakerPausesAfterFiveFailures(t *testing.T) {
	e, _ := setup(t)
	e.mlflow.set("1", "first", false)
	e.loop.Follow(e.ctx)
	e.m.down.Store(true)
	for range 5 {
		_ = e.loop.MatchPending(e.ctx)
	}
	e.m.down.Store(false)
	before := e.m.batches.Load()
	_ = e.loop.MatchPending(e.ctx) // paused for a minute: no call
	if e.m.batches.Load() != before {
		t.Error("matched while the breaker was open")
	}
}

func TestFollowRetriesEveryTickUntilAChampionIsKnown(t *testing.T) {
	e, _ := setup(t)
	e.mlflow.set("", "", true) // MLflow still starting
	e.loop.FollowEvery, e.loop.MatchEvery = time.Hour, 20*time.Millisecond
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { e.loop.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	e.mlflow.set("1", "first", false) // MLflow is up now
	deadline := time.Now().Add(5 * time.Second)
	for e.loop.State()["champion"] != "1" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if e.loop.State()["champion"] != "1" {
		t.Fatal("champion not picked up before the hourly follow")
	}
}
