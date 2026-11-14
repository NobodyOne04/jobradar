package rippling

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// testdata: jobs.json (the first two entries of rippling's board, 2026-11-14: one job listed
// twice, once per location; totalPages set to 1) and job.json (its detail, shortened).
func TestFetchMergesRepeatsEnrichesAndNormalizes(t *testing.T) {
	list, _ := os.ReadFile("testdata/jobs.json")
	detail, _ := os.ReadFile("testdata/job.json")
	const id = "84d388b6-7656-434c-8862-0312eb6b97ac"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rippling/jobs":
			if r.URL.Query().Get("page") != "0" {
				t.Errorf("query %q", r.URL.RawQuery)
			}
			_, _ = w.Write(list)
		case "/rippling/jobs/" + id:
			_, _ = w.Write(detail)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := &Adapter{Client: ingest.NewHTTPClient(1000), BaseURL: srv.URL + "/"}
	src := store.Source{BoardToken: "rippling", Config: json.RawMessage(`{}`)}
	got, err := a.Fetch(context.Background(), src)
	if err != nil || len(got) != 1 || got[0].ExternalID != id {
		t.Fatalf("got %d postings (%+v), err %v; want the two repeats merged into one", len(got), got, err)
	}
	full, err := a.Enrich(context.Background(), src, got[0])
	if err != nil {
		t.Fatal(err)
	}
	n, err := a.Normalize(full)
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "Business Operations Manager" || n.CompanyName != "Rippling" || n.RemotePolicy != "onsite" ||
		n.LocationText != "San Francisco, CA; New York, NY" || len(n.Countries) != 1 || n.Countries[0] != "US" ||
		n.EmploymentType != "full_time" || *n.SalaryMin != 185000 || *n.SalaryMax != 240000 ||
		*n.SalaryCurrency != "USD" || *n.SalaryPeriod != "year" || !strings.Contains(n.DescriptionHTML, "<div>") {
		t.Errorf("normalized: %+v", n)
	}
}
