package greenhouse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// testdata/jobs.json: two jobs recorded from boards-api.greenhouse.io/v1/boards/gitlab on
// 2026-11-13 (descriptions shortened), plus job 999 without company_name/first_published.
func fixture(t *testing.T) (*Adapter, []ingest.RawPosting) {
	t.Helper()
	body, err := os.ReadFile("testdata/jobs.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gitlab/jobs" || r.URL.Query().Get("content") != "true" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	a := &Adapter{Client: ingest.NewHTTPClient(1000), BaseURL: srv.URL + "/"}
	postings, err := a.Fetch(context.Background(), store.Source{BoardToken: "gitlab"})
	if err != nil {
		t.Fatal(err)
	}
	return a, postings
}

func TestFetch(t *testing.T) {
	_, postings := fixture(t)
	if len(postings) != 3 {
		t.Fatalf("got %d postings, want 3", len(postings))
	}
	p := postings[0]
	if p.ExternalID != "8638232002" || !strings.HasPrefix(p.URL, "https://") || len(p.Payload) == 0 {
		t.Errorf("unexpected posting: %+v", p)
	}
}

func TestNormalize(t *testing.T) {
	a, postings := fixture(t)
	j, err := a.Normalize(postings[0])
	if err != nil {
		t.Fatal(err)
	}
	if j.Title != "AI Transformation Owner, CRO" || j.CompanyName != "GitLab" ||
		j.LocationText != "Remote, United States" {
		t.Errorf("fields: %+v", j)
	}
	if !strings.HasPrefix(j.DescriptionHTML, `<div class="content-intro">`) {
		t.Errorf("content not unescaped: %.60q", j.DescriptionHTML)
	}
	if j.PostedAt == nil || j.PostedAt.Format("2006-01-02") != "2026-07-22" {
		t.Errorf("posted_at from first_published: %v", j.PostedAt)
	}
	// Nothing structured to say about these: unknown, never guessed from "Remote, ...".
	if j.RemotePolicy != "unknown" || j.EmploymentType != "unknown" || j.SalaryMin != nil {
		t.Errorf("guessed values: %+v", j)
	}

	fallback, err := a.Normalize(postings[2])
	if err != nil {
		t.Fatal(err)
	}
	if fallback.CompanyName != "" || fallback.PostedAt != nil {
		t.Errorf("missing fields should stay empty: %+v", fallback)
	}
}

func TestFetchEscapesBoardToken(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	}))
	defer srv.Close()
	a := &Adapter{Client: ingest.NewHTTPClient(1000), BaseURL: srv.URL + "/"}
	if _, err := a.Fetch(context.Background(), store.Source{BoardToken: "a/../b?x"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/a%2F..%2Fb%3Fx/jobs" {
		t.Errorf("path = %q", gotPath)
	}
}
