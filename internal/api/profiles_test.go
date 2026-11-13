package api

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NobodyOne04/jobradar/internal/profiles"
	"github.com/NobodyOne04/jobradar/internal/store"
	"github.com/NobodyOne04/jobradar/internal/testdb"
)

func TestProfilesAPI(t *testing.T) {
	st := testdb.Migrated(t)
	var logs strings.Builder
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	p := &Profiles{Queries: store.New(st.Pool), Service: &profiles.Service{Pool: st.Pool}, Log: log}
	h := NewHandler(log, readyOK, 5*time.Second, []string{"example.com"}, p.Register)

	src, _ := os.ReadFile("../../profiles/example.yaml")
	doc, _ := profiles.FromYAML(src)

	assertProblem(t, do(h, "POST", "/api/v1/profiles", `{"name":"Anna Smith"}`), 422)
	if rec := do(h, "POST", "/api/v1/profiles", `{"name":"anna"}`); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	assertProblem(t, do(h, "POST", "/api/v1/profiles", `{"name":"anna"}`), 409)

	if rec := do(h, "POST", "/api/v1/profiles/1/revisions", string(doc)); rec.Code != 201 {
		t.Fatalf("revision: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/profiles/1/revisions", string(doc)); rec.Code != 200 {
		t.Errorf("same document again: %d, want 200", rec.Code)
	}
	rec := do(h, "POST", "/api/v1/profiles/1/revisions", `{"schema_version":1,"seniority":"wizard"}`)
	assertProblem(t, rec, 422)
	if !strings.Contains(rec.Body.String(), "/seniority") {
		t.Errorf("problem lacks the path: %s", rec.Body)
	}
	assertProblem(t, do(h, "POST", "/api/v1/profiles/99/revisions", string(doc)), 404)

	rec = do(h, "GET", "/api/v1/profiles/1/revisions/latest", "")
	var rev store.ProfileRevision
	_ = json.Unmarshal(rec.Body.Bytes(), &rev)
	if rec.Code != 200 || rev.Revision != 1 || rev.SchemaVersion != 1 || !strings.Contains(string(rev.Data), "Example Logistics") {
		t.Errorf("latest: %d %s", rec.Code, rec.Body)
	}
	assertProblem(t, do(h, "GET", "/api/v1/profiles/1/revisions/7", ""), 404)
	assertProblem(t, do(h, "GET", "/api/v1/profiles/1/revisions/abc", ""), 404)

	rec = do(h, "GET", "/api/v1/profiles", "")
	if !strings.Contains(rec.Body.String(), `"latest_revision":1`) {
		t.Errorf("list: %s", rec.Body)
	}

	// Profile content never reaches the logs.
	if strings.Contains(logs.String(), "Example Logistics") || strings.Contains(logs.String(), "Airflow") {
		t.Errorf("profile content logged:\n%s", logs.String())
	}
}
