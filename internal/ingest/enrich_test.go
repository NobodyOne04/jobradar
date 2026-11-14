package ingest_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// enrichingAdapter is a fakeAdapter whose list lacks the location: Enrich adds it, and
// counts its calls.
type enrichingAdapter struct {
	*fakeAdapter
	calls atomic.Int32
}

func (e *enrichingAdapter) Enrich(_ context.Context, _ store.Source, raw ingest.RawPosting) (ingest.RawPosting, error) {
	e.calls.Add(1)
	var p map[string]string
	_ = json.Unmarshal(raw.Payload, &p)
	p["location"] = "Enriched"
	raw.Payload, _ = json.Marshal(p)
	return raw, nil
}

func TestEnrichOnlyNewOrChangedPostings(t *testing.T) {
	e := setup(t)
	en := &enrichingAdapter{fakeAdapter: &fakeAdapter{kind: "greenhouse"}}
	e.syncer.Adapters = ingest.NewRegistry(en, e.b)
	en.set(map[string]string{"1": `{"title":"Data Engineer"}`, "2": `{"title":"ML Engineer"}`})

	if c := e.sync(t, e.srcA); c.Created != 2 || en.calls.Load() != 2 {
		t.Fatalf("first sync: %+v, enrich calls %d", c, en.calls.Load())
	}
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE location_text = 'Enriched'`); n != 2 {
		t.Errorf("enriched jobs = %d, want 2 (the stored payload is the enriched one)", n)
	}
	// Same list: nothing enriched, nothing updated.
	if c := e.sync(t, e.srcA); c.Unchanged != 2 || en.calls.Load() != 2 {
		t.Errorf("unchanged sync: %+v, enrich calls %d", c, en.calls.Load())
	}
	// One entry changes: only it is enriched again.
	en.set(map[string]string{"1": `{"title":"Senior Data Engineer"}`, "2": `{"title":"ML Engineer"}`})
	if c := e.sync(t, e.srcA); c.Updated != 1 || c.Unchanged != 1 || en.calls.Load() != 3 {
		t.Errorf("changed sync: %+v, enrich calls %d", c, en.calls.Load())
	}
}

func TestEmptyFirstSyncWarns(t *testing.T) {
	e := setup(t)
	e.a.set(map[string]string{})
	e.sync(t, e.srcA)
	runs, _ := e.q.ListRuns(e.ctx, store.ListRunsParams{SourceID: e.srcA.ID, Limit: 1})
	if runs[0].Status != "succeeded" || runs[0].Error == nil || !strings.Contains(*runs[0].Error, "check board_token") {
		t.Errorf("first empty run: %+v", runs[0])
	}
	// Later empty runs are normal: a company with no openings.
	e.sync(t, e.srcA)
	runs, _ = e.q.ListRuns(e.ctx, store.ListRunsParams{SourceID: e.srcA.ID, Limit: 1})
	if runs[0].Status != "succeeded" || runs[0].Error != nil {
		t.Errorf("second empty run: %+v", runs[0])
	}
}
