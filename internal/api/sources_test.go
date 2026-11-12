package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/ingest/greenhouse"
	"github.com/NobodyOne04/jobradar/internal/store"
	"github.com/NobodyOne04/jobradar/internal/testdb"
)

// sourcesAPI wires the real handler to a migrated Postgres and a fake Greenhouse server
// that serves the adapter's recorded fixture.
func sourcesAPI(t *testing.T) (http.Handler, *Sources) {
	t.Helper()
	st := testdb.Migrated(t)
	fixture, err := os.ReadFile("../ingest/greenhouse/testdata/jobs.json")
	if err != nil {
		t.Fatal(err)
	}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(gh.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	adapter := &greenhouse.Adapter{Client: ingest.NewHTTPClient(1000), BaseURL: gh.URL + "/"}
	sched := &ingest.Scheduler{
		Syncer:      &ingest.Syncer{Pool: st.Pool, Adapters: ingest.NewRegistry(adapter), Log: log},
		Concurrency: 1, Log: log,
	}
	s := &Sources{Queries: store.New(st.Pool), Scheduler: sched, Log: log, Background: context.Background()}
	return NewHandler(log, readyOK, 5*time.Second, s.Register), s
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreateSourceValidation(t *testing.T) {
	h, _ := sourcesAPI(t)
	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"unsupported kind": {`{"kind":"monster","board_token":"x"}`, 422},
		"not yet built":    {`{"kind":"lever","board_token":"x"}`, 422},
		"empty token":      {`{"kind":"greenhouse","board_token":"  "}`, 422},
		"control chars":    {`{"kind":"greenhouse","board_token":"a\nb"}`, 422},
		"long token":       {`{"kind":"greenhouse","board_token":"` + strings.Repeat("a", 201) + `"}`, 422},
		"config not obj":   {`{"kind":"greenhouse","board_token":"x","config":[1]}`, 422},
		"unknown field":    {`{"kind":"greenhouse","board_token":"x","admin":true}`, 400},
		"two objects":      {`{"kind":"greenhouse","board_token":"x"}{}`, 400},
		"not json":         {`kind=greenhouse`, 400},
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(h, "POST", "/api/v1/sources", tc.body)
			assertProblem(t, rec, tc.status)
		})
	}
}

func TestSourceLifecycle(t *testing.T) {
	h, s := sourcesAPI(t)

	rec := do(h, "POST", "/api/v1/sources", `{"kind":"greenhouse","board_token":" gitlab ","display_name":"GitLab"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var src store.Source
	_ = json.Unmarshal(rec.Body.Bytes(), &src)
	if src.BoardToken != "gitlab" || !src.Enabled || string(src.Config) != "{}" {
		t.Errorf("created: %+v", src)
	}
	assertProblem(t, do(h, "POST", "/api/v1/sources", `{"kind":"greenhouse","board_token":"gitlab"}`), 409)

	rec = do(h, "PATCH", "/api/v1/sources/"+itoa(src.ID), `{"enabled":false}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":false`) ||
		!strings.Contains(rec.Body.String(), `"display_name":"GitLab"`) {
		t.Errorf("patch: %d %s", rec.Code, rec.Body)
	}
	assertProblem(t, do(h, "PATCH", "/api/v1/sources/999", `{"enabled":true}`), 404)
	assertProblem(t, do(h, "PATCH", "/api/v1/sources/abc", `{}`), 404)

	if rec = do(h, "POST", "/api/v1/sources/"+itoa(src.ID)+"/sync", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("sync: %d %s", rec.Code, rec.Body)
	}
	s.Wait()
	rec = do(h, "GET", "/api/v1/sources/"+itoa(src.ID)+"/runs", "")
	var runs struct{ Runs []store.SyncRun }
	_ = json.Unmarshal(rec.Body.Bytes(), &runs)
	if len(runs.Runs) != 1 || runs.Runs[0].Status != "succeeded" || runs.Runs[0].Created != 3 {
		t.Errorf("runs: %s", rec.Body)
	}
	assertProblem(t, do(h, "GET", "/api/v1/sources/"+itoa(src.ID)+"/runs?limit=0", ""), 422)

	rec = do(h, "GET", "/api/v1/sources", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"board_token":"gitlab"`) {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
