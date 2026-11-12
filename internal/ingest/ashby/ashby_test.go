package ashby

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// testdata/board.json: two jobs recorded from api.ashbyhq.com/posting-api/job-board/Ashby on
// 2026-11-13 (texts and tiers shortened), a job with no compensation, workplace, type,
// date or address, and an unlisted job.
func fetch(t *testing.T) (*Adapter, []ingest.RawPosting) {
	t.Helper()
	body, err := os.ReadFile("testdata/board.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Ashby" || r.URL.Query().Get("includeCompensation") != "true" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	a := &Adapter{Client: ingest.NewHTTPClient(1000), BaseURL: srv.URL + "/"}
	postings, err := a.Fetch(context.Background(), store.Source{BoardToken: "Ashby"})
	if err != nil {
		t.Fatal(err)
	}
	return a, postings
}

func TestFetchSkipsUnlisted(t *testing.T) {
	_, postings := fetch(t)
	if len(postings) != 3 {
		t.Fatalf("got %d postings, want 3 (the unlisted one skipped)", len(postings))
	}
	for _, p := range postings {
		if p.ExternalID == "00000000-0000-0000-0000-000000000998" {
			t.Error("unlisted posting fetched")
		}
	}
}

func TestNormalize(t *testing.T) {
	a, postings := fetch(t)
	j, err := a.Normalize(postings[0])
	if err != nil {
		t.Fatal(err)
	}
	if j.Title != "Engineering Manager - EU" || j.RemotePolicy != "remote" ||
		j.EmploymentType != "full_time" || j.LocationText != "Remote - European Union; Spain; Italy; Germany" {
		t.Errorf("fields: %+v", j)
	}
	if j.SalaryMin == nil || *j.SalaryMin != 150000 || *j.SalaryMax != 250000 ||
		*j.SalaryCurrency != "EUR" || *j.SalaryPeriod != "year" {
		t.Errorf("salary: %v %v %v %v", j.SalaryMin, j.SalaryMax, j.SalaryCurrency, j.SalaryPeriod)
	}
	if j.PostedAt == nil || j.PostedAt.Format("2006-01-02") != "2024-03-04" || j.Countries != nil {
		t.Errorf("posted_at %v, countries %v", j.PostedAt, j.Countries)
	}

	// Several regional tiers: the summary's single range is used.
	multi, _ := a.Normalize(postings[1])
	if multi.SalaryMin == nil || *multi.SalaryMin != 110000 || *multi.SalaryMax != 195500 {
		t.Errorf("multi-tier salary: %v-%v", multi.SalaryMin, multi.SalaryMax)
	}

	bare, err := a.Normalize(postings[2])
	if err != nil {
		t.Fatal(err)
	}
	if bare.RemotePolicy != "unknown" || bare.EmploymentType != "unknown" || bare.SalaryMin != nil ||
		bare.PostedAt != nil || bare.LocationText != "Somewhere" {
		t.Errorf("missing fields must stay unknown: %+v", bare)
	}
}
