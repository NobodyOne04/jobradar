// Package devglobaljobs reads the Dev Global Jobs API (ADR 0013).
//
// API (docs: https://devglobaljobs.com/developers, checked 2026-11-14), no key, 120 requests
// a minute per IP:
//   - GET /api/v1/jobs?limit=100&offset=N[&search=&category=&country=]: newest first, with a
//     short descriptionExcerpt only.
//   - GET /api/v1/jobs/{id}: one job with its full description.
//
// A Dev Global Jobs source is a saved feed filter: board_token is `search`, or "all"; config
// may set `category` and `country`. It is a global board, not a remote-only one.
//
// Terms (the docs and every response's meta.license, checked 2026-11-14):
//   - Wherever the jobs are shown, a visible link "Powered by Dev Global Jobs" to
//     https://devglobaljobs.com (jobradar-web's footer), and each job keeps its link to its
//     Dev Global Jobs page (the posting URL here; the API shows "via Dev Global Jobs").
//   - Free for internal, non-commercial use within the rate limit; no redistribution.
//
// It is a feed (ingest.FeedAdapter): it reads the newest MaxJobs, and postings close only
// after 30 days unseen, or at the job's closingDate (expires_at), whichever comes first.
//
// Notes on the data: some descriptions arrive HTML-escaped once (&lt;p&gt;, about 1 in 6 on
// 2026-11-14) and are unescaped here, as Greenhouse's are. Salary is free text, so it stays unknown (ADR 0005). country is an
// ISO code, "Remote", or null; only "Remote" makes remote_policy remote.
package devglobaljobs

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

const (
	// BaseURL is the API root; tests replace it.
	BaseURL = "https://devglobaljobs.com/api/v1/jobs"
	// MaxJobs bounds one sync: 2 list requests plus one detail request per job.
	MaxJobs  = 200
	pageSize = 100
)

// Adapter implements ingest.Adapter for Dev Global Jobs.
type Adapter struct {
	Client  *ingest.HTTPClient
	BaseURL string
	Now     func() time.Time // for expiry checks; tests fix it
}

// New returns an adapter for the real API at one request a second (half its limit).
func New() *Adapter {
	return &Adapter{Client: ingest.NewHTTPClient(1), BaseURL: BaseURL, Now: time.Now}
}

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "devglobaljobs" }

// StaleAfter makes this a feed: see ingest.FeedAdapter.
func (*Adapter) StaleAfter() time.Duration { return ingest.FeedStaleAfter }

// ValidateConfig accepts only the category and country filters.
func (*Adapter) ValidateConfig(config json.RawMessage) error {
	_, err := ingest.FeedParams("all", "search", config, "category", "country")
	return err
}

type job struct {
	ID           int64   `json:"id"`
	URL          string  `json:"url"`
	Title        string  `json:"title"`
	Organization string  `json:"organization"`
	Location     *string `json:"location"`
	Country      *string `json:"country"`
	JobType      *string `json:"jobType"`
	Description  string  `json:"description"`
	PostedAt     string  `json:"postedAt"`
	ClosingDate  *string `json:"closingDate"`
}

// Fetch lists the newest MaxJobs jobs, then reads each one in full.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	params, err := ingest.FeedParams(src.BoardToken, "search", src.Config, "category", "country")
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	params.Set("limit", strconv.Itoa(pageSize))
	now := a.Now()
	var ids []int64
	seen := map[int64]bool{}
	for offset := 0; offset < MaxJobs; offset += pageSize {
		params.Set("offset", strconv.Itoa(offset))
		body, err := a.Client.GetBody(ctx, a.BaseURL+"?"+params.Encode())
		if err != nil {
			return nil, err
		}
		var resp struct {
			Jobs []job `json:"jobs"`
			Meta struct {
				HasMore bool `json:"hasMore"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("decode list: %w", err)
		}
		for _, j := range resp.Jobs {
			if j.ID != 0 && !seen[j.ID] && !expired(j, now) {
				seen[j.ID] = true
				ids = append(ids, j.ID)
			}
		}
		if !resp.Meta.HasMore {
			break
		}
	}
	out := make([]ingest.RawPosting, 0, len(ids))
	for _, id := range ids {
		raw, err := a.Client.GetBody(ctx, a.BaseURL+"/"+strconv.FormatInt(id, 10))
		if err != nil {
			return nil, fmt.Errorf("job %d: %w", id, err)
		}
		var j job
		if err := json.Unmarshal(raw, &j); err != nil || j.ID != id {
			return nil, fmt.Errorf("decode job %d: id missing or different (%v)", id, err)
		}
		out = append(out, ingest.RawPosting{
			ExternalID: strconv.FormatInt(id, 10), URL: j.URL, Payload: raw, ExpiresAt: closing(j),
		})
	}
	return out, nil
}

func closing(j job) *time.Time {
	if j.ClosingDate == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *j.ClosingDate)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

func expired(j job, now time.Time) bool {
	t := closing(j)
	return t != nil && !t.After(now)
}

var employment = map[string]string{
	"full-time": "full_time", "part-time": "part_time", "contract": "contract", "internship": "internship",
}

// Normalize maps one Dev Global Jobs job (the detail response) to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var j job
	if err := json.Unmarshal(raw.Payload, &j); err != nil {
		return ingest.NormalizedJob{}, err
	}
	n := ingest.NormalizedJob{
		Title:           j.Title,
		CompanyName:     j.Organization,
		DescriptionHTML: description(j.Description),
		RemotePolicy:    "unknown",
		EmploymentType:  "unknown",
	}
	if j.Location != nil {
		n.LocationText = *j.Location
	}
	if j.Country != nil {
		switch c := *j.Country; {
		case c == "Remote":
			n.RemotePolicy = "remote"
		case len(c) == 2:
			n.Countries = []string{strings.ToUpper(c)}
		}
	}
	if j.JobType != nil {
		if v, ok := employment[strings.ToLower(*j.JobType)]; ok {
			n.EmploymentType = v
		}
	}
	if t, err := time.Parse(time.RFC3339, j.PostedAt); err == nil {
		t = t.UTC()
		n.PostedAt = &t
	}
	return n, nil
}

// description unescapes a description that came HTML-escaped, so its tags are stripped like
// any other. Some are an escaped body plus a real-HTML footer ("<p>Find more on ..."), so the
// test is more escaped tags than real ones; unescaping leaves the real ones as they are. A
// normal HTML description (more real tags) is left alone.
func description(s string) string {
	if strings.Count(s, "&lt;") > strings.Count(s, "<") {
		return html.UnescapeString(s)
	}
	return s
}
