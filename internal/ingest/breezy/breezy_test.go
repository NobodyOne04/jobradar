package breezy

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

// testdata/positions.json: Breezy's own demo board (breezy.breezy.hr/json, 2026-11-14), as is.
func TestFetchAndNormalize(t *testing.T) {
	body, _ := os.ReadFile("testdata/positions.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/breezy/json" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	a := &Adapter{Client: ingest.NewHTTPClient(1000), URLTemplate: srv.URL + "/%s/json"}
	got, err := a.Fetch(context.Background(), store.Source{BoardToken: "breezy", Config: json.RawMessage(`{}`)})
	if err != nil || len(got) != 3 || got[0].URL != "https://breezy.breezy.hr/p/98323abf2296-employee-12" {
		t.Fatalf("got %+v, err %v", got, err)
	}
	n, err := a.Normalize(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "Employee #12" || n.CompanyName != "MS Breezy Trial" || n.LocationText != "Chaos, FL" ||
		n.RemotePolicy != "unknown" || n.EmploymentType != "unknown" || n.Countries[0] != "US" ||
		n.DescriptionHTML != "" || n.SalaryMin != nil || n.PostedAt.Format("2006-01-02") != "2024-02-15" {
		t.Errorf("normalized: %+v", n)
	}
	if n, _ := a.Normalize(got[2]); n.EmploymentType != "full_time" {
		t.Errorf("fullTime: %q", n.EmploymentType)
	}
}
