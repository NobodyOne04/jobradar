package ingest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func fastClient() *HTTPClient {
	c := NewHTTPClient(1000)
	c.Limiter = rate.NewLimiter(rate.Inf, 1)
	c.BaseWait = time.Millisecond
	c.MaxWait = 50 * time.Millisecond
	return c
}

// serve answers with the given statuses in order, then 200 "ok".
func serve(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != UserAgent {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		n := int(calls.Add(1))
		if n <= len(statuses) {
			if statuses[n-1] == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "0")
			}
			w.WriteHeader(statuses[n-1])
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestRetriesTransientErrors(t *testing.T) {
	srv, calls := serve(t, 503, 429, 500)
	body, err := fastClient().GetBody(context.Background(), srv.URL)
	if err != nil || string(body) != "ok" {
		t.Fatalf("got %q, %v", body, err)
	}
	if calls.Load() != 4 {
		t.Errorf("calls = %d, want 4", calls.Load())
	}
}

func TestGivesUpAfterAttempts(t *testing.T) {
	srv, calls := serve(t, 503, 503, 503, 503, 503)
	_, err := fastClient().GetBody(context.Background(), srv.URL)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 503 {
		t.Fatalf("want StatusError 503, got %v", err)
	}
	if calls.Load() != 4 {
		t.Errorf("calls = %d, want 4", calls.Load())
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	srv, calls := serve(t, 404)
	_, err := fastClient().GetBody(context.Background(), srv.URL)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 404 {
		t.Fatalf("want StatusError 404, got %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestBackoffHonoursRetryAfterWithinCap(t *testing.T) {
	c := fastClient()
	err := &retryAfterError{&StatusError{Status: 429}, time.Hour}
	if got := c.backoff(1, err); got != c.MaxWait {
		t.Errorf("Retry-After of an hour: waited %v, want the cap %v", got, c.MaxWait)
	}
	err.after = 10 * time.Millisecond
	if got := c.backoff(1, err); got != 10*time.Millisecond {
		t.Errorf("Retry-After 10ms: waited %v", got)
	}
}

func TestStopsWhenContextCancelled(t *testing.T) {
	srv, _ := serve(t, 503, 503, 503, 503)
	c := fastClient()
	c.BaseWait = time.Hour
	c.MaxWait = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.GetBody(ctx, srv.URL); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}
