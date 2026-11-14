package remotive

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

// testdata/remote-jobs.json: three jobs recorded from remotive.com/api/remote-jobs on
// 2026-11-14 with the warning and legal notice, descriptions shortened.
func TestFetchAndNormalize(t *testing.T) {
	body, err := os.ReadFile("testdata/remote-jobs.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "category=software-dev" { // board_token "all": no search
			t.Errorf("query %q", r.URL.RawQuery)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	a := &Adapter{Client: ingest.NewHTTPClient(1000), FeedURL: srv.URL}
	got, err := a.Fetch(context.Background(), store.Source{BoardToken: "all",
		Config: json.RawMessage(`{"category":"software-dev"}`)})
	if err != nil || len(got) != 3 || got[1].ExternalID != "2091137" {
		t.Fatalf("got %+v, err %v", got, err)
	}
	n, err := a.Normalize(got[1])
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "Senior Data Engineer" || n.CompanyName != "Lemon.io" || n.RemotePolicy != "remote" ||
		n.EmploymentType != "full_time" || n.LocationText != "Remote: LATAM, Europe, USA, Canada, APAC" ||
		n.SalaryMin != nil || n.PostedAt == nil {
		t.Errorf("normalized: %+v", n)
	}
	if n, _ := a.Normalize(got[2]); n.EmploymentType != "contract" {
		t.Errorf("employment %q, want contract", n.EmploymentType)
	}
}

func TestLimitsFollowTheTerms(t *testing.T) {
	a := New()
	if a.MinInterval().Hours() < 6 || a.Client.Attempts > 2 {
		t.Errorf("min interval %v, attempts %d: must stay within 4 requests a day", a.MinInterval(), a.Client.Attempts)
	}
}
