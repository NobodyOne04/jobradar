package remoteok

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

// testdata/api.json: the legal notice and two jobs recorded from remoteok.com/api?tag=data on
// 2026-11-14, descriptions shortened, plus a copy (id 999001) with a nonsense salary of 21.
func TestFetchSkipsLegalNoticeAndNormalizes(t *testing.T) {
	body, err := os.ReadFile("testdata/api.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tag") != "data" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	a := &Adapter{Client: ingest.NewHTTPClient(1000), FeedURL: srv.URL}
	got, err := a.Fetch(context.Background(), store.Source{BoardToken: "data", Config: json.RawMessage(`{}`)})
	if err != nil || len(got) != 3 {
		t.Fatalf("got %d postings, err %v", len(got), err)
	}
	if got[0].ExternalID != "1137513" || got[0].URL != "https://remoteOK.com/remote-jobs/remote-ai-trainer-imerit-technology-1137513" {
		t.Errorf("posting: %+v", got[0])
	}

	n, err := a.Normalize(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "AI Trainer" || n.CompanyName != "iMerit Technology" || n.RemotePolicy != "remote" ||
		n.LocationText != "Remote: US, UK, Canada, Australia + 35 other countries" ||
		*n.SalaryMin != 50000 || *n.SalaryMax != 70000 || *n.SalaryCurrency != "USD" || *n.SalaryPeriod != "year" ||
		n.PostedAt.Format("2006-01-02") != "2026-10-14" {
		t.Errorf("normalized: %+v", n)
	}
	for _, i := range []int{1, 2} { // salary 0 and salary 21: unknown
		if n, _ := a.Normalize(got[i]); n.SalaryMin != nil || n.SalaryCurrency != nil {
			t.Errorf("posting %s: salary %v, want unknown", got[i].ExternalID, *n.SalaryMin)
		}
	}
}

func TestValidateConfigTakesNoFilters(t *testing.T) {
	if err := New().ValidateConfig(json.RawMessage(`{"tag":"x"}`)); err == nil {
		t.Error("config filter accepted")
	}
}
