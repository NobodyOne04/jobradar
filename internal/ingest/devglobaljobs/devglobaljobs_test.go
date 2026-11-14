package devglobaljobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// testdata: list.json (two jobs from /api/v1/jobs?limit=3&search=data, 2026-11-14) and
// job.json (the first one's detail), descriptions shortened.
var now = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func TestFetchListsThenReadsEachJob(t *testing.T) {
	list, _ := os.ReadFile("testdata/list.json")
	detail, _ := os.ReadFile("testdata/job.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			q := r.URL.Query()
			if q.Get("search") != "data" || q.Get("country") != "de" || q.Get("limit") != "100" || q.Get("offset") != "0" {
				t.Errorf("list query %q", r.URL.RawQuery)
			}
			_, _ = w.Write(list)
		case "/176858108":
			_, _ = w.Write(detail)
		case "/176858105": // the second job: already closed, with a past closingDate
			_, _ = w.Write([]byte(`{"id":176858105,"title":"Old","url":"https://devglobaljobs.com/jobs/detail/176858105",
				"country":"Remote","jobType":"Contract","closingDate":"2026-12-01T00:00:00.000Z"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := &Adapter{Client: ingest.NewHTTPClient(1000), BaseURL: srv.URL, Now: func() time.Time { return now }}
	got, err := a.Fetch(context.Background(), store.Source{BoardToken: "data", Config: json.RawMessage(`{"country":"de"}`)})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d postings, err %v", len(got), err)
	}
	if got[0].ExternalID != "176858108" || got[0].URL != "https://devglobaljobs.com/jobs/detail/176858108" || got[0].ExpiresAt != nil {
		t.Errorf("posting 0: %+v", got[0])
	}
	if got[1].ExpiresAt == nil || got[1].ExpiresAt.Format("2006-01-02") != "2026-12-01" {
		t.Errorf("posting 1 expiry: %v", got[1].ExpiresAt)
	}

	n, err := a.Normalize(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "Senior Data & Analytics Engineer" || n.CompanyName != "Polarsteps" || n.LocationText != "Amsterdam" ||
		n.RemotePolicy != "unknown" || n.EmploymentType != "full_time" || len(n.Countries) != 0 ||
		!strings.HasPrefix(n.DescriptionHTML, "<p>") || n.PostedAt.Format("2006-01-02") != "2026-11-14" {
		t.Errorf("normalized: %+v", n)
	}
	n, _ = a.Normalize(got[1])
	if n.RemotePolicy != "remote" || n.EmploymentType != "contract" {
		t.Errorf("remote job: %+v", n)
	}
	n, _ = a.Normalize(ingest.RawPosting{Payload: json.RawMessage(`{"id":1,"title":"x","country":"de"}`)})
	if len(n.Countries) != 1 || n.Countries[0] != "DE" {
		t.Errorf("countries %v, want [DE]", n.Countries)
	}
}

func TestFetchSkipsJobsPastClosingDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			t.Errorf("expired job fetched: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"jobs":[{"id":5,"closingDate":"2026-11-01T00:00:00.000Z"}],"meta":{"hasMore":false}}`))
	}))
	defer srv.Close()
	a := &Adapter{Client: ingest.NewHTTPClient(1000), BaseURL: srv.URL, Now: func() time.Time { return now }}
	got, err := a.Fetch(context.Background(), store.Source{BoardToken: "all", Config: json.RawMessage(`{}`)})
	if err != nil || len(got) != 0 {
		t.Fatalf("got %d, err %v", len(got), err)
	}
}

func TestEscapedDescriptionIsUnescaped(t *testing.T) {
	a := New()
	for payload, want := range map[string]string{
		`{"id":1,"title":"x","description":"&lt;p&gt;At Lyft &amp;amp; co&lt;/p&gt;"}`: "<p>At Lyft &amp; co</p>",
		`{"id":2,"title":"x","description":"<p>Fish &amp; chips</p>"}`:                 "<p>Fish &amp; chips</p>",
		`{"id":3,"title":"x","description":"Plain text, 3 &lt; 5"}`:                    "Plain text, 3 < 5",
		// An escaped body with a real-HTML footer, as Dev Global sends some.
		`{"id":4,"title":"x","description":"&lt;div&gt;&lt;p&gt;Body&lt;/p&gt;&lt;p&gt;More&lt;/p&gt;&lt;/div&gt;<p>Find <a href=\"https://x.test\">more</a></p>"}`: `<div><p>Body</p><p>More</p></div><p>Find <a href="https://x.test">more</a></p>`,
		// Real HTML that mentions "1 &lt; 2" once stays as is.
		`{"id":5,"title":"x","description":"<p>Real</p><p>HTML, 1 &lt; 2</p>"}`: "<p>Real</p><p>HTML, 1 &lt; 2</p>",
	} {
		n, err := a.Normalize(ingest.RawPosting{Payload: json.RawMessage(payload)})
		if err != nil || n.DescriptionHTML != want {
			t.Errorf("%s: got %q, %v; want %q", payload, n.DescriptionHTML, err, want)
		}
		if text := ingest.HTMLToText(n.DescriptionHTML); strings.Contains(text, "<p>") {
			t.Errorf("%s: text still has tags: %q", payload, text)
		}
	}
}
