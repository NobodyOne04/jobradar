package ingest_test

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// feedAdapter is a fakeAdapter that is a feed with a request limit, like Remotive.
type feedAdapter struct{ *fakeAdapter }

func (feedAdapter) StaleAfter() time.Duration  { return ingest.FeedStaleAfter }
func (feedAdapter) MinInterval() time.Duration { return 6 * time.Hour }

func setupFeed(t *testing.T) (*env, feedAdapter, store.Source) {
	t.Helper()
	e := setup(t)
	f := feedAdapter{&fakeAdapter{kind: "remotive"}}
	e.syncer.Adapters = ingest.NewRegistry(e.a, e.b, f)
	src, err := e.q.CreateSource(e.ctx, store.CreateSourceParams{Kind: "remotive", BoardToken: "all",
		DisplayName: "Remotive", Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return e, f, src
}

func TestFeedClosesOnlyPostingsUnseenFor30Days(t *testing.T) {
	e, f, src := setupFeed(t)
	f.set(map[string]string{
		"1": `{"title":"Data Engineer","location":"Remote"}`,
		"2": `{"title":"ML Engineer","location":"Remote"}`,
	})
	e.sync(t, src)

	// "2" drops out of the feed's window: still open, and the run is a normal success.
	f.set(map[string]string{"1": `{"title":"Data Engineer","location":"Remote"}`})
	if c := e.sync(t, src); c.Closed != 0 {
		t.Errorf("closed %d right after dropping out, want 0", c.Closed)
	}
	runs, _ := e.q.ListRuns(e.ctx, store.ListRunsParams{SourceID: src.ID, Limit: 1})
	if runs[0].Status != "succeeded" {
		t.Errorf("run status %q, want succeeded (a feed is never 'partial' for being a window)", runs[0].Status)
	}

	// 29 days unseen: still open. 31 days: closed, and its job with it.
	if _, err := e.pool.Exec(e.ctx, `UPDATE source_postings SET last_seen_at = now() - interval '29 days'
		WHERE external_id = '2'`); err != nil {
		t.Fatal(err)
	}
	if c := e.sync(t, src); c.Closed != 0 {
		t.Errorf("closed %d at 29 days, want 0", c.Closed)
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE source_postings SET last_seen_at = now() - interval '31 days'
		WHERE external_id = '2'`); err != nil {
		t.Fatal(err)
	}
	if c := e.sync(t, src); c.Closed != 1 {
		t.Errorf("closed %d at 31 days, want 1", c.Closed)
	}
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE closed_at IS NULL`); n != 1 {
		t.Errorf("open jobs = %d, want 1", n)
	}
}

func TestMinIntervalLimitsSyncs(t *testing.T) {
	e, f, src := setupFeed(t)
	f.set(map[string]string{"1": `{"title":"Data Engineer","location":"Remote"}`})
	s := &ingest.Scheduler{Syncer: e.syncer, Interval: time.Hour, Concurrency: 2,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if err := s.SyncLocked(e.ctx, src); err != nil {
		t.Fatal(err)
	}
	var tooSoon *ingest.TooSoonError
	if err := s.SyncLocked(e.ctx, src); !errors.As(err, &tooSoon) || tooSoon.Wait < 5*time.Hour {
		t.Fatalf("second sync: err %v, want TooSoonError with ~6h left", err)
	}
	// Once 6 hours have passed since the last start, it may run again.
	if _, err := e.pool.Exec(e.ctx, `UPDATE sync_runs SET started_at = now() - interval '7 hours'`); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncLocked(e.ctx, src); err != nil {
		t.Fatalf("after 7h: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM sync_runs`); n != 2 {
		t.Errorf("runs = %d, want 2", n)
	}
	// Sources without a limit are unaffected.
	e.a.set(map[string]string{"9": `{"title":"Analyst","location":"Berlin"}`})
	for range 2 {
		if err := s.SyncLocked(e.ctx, e.srcA); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFeedParams(t *testing.T) {
	v, err := ingest.FeedParams(" data engineer ", "tag", json.RawMessage(`{"geo":"europe"}`), "geo", "industry")
	if err != nil || v.Encode() != "geo=europe&tag=data+engineer" {
		t.Errorf("got %q, %v", v.Encode(), err)
	}
	if v, _ := ingest.FeedParams("ALL", "tag", nil); len(v) != 0 {
		t.Errorf(`"all" sets no filter, got %q`, v.Encode())
	}
	for _, bad := range []string{`{"region":"eu"}`, `{"geo":1}`, `{"geo":""}`, `[]`} {
		if _, err := ingest.FeedParams("all", "tag", json.RawMessage(bad), "geo"); err == nil {
			t.Errorf("config %s accepted", bad)
		}
	}
}
