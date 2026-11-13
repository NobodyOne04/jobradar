package matcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/NobodyOne04/jobradar/contracts"
)

// The contract test (ADR 0004): every request this client sends, and every response recorded
// from the real matcher 0.2.0 (testdata/, recorded 2026-11-13), must match the pinned
// contracts/matcher.openapi.yaml.

func contract(t *testing.T) routers.Router {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData(contracts.MatcherOpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	doc.Servers = openapi3.Servers{{URL: "/"}} // match any host
	r, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type fake struct {
	t        *testing.T
	router   routers.Router
	statuses []int    // per call; the last repeats
	fixtures []string // per call; the last repeats
	calls    atomic.Int32
	lastID   atomic.Value
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := int(f.calls.Add(1)) - 1
	pick := func(i int) int { return min(i, len(f.statuses)-1) }
	status, fixture := f.statuses[pick(n)], f.fixtures[pick(n)]
	f.lastID.Store(r.Header.Get("X-Request-ID"))

	// The request must match the contract.
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	route, params, err := f.router.FindRoute(r)
	if err != nil {
		f.t.Errorf("no route for %s %s: %v", r.Method, r.URL.Path, err)
		return
	}
	reqIn := &openapi3filter.RequestValidationInput{
		Request: r, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}
	if err := openapi3filter.ValidateRequest(context.Background(), reqIn); err != nil {
		f.t.Errorf("request breaks the contract: %v", err)
	}

	// The recorded response must match the contract too.
	data, err := os.ReadFile("testdata/" + fixture)
	if err != nil {
		f.t.Fatal(err)
	}
	contentType := "application/json"
	if status >= 400 {
		contentType = "application/problem+json"
	}
	h := http.Header{"Content-Type": {contentType}}
	if err := openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: reqIn, Status: status, Header: h, Body: io.NopCloser(bytes.NewReader(data)),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}); err != nil && status < 500 {
		// 5xx (other than documented ones) may come from a proxy, not the matcher.
		f.t.Errorf("fixture %s breaks the contract: %v", fixture, err)
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func client(t *testing.T, f *fake) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New(srv.URL, "test-token-0123456789")
	c.BaseWait = time.Millisecond
	return c
}

func request() MatchRequest {
	profile, _ := os.ReadFile("testdata/profile.json")
	return MatchRequest{
		ModelVersion: "1",
		Profile:      profile,
		Jobs: []Job{
			{JobID: "101", Title: "Senior Data Engineer", DescriptionText: "Python, SQL", RemotePolicy: "remote",
				Countries: []string{"DE"}, EmploymentType: "full_time", ContentHash: "abc"},
			{JobID: "102", Title: "Analyst"},
		},
	}
}

func TestMatchDecodesScoresAndItemErrors(t *testing.T) {
	f := &fake{t: t, router: contract(t), statuses: []int{200}, fixtures: []string{"match_200.json"}}
	resp, err := client(t, f).Match(context.Background(), "req-1", request())
	if err != nil {
		t.Fatal(err)
	}
	if resp.ModelVersion != "1" || resp.MatcherVersion != "0.2.0" || len(resp.Results) != 2 {
		t.Fatalf("response: %+v", resp)
	}
	ok, bad := resp.Results[0], resp.Results[1]
	if ok.JobID != "101" || ok.Score != 55 || ok.Band != "good" || ok.Error != "" ||
		!strings.Contains(string(ok.Raw), `"explanations"`) {
		t.Errorf("scored result: %+v", ok)
	}
	if bad.JobID != "102" || bad.Error != "invalid job" {
		t.Errorf("item error: %+v", bad)
	}
	if f.lastID.Load() != "req-1" {
		t.Errorf("X-Request-ID not forwarded: %v", f.lastID.Load())
	}
}

func TestMatchRetriesUnavailableThenSucceeds(t *testing.T) {
	f := &fake{t: t, router: contract(t), statuses: []int{503, 503, 200},
		fixtures: []string{"match_503_unavailable.json", "match_503_unavailable.json", "match_200.json"}}
	if _, err := client(t, f).Match(context.Background(), "", request()); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", f.calls.Load())
	}
}

func TestMatchDoesNotRetryClientErrors(t *testing.T) {
	for fixture, status := range map[string]int{"match_422_validation.json": 422, "match_401.json": 401} {
		f := &fake{t: t, router: contract(t), statuses: []int{status}, fixtures: []string{fixture}}
		_, err := client(t, f).Match(context.Background(), "", request())
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != status || apiErr.Retryable() {
			t.Errorf("%s: got %v", fixture, err)
		}
		if f.calls.Load() != 1 {
			t.Errorf("%s: %d calls, want 1", fixture, f.calls.Load())
		}
	}
}

func TestMatchGivesUpAfterAttempts(t *testing.T) {
	f := &fake{t: t, router: contract(t), statuses: []int{503}, fixtures: []string{"match_503_unavailable.json"}}
	_, err := client(t, f).Match(context.Background(), "", request())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 503 || f.calls.Load() != 3 {
		t.Errorf("got %v after %d calls", err, f.calls.Load())
	}
}

func TestBatchLimits(t *testing.T) {
	c := New("http://unused", "x")
	req := request()
	req.Jobs = nil
	if _, err := c.Match(context.Background(), "", req); err == nil {
		t.Error("empty batch accepted")
	}
	req.Jobs = make([]Job, MaxBatch+1)
	if _, err := c.Match(context.Background(), "", req); err == nil {
		t.Error("oversized batch accepted")
	}
}

func TestVersion(t *testing.T) {
	f := &fake{t: t, router: contract(t), statuses: []int{200}, fixtures: []string{"version_200.json"}}
	v, err := client(t, f).Version(context.Background(), "")
	if err != nil || v.MatcherVersion != "0.2.0" || v.APIVersions[0] != "v1" {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestCheckVersion(t *testing.T) {
	for _, tc := range []struct {
		running, pinned string
		warn, fail      bool
	}{
		{"0.2.0", "0.2.0", false, false},
		{"0.3.1", "0.2.0", true, false},
		{"1.0.0", "0.2.0", false, true},
		{"x", "0.2.0", false, true},
	} {
		warn, err := CheckVersion(tc.running, tc.pinned)
		if (warn != "") != tc.warn || (err != nil) != tc.fail {
			t.Errorf("%s vs %s: warn %q err %v", tc.running, tc.pinned, warn, err)
		}
	}
	if _, err := CheckVersion(contracts.MatcherVersion, contracts.MatcherVersion); err != nil {
		t.Errorf("pinned version %q is not a usable version: %v", contracts.MatcherVersion, err)
	}
}
