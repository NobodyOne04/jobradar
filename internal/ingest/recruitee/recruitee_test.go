package recruitee

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

// testdata/offers.json: two of bunq's offers (2026-11-14), one remote and one hybrid, texts
// shortened, translations and questions dropped.
func TestFetchAndNormalize(t *testing.T) {
	body, _ := os.ReadFile("testdata/offers.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bunq/api/offers/" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	a := &Adapter{Client: ingest.NewHTTPClient(1000), URLTemplate: srv.URL + "/%s/api/offers/"}
	got, err := a.Fetch(context.Background(), store.Source{BoardToken: "bunq", Config: json.RawMessage(`{}`)})
	if err != nil || len(got) != 2 || got[0].ExternalID != "2821648" || got[0].URL != "https://careers.bunq.com/o/reporting-agent-france-3" {
		t.Fatalf("got %+v, err %v", got, err)
	}
	n, err := a.Normalize(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "Reporting Agent (France)" || n.CompanyName != "bunq" || n.RemotePolicy != "remote" ||
		n.EmploymentType != "full_time" || len(n.Countries) != 1 || n.Countries[0] != "FR" ||
		!strings.Contains(n.DescriptionHTML, "<h3>Requirements</h3>") || n.PostedAt.Format(time2) != "2026-11-10 07:45" ||
		n.SalaryMin != nil {
		t.Errorf("normalized: %+v", n)
	}
	if n, _ := a.Normalize(got[1]); n.RemotePolicy != "hybrid" || n.LocationText != "Amsterdam, Noord-Holland, Netherlands" {
		t.Errorf("hybrid offer: %+v", n)
	}
	withPay := json.RawMessage(`{"id":1,"title":"x","salary":{"min":"50000","max":70000,"period":"year","currency":"eur"}}`)
	if n, _ := a.Normalize(ingest.RawPosting{Payload: withPay}); n.SalaryMin == nil || *n.SalaryMin != 50000 ||
		*n.SalaryMax != 70000 || *n.SalaryCurrency != "EUR" || *n.SalaryPeriod != "year" {
		t.Errorf("salary: %+v", n)
	}
}

const time2 = "2006-01-02 15:04"
