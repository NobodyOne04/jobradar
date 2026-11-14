package smartrecruiters

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

// testdata: postings.json (two of BoschGroup's list, 2026-11-14, totalFound set to 2) and
// posting.json (the first one's detail, sections shortened).
func TestFetchEnrichNormalize(t *testing.T) {
	list, _ := os.ReadFile("testdata/postings.json")
	detail, _ := os.ReadFile("testdata/posting.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/BoschGroup/postings":
			if r.URL.Query().Get("offset") != "0" || r.URL.Query().Get("limit") != "100" {
				t.Errorf("query %q", r.URL.RawQuery)
			}
			_, _ = w.Write(list)
		case "/BoschGroup/postings/744000161629479":
			_, _ = w.Write(detail)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := &Adapter{Client: ingest.NewHTTPClient(1000), BaseURL: srv.URL + "/"}
	src := store.Source{BoardToken: "BoschGroup", Config: json.RawMessage(`{}`)}
	got, err := a.Fetch(context.Background(), src)
	if err != nil || len(got) != 2 || got[0].ExternalID != "744000161629479" ||
		got[0].URL != "https://jobs.smartrecruiters.com/BoschGroup/744000161629479" {
		t.Fatalf("got %+v, err %v", got, err)
	}
	full, err := a.Enrich(context.Background(), src, got[0])
	if err != nil {
		t.Fatal(err)
	}
	if full.URL != "https://jobs.smartrecruiters.com/BoschGroup/744000161629479-manufacturing-quality-engineer" {
		t.Errorf("url %s", full.URL)
	}
	n, err := a.Normalize(full)
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "Manufacturing Quality Engineer" || n.CompanyName != "Bosch Group" || n.RemotePolicy != "unknown" ||
		n.EmploymentType != "full_time" || len(n.Countries) != 1 || n.Countries[0] != "TH" ||
		!strings.HasPrefix(n.DescriptionHTML, "<h3>Company Description</h3>") || !strings.Contains(n.DescriptionHTML, "<h3>Qualifications</h3>") ||
		n.PostedAt.Format("2006-01-02") != "2026-11-14" {
		t.Errorf("normalized: %+v", n)
	}
	if _, err := a.Enrich(context.Background(), src, got[1]); err == nil {
		t.Error("enrich of a missing posting succeeded")
	}
}

func TestBoardTokenIsASlug(t *testing.T) {
	a := New(nil)
	for _, bad := range []string{"", "a/b", "../x", "x?y", "a b"} {
		if a.ValidateBoardToken(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := a.ValidateBoardToken("Ubisoft2"); err != nil {
		t.Error(err)
	}
}
