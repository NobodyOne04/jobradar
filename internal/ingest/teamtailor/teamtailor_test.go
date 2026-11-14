package teamtailor

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

// testdata/jobs.rss: two items of career.teamtailor.com/jobs.rss (2026-11-14), descriptions
// shortened.
func TestFetchConvertsRSSAndNormalizes(t *testing.T) {
	body, _ := os.ReadFile("testdata/jobs.rss")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jobs.rss" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	a := &Adapter{Client: ingest.NewHTTPClient(1000), Scheme: "http://", AllowHost: host}
	got, err := a.Fetch(context.Background(), store.Source{BoardToken: host, Config: json.RawMessage(`{}`)})
	if err != nil || len(got) != 2 || got[0].ExternalID != "f6c31512-671a-4e1c-be54-c8a1e2552144" ||
		got[0].URL != "https://career.teamtailor.com/jobs/8742767-customer-success-manager-french-speaking" {
		t.Fatalf("got %+v, err %v", got, err)
	}
	n, err := a.Normalize(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "Customer Success Manager - French Speaking" || n.RemotePolicy != "hybrid" ||
		n.LocationText != "Stockholm, Sweden; Barcelona, Spain" || !strings.HasPrefix(n.DescriptionHTML, "<figure>") ||
		n.PostedAt.UTC().Format("2006-01-02 15:04") != "2026-11-13 14:00" {
		t.Errorf("normalized: %+v", n)
	}
}

func TestHostValidationRefusesInternalTargets(t *testing.T) {
	a := New(nil)
	for _, bad := range []string{"localhost", "127.0.0.1", "backend", "backend:8080", "10.0.0.5", "[::1]",
		"career.example.com/path", "evil.com:22", "postgres.local", "x.internal", "", "a..b.com"} {
		if a.ValidateBoardToken(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"career.teamtailor.com", "Careers.Example.co.uk"} {
		if err := a.ValidateBoardToken(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}
