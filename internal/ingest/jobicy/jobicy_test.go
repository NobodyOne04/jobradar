package jobicy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// testdata/jobs.json: three jobs recorded from jobicy.com/api/jobs?count=3&tag=data on
// 2026-11-14, descriptions shortened, nextCursor cleared.
func TestFetchFollowsCursorAndNormalizes(t *testing.T) {
	body, err := os.ReadFile("testdata/jobs.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("tag") != "data engineer" || q.Get("geo") != "usa" || q.Get("count") != "100" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		if q.Get("cursor") == "" { // first page: one job and a cursor to the rest
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jobs":       []map[string]any{{"id": 155336, "url": "https://jobicy.com/jobs/155336", "jobTitle": "x"}},
				"nextCursor": "c2",
			})
			return
		}
		_, _ = w.Write(body) // second page repeats 155336: kept once
	}))
	defer srv.Close()
	a := &Adapter{Client: ingest.NewHTTPClient(1000), FeedURL: srv.URL}
	got, err := a.Fetch(context.Background(), store.Source{BoardToken: "data engineer",
		Config: json.RawMessage(`{"geo":"usa"}`)})
	if err != nil || len(got) != 3 {
		t.Fatalf("got %d postings, err %v", len(got), err)
	}

	var page struct{ Jobs []json.RawMessage }
	_ = json.Unmarshal(body, &page)
	n, err := a.Normalize(ingest.RawPosting{Payload: page.Jobs[1]})
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "Director, Enterprise Portfolio Management" || n.CompanyName != "Clover Health" ||
		n.RemotePolicy != "remote" || n.EmploymentType != "full_time" || n.LocationText != "Remote: USA" ||
		*n.SalaryMin != 150000 || *n.SalaryMax != 195000 || *n.SalaryCurrency != "USD" || *n.SalaryPeriod != "year" ||
		n.PostedAt.Format("2006-01-02") != "2026-11-14" {
		t.Errorf("normalized: %+v", n)
	}
	n, _ = a.Normalize(ingest.RawPosting{Payload: page.Jobs[0]})
	if n.LocationText != "Remote: Canada, Mexico, USA" || n.SalaryMin != nil || n.SalaryCurrency != nil {
		t.Errorf("no-salary job: %+v", n)
	}
}

func TestValidateConfig(t *testing.T) {
	a := New()
	if err := a.ValidateConfig(json.RawMessage(`{"geo":"europe","industry":"engineering"}`)); err != nil {
		t.Error(err)
	}
	if err := a.ValidateConfig(json.RawMessage(`{"country":"de"}`)); err == nil {
		t.Error("unknown filter accepted")
	}
}
