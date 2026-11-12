package himalayas

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// testdata/search.json: two jobs recorded from himalayas.app/jobs/api/search
// (q=data engineer, seniority=Senior) on 2026-11-13, texts and country lists shortened, plus
// an expired copy and a copy with weekly pay, an unknown employment type and no restrictions.
var now = time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)

func fixtureJobs(t *testing.T) []json.RawMessage {
	t.Helper()
	body, err := os.ReadFile("testdata/search.json")
	if err != nil {
		t.Fatal(err)
	}
	var resp struct{ Jobs []json.RawMessage }
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Jobs
}

// searchServer returns pages[n-1] for page n and an empty page after; it checks the query.
func searchServer(t *testing.T, pages [][]json.RawMessage, wantQuery map[string]string) (*Adapter, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		for k, v := range wantQuery {
			if q.Get(k) != v {
				t.Errorf("param %s = %q, want %q", k, q.Get(k), v)
			}
		}
		page, _ := strconv.Atoi(q.Get("page"))
		jobs := []json.RawMessage{}
		if page >= 1 && page <= len(pages) {
			jobs = pages[page-1]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
	}))
	t.Cleanup(srv.Close)
	return &Adapter{Client: ingest.NewHTTPClient(1000), SearchURL: srv.URL, Now: func() time.Time { return now }}, &calls
}

func TestFetchSearchSkipsExpiredAndDuplicates(t *testing.T) {
	jobs := fixtureJobs(t)
	// Page 2 repeats a job from page 1, as happens when new jobs shift the pages.
	a, calls := searchServer(t, [][]json.RawMessage{jobs, {jobs[0]}},
		map[string]string{"q": "data engineer", "seniority": "Senior", "worldwide": "false", "sort": "recent"})
	src := store.Source{BoardToken: "data engineer",
		Config: json.RawMessage(`{"seniority":"Senior","worldwide":false}`)}
	got, err := a.Fetch(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || calls.Load() != 3 {
		t.Fatalf("got %d postings in %d requests, want 3 in 3 (expired and repeat skipped)", len(got), calls.Load())
	}
	for _, p := range got {
		if p.ExpiresAt == nil || !p.ExpiresAt.After(now) {
			t.Errorf("%s: expires_at %v", p.ExternalID, p.ExpiresAt)
		}
		if p.URL == "" || p.URL[:20] != "https://himalayas.ap" {
			t.Errorf("URL must be the applicationLink (terms a): %q", p.URL)
		}
	}
}

func TestFetchTruncatesBroadSearches(t *testing.T) {
	base := fixtureJobs(t)[0]
	var pages [][]json.RawMessage
	for p := range MaxPages + 5 {
		var m map[string]any
		_ = json.Unmarshal(base, &m)
		m["guid"] = "job-" + strconv.Itoa(p)
		b, _ := json.Marshal(m)
		pages = append(pages, []json.RawMessage{b})
	}
	a, calls := searchServer(t, pages, nil)
	got, err := a.Fetch(context.Background(), store.Source{BoardToken: "engineer", Config: json.RawMessage(`{}`)})
	if !errors.Is(err, ingest.ErrTruncated) || len(got) != MaxPages || int(calls.Load()) != MaxPages {
		t.Fatalf("got %d postings, %d requests, err %v", len(got), calls.Load(), err)
	}
}

func TestValidateConfig(t *testing.T) {
	a := &Adapter{}
	for cfg, ok := range map[string]bool{
		`{}`: true, `{"seniority":"Senior","country":"Germany","worldwide":true}`: true,
		`{"q":"x"}`: false, `{"seniority":""}`: false, `{"worldwide":"yes"}`: false, `{"seniority":3}`: false,
	} {
		if err := a.ValidateConfig(json.RawMessage(cfg)); (err == nil) != ok {
			t.Errorf("%s: err = %v", cfg, err)
		}
	}
}

func TestNormalize(t *testing.T) {
	jobs := fixtureJobs(t)
	a := &Adapter{}
	j, err := a.Normalize(ingest.RawPosting{Payload: jobs[0]})
	if err != nil {
		t.Fatal(err)
	}
	if j.CompanyName != "micro1" || j.RemotePolicy != "remote" || j.EmploymentType != "full_time" ||
		j.LocationText != "Remote: United States (UTC-10 to UTC+14)" {
		t.Errorf("fields: %+v", j)
	}
	if j.SalaryMin == nil || *j.SalaryMin != 350000 || *j.SalaryCurrency != "USD" || *j.SalaryPeriod != "year" {
		t.Errorf("salary: %v %v %v", j.SalaryMin, j.SalaryCurrency, j.SalaryPeriod)
	}
	if j.PostedAt == nil {
		t.Error("posted_at from pubDate missing")
	}

	weekly, _ := a.Normalize(ingest.RawPosting{Payload: jobs[3]})
	if weekly.SalaryPeriod != nil || weekly.EmploymentType != "unknown" || weekly.LocationText != "Remote: worldwide" {
		t.Errorf("weekly/unknown/worldwide: %+v", weekly)
	}
}
