package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/NobodyOne04/jobradar/internal/applications"
	"github.com/NobodyOne04/jobradar/internal/store"
	"github.com/NobodyOne04/jobradar/internal/testdb"
)

func TestApplicationsAPI(t *testing.T) {
	st := testdb.Migrated(t)
	q := store.New(st.Pool)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &Applications{Queries: q, Service: &applications.Service{Pool: st.Pool}, Log: log}
	h := NewHandler(log, readyOK, 5*time.Second, []string{"example.com"}, a.Register)
	ctx := context.Background()

	job, err := q.InsertJob(ctx, store.InsertJobParams{
		Title: "Senior Data Engineer", CompanyName: "Acme", CompanyKey: "acme", RemotePolicy: "remote",
		Countries: []string{"DE"}, EmploymentType: "full_time", ContentHash: "h", DedupKey: "k",
		DescriptionText: "Python and Kafka",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/applications/" + itoa(job)

	// No profile yet: the API says what to do.
	rec := do(h, "PUT", path, `{"status":"saved"}`)
	assertProblem(t, rec, 422)
	if !strings.Contains(rec.Body.String(), "import a profile first") {
		t.Errorf("no-profile message: %s", rec.Body)
	}
	if _, err := q.CreateProfile(ctx, "anna"); err != nil {
		t.Fatal(err)
	}

	if rec = do(h, "GET", path, ""); !strings.Contains(rec.Body.String(), `"status":"discovered"`) {
		t.Errorf("discovered: %s", rec.Body)
	}
	if rec = do(h, "PUT", path, `{"status":"applied","note":"sent CV","occurred_at":"2026-11-09T10:00:00Z"}`); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `"changed":true`) {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	if rec = do(h, "PUT", path, `{"status":"applied"}`); !strings.Contains(rec.Body.String(), `"changed":false`) {
		t.Errorf("idempotent repeat: %s", rec.Body)
	}
	assertProblem(t, do(h, "PUT", path, `{"status":"saved"}`), 409)
	assertProblem(t, do(h, "PUT", path, `{"status":"hired"}`), 422)
	assertProblem(t, do(h, "PUT", path, `{"status":"offer","occurred_at":"2099-01-01T00:00:00Z"}`), 422)
	assertProblem(t, do(h, "PUT", "/api/v1/applications/999", `{"status":"saved"}`), 404)
	assertProblem(t, do(h, "PUT", path, `{"status":"saved","profile_id":77,"force":true}`), 422) // no such profile

	if rec = do(h, "GET", "/api/v1/applications?status=applied", ""); !strings.Contains(rec.Body.String(), `"title":"Senior Data Engineer"`) {
		t.Errorf("list: %s", rec.Body)
	}
	if rec = do(h, "GET", "/api/v1/applications?status=offer", ""); rec.Body.String() != "{\"applications\":[]}\n" {
		t.Errorf("empty filter: %s", rec.Body)
	}
	assertProblem(t, do(h, "GET", "/api/v1/applications?status=discovered", ""), 422)
	if rec = do(h, "GET", path+"/events", ""); !strings.Contains(rec.Body.String(), `"note":"sent CV"`) {
		t.Errorf("events: %s", rec.Body)
	}

	rec = do(h, "GET", "/api/v1/labels/export", "")
	if rec.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Errorf("content type %q", rec.Header().Get("Content-Type"))
	}
	var line struct {
		JobID      string         `json:"job_id"`
		Outcome    string         `json:"outcome"`
		OccurredAt time.Time      `json:"occurred_at"`
		Job        map[string]any `json:"job"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &line); err != nil {
		t.Fatalf("export: %v: %s", err, rec.Body)
	}
	if line.JobID != itoa(job) || line.Outcome != "applied" || line.Job["description_text"] != "Python and Kafka" {
		t.Errorf("label: %+v", line)
	}
}
