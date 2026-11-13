// Package matcher is the backend's client for the matcher service (ADR 0004): POST /v1/match
// in batches, GET /v1/version for the startup compatibility check (ADR 0001). The contract is
// contracts/matcher.openapi.yaml, pinned with the matcher release.
package matcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxBatch is the matcher's limit of jobs per request.
const MaxBatch = 50

// Job is one job as the matcher reads it (its MatchJob schema).
type Job struct {
	JobID           string   `json:"job_id"`
	Title           string   `json:"title"`
	CompanyName     string   `json:"company_name,omitempty"`
	DescriptionText string   `json:"description_text,omitempty"`
	LocationText    string   `json:"location_text,omitempty"`
	RemotePolicy    string   `json:"remote_policy,omitempty"`
	Countries       []string `json:"countries,omitempty"`
	EmploymentType  string   `json:"employment_type,omitempty"`
	SalaryMin       *float64 `json:"salary_min,omitempty"`
	SalaryMax       *float64 `json:"salary_max,omitempty"`
	SalaryCurrency  *string  `json:"salary_currency,omitempty"`
	SalaryPeriod    *string  `json:"salary_period,omitempty"`
	ContentHash     string   `json:"content_hash,omitempty"`
}

// MatchRequest asks for one model version's scores of up to MaxBatch jobs against a profile.
type MatchRequest struct {
	ModelVersion string          `json:"model_version"`
	Profile      json.RawMessage `json:"profile"` // a stored profile revision, schema v1
	Jobs         []Job           `json:"jobs"`
}

// Result is one job's outcome: scored, or an item-level error (Error set). Raw keeps the
// matcher's full JSON for the job (dimensions, skills, explanations) for storage.
type Result struct {
	JobID           string  `json:"job_id"`
	Score           int     `json:"score"`
	Band            string  `json:"band"`
	Confidence      string  `json:"confidence"`
	Completeness    float64 `json:"completeness"`
	HardConstraints string  `json:"hard_constraints"`
	Error           string  `json:"error"`
	Raw             json.RawMessage
}

// MatchResponse is the matcher's answer to one batch.
type MatchResponse struct {
	ModelVersion   string
	MatcherVersion string
	Results        []Result
}

// VersionInfo is GET /v1/version.
type VersionInfo struct {
	MatcherVersion string   `json:"matcher_version"`
	APIVersions    []string `json:"api_versions"`
}

// APIError is a problem+json answer from the matcher.
type APIError struct {
	Status int
	Title  string
	Detail string
}

func (e *APIError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("matcher: HTTP %d %s: %s", e.Status, e.Title, e.Detail)
	}
	return fmt.Sprintf("matcher: HTTP %d %s", e.Status, e.Title)
}

// Retryable reports whether trying the same request later may succeed.
func (e *APIError) Retryable() bool {
	return e.Status == http.StatusServiceUnavailable || e.Status >= 500
}

// Client talks to one matcher.
type Client struct {
	BaseURL  string // e.g. http://matcher:8000
	Token    string // MATCHER_TOKEN
	HTTP     *http.Client
	Attempts int           // total tries per batch, including the first
	BaseWait time.Duration // first backoff; doubles, plus jitter
}

// New returns a client with 3 attempts and 2 minutes per batch. ADR 0004 said 30 s, but a
// batch of 50 new, long postings embeds about a thousand sentences on one CPU thread and took
// 32 s in the first full-system run; a timeout there wastes the work and retries it.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		Token:    token,
		HTTP:     &http.Client{Timeout: 2 * time.Minute},
		Attempts: 3,
		BaseWait: time.Second,
	}
}

// Version asks the matcher which release it runs.
func (c *Client) Version(ctx context.Context, requestID string) (VersionInfo, error) {
	var v VersionInfo
	body, err := c.do(ctx, http.MethodGet, "/v1/version", requestID, nil)
	if err != nil {
		return v, err
	}
	return v, json.Unmarshal(body, &v)
}

// Match scores one batch. Connection errors and 5xx are retried with backoff; 4xx are not.
func (c *Client) Match(ctx context.Context, requestID string, req MatchRequest) (MatchResponse, error) {
	if len(req.Jobs) == 0 || len(req.Jobs) > MaxBatch {
		return MatchResponse{}, fmt.Errorf("matcher: batch of %d jobs, want 1-%d", len(req.Jobs), MaxBatch)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return MatchResponse{}, err
	}
	var lastErr error
	for attempt := range c.Attempts {
		if attempt > 0 {
			wait := c.BaseWait << (attempt - 1)
			wait += time.Duration(rand.Int64N(int64(wait)/2 + 1))
			select {
			case <-ctx.Done():
				return MatchResponse{}, ctx.Err()
			case <-time.After(wait):
			}
		}
		body, err := c.do(ctx, http.MethodPost, "/v1/match", requestID, payload)
		if err == nil {
			return decodeMatch(body)
		}
		lastErr = err
		var apiErr *APIError
		if errors.As(err, &apiErr) && !apiErr.Retryable() {
			return MatchResponse{}, err
		}
		if ctx.Err() != nil {
			return MatchResponse{}, ctx.Err()
		}
	}
	return MatchResponse{}, fmt.Errorf("after %d attempts: %w", c.Attempts, lastErr)
}

func decodeMatch(body []byte) (MatchResponse, error) {
	var wire struct {
		ModelVersion   string            `json:"model_version"`
		MatcherVersion string            `json:"matcher_version"`
		Results        []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return MatchResponse{}, fmt.Errorf("matcher: decode response: %w", err)
	}
	out := MatchResponse{ModelVersion: wire.ModelVersion, MatcherVersion: wire.MatcherVersion}
	for _, raw := range wire.Results {
		r := Result{Raw: raw}
		if err := json.Unmarshal(raw, &r); err != nil {
			return MatchResponse{}, fmt.Errorf("matcher: decode result: %w", err)
		}
		out.Results = append(out.Results, r)
	}
	return out, nil
}

func (c *Client) do(ctx context.Context, method, path, requestID string, payload []byte) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		var p struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(data, &p)
		if p.Title == "" {
			p.Title = http.StatusText(resp.StatusCode)
		}
		return nil, &APIError{Status: resp.StatusCode, Title: p.Title, Detail: p.Detail}
	}
	return data, nil
}

// CheckVersion compares the running matcher with the pinned one (contracts/MATCHER_VERSION).
// A different major version is an error: the contract may differ. A different minor or patch
// is fine but worth a warning, returned as warn.
func CheckVersion(running, pinned string) (warn string, err error) {
	rMajor, rOK := major(running)
	pMajor, pOK := major(pinned)
	if !rOK || !pOK {
		return "", fmt.Errorf("unparseable matcher versions %q (running) and %q (pinned)", running, pinned)
	}
	if rMajor != pMajor {
		return "", fmt.Errorf("matcher %s runs, %s is pinned: major versions differ", running, pinned)
	}
	if running != pinned {
		return fmt.Sprintf("matcher %s runs, %s is pinned", running, pinned), nil
	}
	return "", nil
}

func major(v string) (int, bool) {
	n, err := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	return n, err == nil
}

// Status is the matcher's state for /readyz and the matching loop: "ok", "unreachable" or
// "incompatible" (a different major version than pinned: matching must not run). detail
// explains anything other than a plain "ok"; it carries no secrets.
func (c *Client) Status(ctx context.Context, pinned string) (status, detail string) {
	v, err := c.Version(ctx, "")
	if err != nil {
		return "unreachable", err.Error()
	}
	warn, err := CheckVersion(v.MatcherVersion, pinned)
	if err != nil {
		return "incompatible", err.Error()
	}
	return "ok", warn
}
