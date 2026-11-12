package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

// UserAgent identifies us honestly to sources (ADR 0005), without personal details.
const UserAgent = "jobradar/0.1 (personal job search tool)"

// maxBody bounds a source response; a full board is a few MB at most.
const maxBody = 64 << 20

// HTTPClient is the polite client every adapter uses: a rate limit, a timeout per request,
// and retries with backoff on 429 and 5xx that honour Retry-After.
type HTTPClient struct {
	HTTP     *http.Client
	Limiter  *rate.Limiter
	Attempts int           // total tries, including the first
	BaseWait time.Duration // first backoff; doubles each retry, plus jitter
	MaxWait  time.Duration // cap on any single wait, including Retry-After
}

// NewHTTPClient returns a client allowing perSecond requests, with 4 attempts.
func NewHTTPClient(perSecond float64) *HTTPClient {
	return &HTTPClient{
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		Limiter:  rate.NewLimiter(rate.Limit(perSecond), 1),
		Attempts: 4,
		BaseWait: time.Second,
		MaxWait:  2 * time.Minute,
	}
}

// StatusError is a non-2xx response that was not retried or ran out of retries.
type StatusError struct {
	URL    string
	Status int
}

func (e *StatusError) Error() string { return fmt.Sprintf("GET %s: HTTP %d", e.URL, e.Status) }

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// GetBody fetches url and returns its body, retrying transient failures.
func (c *HTTPClient) GetBody(ctx context.Context, url string) ([]byte, error) {
	var lastErr error
	for attempt := range c.Attempts {
		if attempt > 0 {
			wait := c.backoff(attempt, lastErr)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		if err := c.Limiter.Wait(ctx); err != nil {
			return nil, err
		}
		body, err := c.once(ctx, url)
		if err == nil {
			return body, nil
		}
		lastErr = err
		var se *StatusError
		if errors.As(err, &se) && !retryable(se.Status) {
			return nil, err // 4xx other than 429: retrying will not help
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", c.Attempts, lastErr)
}

// retryAfterError carries the server's Retry-After so backoff can honour it.
type retryAfterError struct {
	*StatusError
	after time.Duration
}

func (e *retryAfterError) Unwrap() error { return e.StatusError }

func (c *HTTPClient) once(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16)) // let the connection be reused
		se := &StatusError{URL: url, Status: resp.StatusCode}
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs >= 0 {
			return nil, &retryAfterError{se, time.Duration(secs) * time.Second}
		}
		return nil, se
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("GET %s: response over %d bytes", url, maxBody)
	}
	return body, nil
}

func (c *HTTPClient) backoff(attempt int, lastErr error) time.Duration {
	var ra *retryAfterError
	if errors.As(lastErr, &ra) {
		return min(ra.after, c.MaxWait)
	}
	wait := c.BaseWait << (attempt - 1)
	jitter := time.Duration(rand.Int64N(int64(wait)/2 + 1))
	return min(wait+jitter, c.MaxWait)
}
