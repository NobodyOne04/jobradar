package lever

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// testdata/postings.json: two postings recorded from api.lever.co/v0/postings/leverdemo on
// 2026-11-13 (texts shortened), plus one without salary, country, createdAt or workplace.
func load(t *testing.T) []json.RawMessage {
	t.Helper()
	body, err := os.ReadFile("testdata/postings.json")
	if err != nil {
		t.Fatal(err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatal(err)
	}
	return items
}

// server serves items in pages of the requested size; hits counts requests per host kind.
func server(t *testing.T, items []json.RawMessage, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		q := r.URL.Query()
		if q.Get("mode") != "json" {
			t.Errorf("mode = %q", q.Get("mode"))
		}
		skip, _ := strconv.Atoi(q.Get("skip"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		end := min(skip+limit, len(items))
		page := []json.RawMessage{}
		if skip < len(items) {
			page = items[skip:end]
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchPagesAndRegion(t *testing.T) {
	// 250 postings: pages of 100, 100, 50.
	base := load(t)[0]
	var items []json.RawMessage
	for i := range 250 {
		var m map[string]any
		_ = json.Unmarshal(base, &m)
		m["id"] = "id-" + strconv.Itoa(i)
		b, _ := json.Marshal(m)
		items = append(items, b)
	}
	var globalHits, euHits atomic.Int32
	global := server(t, items, &globalHits)
	eu := server(t, items[:1], &euHits)
	a := &Adapter{Client: ingest.NewHTTPClient(1000), GlobalURL: global.URL + "/", EUURL: eu.URL + "/"}

	got, err := a.Fetch(context.Background(), store.Source{BoardToken: "leverdemo", Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 250 || got[249].ExternalID != "id-249" || globalHits.Load() != 3 {
		t.Errorf("got %d postings in %d requests", len(got), globalHits.Load())
	}

	got, err = a.Fetch(context.Background(), store.Source{BoardToken: "x", Config: json.RawMessage(`{"region":"EU"}`)})
	if err != nil || len(got) != 1 || euHits.Load() != 1 {
		t.Errorf("EU region: %d postings, %d EU requests, %v", len(got), euHits.Load(), err)
	}
}

func TestNormalize(t *testing.T) {
	items := load(t)
	a := &Adapter{}
	j, err := a.Normalize(ingest.RawPosting{Payload: items[0]})
	if err != nil {
		t.Fatal(err)
	}
	if j.Title != "Customer Success Manager" || j.LocationText != "Atlanta, Georgia" ||
		j.RemotePolicy != "hybrid" || len(j.Countries) != 1 || j.Countries[0] != "US" {
		t.Errorf("fields: %+v", j)
	}
	if j.SalaryMin == nil || *j.SalaryMin != 85000 || *j.SalaryMax != 175000 ||
		*j.SalaryCurrency != "USD" || *j.SalaryPeriod != "year" {
		t.Errorf("salary: %v %v %v %v", j.SalaryMin, j.SalaryMax, j.SalaryCurrency, j.SalaryPeriod)
	}
	if j.PostedAt == nil || j.PostedAt.Year() < 2020 {
		t.Errorf("posted_at from createdAt: %v", j.PostedAt)
	}
	// The lists (skills, responsibilities) must be in the description the matcher reads.
	if !strings.Contains(j.DescriptionHTML, "<h3>Skill Set:</h3><ul>") {
		t.Errorf("lists missing from description: %.200q", j.DescriptionHTML)
	}
	if j.EmploymentType != "unknown" {
		t.Errorf("employment type guessed from commitment: %q", j.EmploymentType)
	}

	onsite, _ := a.Normalize(ingest.RawPosting{Payload: items[1]})
	if onsite.RemotePolicy != "onsite" {
		t.Errorf(`workplaceType "onsite": got %q`, onsite.RemotePolicy)
	}

	bare, err := a.Normalize(ingest.RawPosting{Payload: items[2]})
	if err != nil {
		t.Fatal(err)
	}
	if bare.RemotePolicy != "unknown" || bare.Countries != nil || bare.SalaryMin != nil ||
		bare.PostedAt != nil {
		t.Errorf("missing fields must stay unknown: %+v", bare)
	}
}

func TestValidateConfig(t *testing.T) {
	a := &Adapter{}
	for cfg, ok := range map[string]bool{
		`{}`: true, `{"region":"eu"}`: true, `{"region":"Global"}`: true,
		`{"region":"us"}`: false, `{"region":1}`: false, `{"site":"x"}`: false,
	} {
		if err := a.ValidateConfig(json.RawMessage(cfg)); (err == nil) != ok {
			t.Errorf("%s: err = %v", cfg, err)
		}
	}
}
