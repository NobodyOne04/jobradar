package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func handler(ready Readiness) (http.Handler, *bytes.Buffer) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	return NewHandler(log, ready, time.Second, []string{"example.com"}), &logs // httptest's Host
}

func get(h http.Handler, path string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func readyOK(context.Context) error { return nil }

func TestHealthz(t *testing.T) {
	h, _ := handler(readyOK)
	rec := get(h, "/healthz", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestReadyz(t *testing.T) {
	h, _ := handler(readyOK)
	if rec := get(h, "/readyz", nil); rec.Code != http.StatusOK {
		t.Fatalf("ready: got %d", rec.Code)
	}

	h, _ = handler(func(context.Context) error { return errors.New("database unreachable") })
	rec := get(h, "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("not ready: got %d", rec.Code)
	}
	assertProblem(t, rec, http.StatusServiceUnavailable)
}

func TestUnknownRouteIsProblemJSON(t *testing.T) {
	h, _ := handler(readyOK)
	assertProblem(t, get(h, "/nope", nil), http.StatusNotFound)
}

func TestRequestID(t *testing.T) {
	h, logs := handler(readyOK)
	rec := get(h, "/healthz", http.Header{"X-Request-Id": {"abc-123"}})
	if got := rec.Header().Get("X-Request-ID"); got != "abc-123" {
		t.Errorf("echoed id: got %q", got)
	}
	if !strings.Contains(logs.String(), `"request_id":"abc-123"`) {
		t.Errorf("log lacks request_id: %s", logs)
	}

	// An unsafe ID is replaced by a generated UUIDv7.
	rec = get(h, "/healthz", http.Header{"X-Request-Id": {"bad\tid"}})
	uuid7 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if got := rec.Header().Get("X-Request-ID"); !uuid7.MatchString(got) {
		t.Errorf("generated id is not a UUIDv7: %q", got)
	}
}

func TestPanicBecomesProblem(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	h := withRequestLog(log, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := get(h, "/", nil)
	assertProblem(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "boom") {
		t.Error("panic value leaked to the client")
	}
	if !strings.Contains(logs.String(), "boom") {
		t.Error("panic value missing from the log")
	}
}

func TestRequestDeadline(t *testing.T) {
	var deadline time.Time
	h := withTimeout(time.Second, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		deadline, _ = r.Context().Deadline()
	}))
	get(h, "/", nil)
	if left := time.Until(deadline); left <= 0 || left > time.Second {
		t.Errorf("deadline not set to ~1s: %v", left)
	}
}

func assertProblem(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status: got %d, want %d", rec.Code, status)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content type: got %q", ct)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Status != status || p.Title == "" {
		t.Errorf("bad problem body %q: %v", rec.Body, err)
	}
}

func TestLocalOnly(t *testing.T) {
	h := withLocalOnly([]string{"localhost", "127.0.0.1", "::1"},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for name, tc := range map[string]struct {
		host, origin string
		want         int
	}{
		"curl to localhost":       {"localhost:8080", "", 204},
		"curl to 127.0.0.1":       {"127.0.0.1:8080", "", 204},
		"ipv6 loopback":           {"[::1]:8080", "", 204},
		"same-origin browser":     {"localhost:8080", "http://localhost:8080", 204},
		"DNS rebinding":           {"evil.example:8080", "http://evil.example:8080", 403},
		"rebinding without port":  {"evil.example", "", 403},
		"cross-site form post":    {"localhost:8080", "https://evil.example", 403},
		"opaque origin (sandbox)": {"localhost:8080", "null", 403},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/sources", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("got %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
