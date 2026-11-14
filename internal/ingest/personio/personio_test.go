package personio

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

// testdata/positions.xml: Personio's own board (personio.jobs.personio.de, 2026-11-14), the
// CDATA descriptions shortened.
func TestFetchConvertsXMLAndNormalizes(t *testing.T) {
	body, _ := os.ReadFile("testdata/positions.xml")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/personio.com/xml" || r.URL.RawQuery != "language=en" {
			t.Errorf("request %s", r.URL)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	a := &Adapter{Client: ingest.NewHTTPClient(1000), URLTemplate: srv.URL + "/%s.%s"}
	got, err := a.Fetch(context.Background(), store.Source{BoardToken: "personio", Config: json.RawMessage(`{"domain":"com"}`)})
	if err != nil || len(got) != 2 || got[0].ExternalID != "2885178" || got[0].URL != srv.URL+"/personio.com/job/2885178" {
		t.Fatalf("got %+v, err %v", got, err)
	}
	if !json.Valid(got[0].Payload) {
		t.Fatal("payload is not JSON")
	}
	n, err := a.Normalize(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "Business Development Representative (d/f/m)" || n.LocationText != "Berlin" ||
		n.RemotePolicy != "unknown" || n.EmploymentType != "full_time" ||
		!strings.HasPrefix(n.DescriptionHTML, "<h3>The Role: How you&#39;ll make an impact at Personio</h3><p>") ||
		n.PostedAt.Format("2006-01-02") != "2026-11-11" {
		t.Errorf("normalized: %+v", n)
	}
}

func TestDomainConfig(t *testing.T) {
	a := New(nil)
	for cfg, ok := range map[string]bool{`{}`: true, `{"domain":"com"}`: true, `{"domain":"org"}`: false, `{"region":"eu"}`: false} {
		if err := a.ValidateConfig(json.RawMessage(cfg)); (err == nil) != ok {
			t.Errorf("%s: err %v", cfg, err)
		}
	}
}
