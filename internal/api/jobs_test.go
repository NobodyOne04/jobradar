package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/NobodyOne04/jobradar/internal/applications"
	"github.com/NobodyOne04/jobradar/internal/store"
	"github.com/NobodyOne04/jobradar/internal/testdb"
)

type listed struct {
	Jobs []struct {
		ID      int64  `json:"id"`
		Title   string `json:"title"`
		Sources []struct {
			Kind, URL, Attribution string
		} `json:"sources"`
		Match *struct {
			Score        int    `json:"score"`
			Band         string `json:"band"`
			ModelVersion string `json:"model_version"`
			Stale        bool   `json:"stale"`
		} `json:"match"`
		Application struct{ Status string } `json:"application"`
	} `json:"jobs"`
	NextCursor *string `json:"next_cursor"`
}

func (l listed) titles() string {
	var out []string
	for _, j := range l.Jobs {
		out = append(out, j.Title)
	}
	return strings.Join(out, ", ")
}

// jobsFixture: a profile, champion v1 and an older v2, and jobs covering every case of ADR 0011.
func jobsFixture(t *testing.T) (*store.Store, map[string]int64) {
	t.Helper()
	st := testdb.Migrated(t)
	ctx := context.Background()
	q := store.New(st.Pool)
	p, _ := q.CreateProfile(ctx, "anna")
	rev, err := q.InsertRevision(ctx, store.InsertRevisionParams{ProfileID: p.ID, Revision: 1, SchemaVersion: 1, Data: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	v1, _ := q.UpsertModelVersion(ctx, store.UpsertModelVersionParams{Version: "1", MlflowRunID: "r1", Metrics: json.RawMessage(`{}`), Tags: json.RawMessage(`{}`)})
	v2, _ := q.UpsertModelVersion(ctx, store.UpsertModelVersionParams{Version: "2", MlflowRunID: "r2", Metrics: json.RawMessage(`{}`), Tags: json.RawMessage(`{}`)})
	if _, err := q.InsertActivation(ctx, store.InsertActivationParams{ModelVersionID: v1.ID}); err != nil {
		t.Fatal(err)
	}
	gh, _ := q.CreateSource(ctx, store.CreateSourceParams{Kind: "greenhouse", BoardToken: "acme", Config: json.RawMessage(`{}`)})
	hm, _ := q.CreateSource(ctx, store.CreateSourceParams{Kind: "himalayas", BoardToken: "data", Config: json.RawMessage(`{}`)})

	ids := map[string]int64{}
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i, c := range []struct {
		title, remote, text string
		score               int16
		band                string
		version             int64 // 0: no result (pending)
		src                 int64
	}{
		{"Senior Data Engineer", "remote", "Build Kafka pipelines", 80, "strong", v1.ID, gh.ID},
		{"Analytics Engineer", "hybrid", "dbt models for analytics", 60, "good", v1.ID, gh.ID},
		{"Excluded Role", "onsite", "On site only", 70, "excluded", v1.ID, gh.ID},
		{"Pending Role", "remote", "Not scored yet", 0, "", 0, gh.ID},
		{"Stale Role", "remote", "Scored by an older model", 90, "strong", v2.ID, hm.ID},
	} {
		posted := day.Add(time.Duration(i) * 24 * time.Hour)
		id, err := q.InsertJob(ctx, store.InsertJobParams{Title: c.title, CompanyName: "Acme", CompanyKey: "acme",
			DescriptionText: c.text, RemotePolicy: c.remote, Countries: []string{}, EmploymentType: "full_time",
			PostedAt: &posted, ContentHash: "h", DedupKey: c.title})
		if err != nil {
			t.Fatal(err)
		}
		ids[c.title] = id
		if err := q.InsertPosting(ctx, store.InsertPostingParams{SourceID: c.src, ExternalID: c.title, JobID: id,
			Url: "https://jobs.example/" + c.title, RawPayload: json.RawMessage(`{}`), PayloadHash: "p"}); err != nil {
			t.Fatal(err)
		}
		if c.version != 0 {
			if err := q.InsertMatchResult(ctx, store.InsertMatchResultParams{JobID: id, ProfileRevisionID: rev.ID,
				ModelVersionID: c.version, JobContentHash: "h", Score: c.score, Band: c.band, Confidence: "medium",
				Completeness: 0.8, HardConstraints: "pass",
				Details: json.RawMessage(`{"explanations":["Missing required: Flink"],"skills":{"missing":[{"id":"flink"}]}}`)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	closedID, _ := q.InsertJob(ctx, store.InsertJobParams{Title: "Closed Role", CompanyName: "Acme", CompanyKey: "acme",
		RemotePolicy: "remote", Countries: []string{}, EmploymentType: "full_time", ContentHash: "h", DedupKey: "closed"})
	if _, err := st.Pool.Exec(ctx, `UPDATE jobs SET closed_at = now() WHERE id = $1`, closedID); err != nil {
		t.Fatal(err)
	}
	ids["Closed Role"] = closedID
	if _, err := (&applications.Service{Pool: st.Pool}).Set(ctx, applications.Change{
		JobID: ids["Analytics Engineer"], ProfileID: p.ID, Status: "applied", Note: "sent CV"}); err != nil {
		t.Fatal(err)
	}
	return st, ids
}

func TestJobsList(t *testing.T) {
	st, _ := jobsFixture(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHandler(log, readyOK, 5*time.Second, []string{"example.com"}, (&Jobs{Queries: store.New(st.Pool), Log: log}).Register)
	list := func(query string) listed {
		t.Helper()
		rec := do(h, "GET", "/api/v1/jobs?"+query, "")
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
		}
		var l listed
		if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		return l
	}

	// Default: by score; excluded and closed hidden; the pending job after every scored one.
	all := list("")
	if got := all.titles(); got != "Stale Role, Senior Data Engineer, Analytics Engineer, Pending Role" {
		t.Errorf("default order: %s", got)
	}
	stale, pending := all.Jobs[0], all.Jobs[3]
	if stale.Match == nil || !stale.Match.Stale || stale.Match.ModelVersion != "2" || pending.Match != nil {
		t.Errorf("stale/pending match: %+v / %+v", stale.Match, pending.Match)
	}
	if all.Jobs[1].Match.Stale {
		t.Error("champion result marked stale")
	}
	if a := stale.Sources[0]; a.Kind != "himalayas" || a.Attribution != "via Himalayas" {
		t.Errorf("Himalayas attribution: %+v", a)
	}
	if all.Jobs[2].Application.Status != "applied" || all.Jobs[1].Application.Status != "discovered" {
		t.Errorf("application status: %+v", all.Jobs)
	}

	for query, want := range map[string]string{
		"band=excluded":                          "Excluded Role",
		"band=good&band=excluded":                "Excluded Role, Analytics Engineer",
		"min_score=70":                           "Stale Role, Senior Data Engineer",
		"status=applied":                         "Analytics Engineer",
		"status=discovered":                      "Stale Role, Senior Data Engineer, Pending Role",
		"q=" + url.QueryEscape("analytics"):      "Analytics Engineer",
		"q=" + url.QueryEscape("kafka pipeline"): "Senior Data Engineer", // stemmed: pipelines
		"remote=remote":                          "Stale Role, Senior Data Engineer, Pending Role",
		"source=himalayas":                       "Stale Role",
		"sort=posted":                            "Stale Role, Pending Role, Analytics Engineer, Senior Data Engineer",
		"posted_after=2026-11-04T00:00:00Z":      "Stale Role, Pending Role",
	} {
		if got := list(query).titles(); got != want {
			t.Errorf("%s: got %q, want %q", query, got, want)
		}
	}

	// Keyset pages: no repeats, no gaps, and a cursor only works with its own filters.
	page1 := list("limit=2")
	if page1.NextCursor == nil || page1.titles() != "Stale Role, Senior Data Engineer" {
		t.Fatalf("page 1: %s %v", page1.titles(), page1.NextCursor)
	}
	page2 := list("limit=2&cursor=" + *page1.NextCursor)
	if page2.titles() != "Analytics Engineer, Pending Role" || page2.NextCursor != nil {
		t.Errorf("page 2: %s, next %v", page2.titles(), page2.NextCursor)
	}
	assertProblem(t, do(h, "GET", "/api/v1/jobs?sort=posted&cursor="+*page1.NextCursor, ""), 422)
	assertProblem(t, do(h, "GET", "/api/v1/jobs?cursor=garbage", ""), 422)

	for _, q := range []string{"band=bogus", "min_score=101", "sort=random", "limit=0", "remote=mars",
		"status=hired", "posted_after=yesterday", "q=" + strings.Repeat("x", 201)} {
		assertProblem(t, do(h, "GET", "/api/v1/jobs?"+q, ""), 422)
	}
}

func TestJobDetail(t *testing.T) {
	st, ids := jobsFixture(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHandler(log, readyOK, 5*time.Second, []string{"example.com"}, (&Jobs{Queries: store.New(st.Pool), Log: log}).Register)
	get := func(id int64) map[string]any {
		t.Helper()
		rec := do(h, "GET", "/api/v1/jobs/"+itoa(id), "")
		if rec.Code != 200 {
			t.Fatalf("%d: %s", rec.Code, rec.Body)
		}
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}

	d := get(ids["Senior Data Engineer"])
	match := d["match"].(map[string]any)
	if d["description_text"] != "Build Kafka pipelines" || match["band"] != "strong" || match["stale"] != false ||
		match["explanations"].([]any)[0] != "Missing required: Flink" {
		t.Errorf("detail: %v", d)
	}
	if d["application"].(map[string]any)["status"] != "discovered" {
		t.Errorf("application: %v", d["application"])
	}
	applied := get(ids["Analytics Engineer"])["application"].(map[string]any)
	if applied["status"] != "applied" || len(applied["events"].([]any)) != 1 {
		t.Errorf("applied: %v", applied)
	}
	if get(ids["Pending Role"])["match"] != nil {
		t.Error("pending job has a match")
	}
	if get(ids["Closed Role"])["closed_at"] == nil {
		t.Error("closed job detail lacks closed_at")
	}
	assertProblem(t, do(h, "GET", "/api/v1/jobs/999999", ""), 404)
}

// Every board that is not a company's own needs the credit its terms ask for (ADR 0013).
func TestNonCompanyKindsHaveAttribution(t *testing.T) {
	company := map[string]bool{"greenhouse": true, "lever": true, "ashby": true}
	for _, k := range sourceKinds {
		if !company[k] && attributions[k] == "" {
			t.Errorf("kind %q has no attribution", k)
		}
	}
}
