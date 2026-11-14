package workable

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

// testdata/account.json: two of huggingface's jobs (2026-11-14), descriptions shortened.
func TestFetchAndNormalize(t *testing.T) {
	body, _ := os.ReadFile("testdata/account.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/huggingface" || r.URL.RawQuery != "details=true" {
			t.Errorf("request %s", r.URL)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	a := &Adapter{Client: ingest.NewHTTPClient(1000), BaseURL: srv.URL + "/"}
	got, err := a.Fetch(context.Background(), store.Source{BoardToken: "huggingface", Config: json.RawMessage(`{}`)})
	if err != nil || len(got) != 2 || got[0].ExternalID != "81B46579FE" || got[0].URL != "https://apply.workable.com/j/81B46579FE" {
		t.Fatalf("got %+v, err %v", got, err)
	}
	n, err := a.Normalize(got[1])
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "Open-Source Machine Learning Engineer - US Remote" || n.RemotePolicy != "remote" ||
		n.EmploymentType != "full_time" || n.LocationText != "New York, United States" ||
		len(n.Countries) != 1 || n.Countries[0] != "US" || n.PostedAt.Format("2006-01-02") != "2026-05-29" ||
		n.CompanyName != "" || n.DescriptionHTML == "" {
		t.Errorf("normalized: %+v", n)
	}
}
