package matching

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// testdata/: responses recorded from MLflow 3.18.0 on 2026-11-13.
func serveFixture(t *testing.T, status int, file string) *Registry {
	t.Helper()
	runID, _ := os.ReadFile("testdata/run_id.txt")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/2.0/mlflow/registered-models/alias":
			if r.URL.Query().Get("name") != ModelName || r.URL.Query().Get("alias") != "champion" {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			body, _ := os.ReadFile("testdata/" + file)
			w.WriteHeader(status)
			_, _ = w.Write(body)
		case r.URL.Path == "/api/2.0/mlflow/runs/get" && r.URL.Query().Get("run_id") == strings.TrimSpace(string(runID)):
			body, _ := os.ReadFile("testdata/run.json")
			_, _ = w.Write(body)
		default:
			t.Errorf("unexpected %s", r.URL)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(srv.Close)
	return NewRegistry(srv.URL)
}

func TestChampion(t *testing.T) {
	c, err := serveFixture(t, 200, "alias_v1.json").Champion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != "1" || c.Tags["gate"] != "passed" || c.Tags["promoted_reason"] != "first model" ||
		c.Metrics["ndcg_at_10"] != 0.81 || c.MatcherVersion != "0.2.1" {
		t.Errorf("%+v", c)
	}
}

func TestNoChampion(t *testing.T) {
	for file, status := range map[string]int{"alias_no_model.json": 404, "alias_no_alias.json": 400} {
		if _, err := serveFixture(t, status, file).Champion(context.Background()); !errors.Is(err, ErrNoChampion) {
			t.Errorf("%s: got %v", file, err)
		}
	}
}

func TestMLflowDown(t *testing.T) {
	_, err := serveFixture(t, 500, "alias_no_model.json").Champion(context.Background())
	if err == nil || errors.Is(err, ErrNoChampion) {
		t.Errorf("a 500 must be an outage, not 'no champion': %v", err)
	}
	if _, err := NewRegistry("http://127.0.0.1:1").Champion(context.Background()); err == nil {
		t.Error("unreachable MLflow gave no error")
	}
}
