// Package remotive reads the Remotive public feed (ADR 0013).
//
// API: GET https://remotive.com/api/remote-jobs[?search=&category=], one JSON object with a
// `jobs` array; the public feed is small (about 16 jobs) and 24 hours late. No key (docs:
// https://github.com/remotive-com/remote-jobs-api, and the legal notice in every response,
// checked 2026-11-14).
//
// A Remotive source is a saved feed filter: board_token is `search`, or "all"; config may set
// `category` (a Remotive category slug).
//
// Terms (the response's 0-legal-notice, checked 2026-11-14):
//   - Link back to the job's Remotive URL and name Remotive as the source: the API shows
//     "via Remotive" next to that URL.
//   - Never submit Remotive jobs to third-party sites (Jooble, Google Jobs, LinkedIn...).
//   - Do not use the jobs to collect signups or email addresses.
//   - At most 4 requests a day; more gets blocked. MinInterval is 6 hours, enforced for
//     scheduled and manual syncs alike.
//
// It is a feed (ingest.FeedAdapter): postings close only after 30 days unseen.
//
// Notes on the data: salary is free text ("$35,3k- $52k"), so it stays unknown (ADR 0005);
// publication_date has no zone and is read as UTC.
package remotive

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// FeedURL is the API endpoint; tests replace it.
const FeedURL = "https://remotive.com/api/remote-jobs"

// Adapter implements ingest.Adapter for Remotive.
type Adapter struct {
	Client  *ingest.HTTPClient
	FeedURL string
}

// New returns an adapter for the real API. One request per sync, so the client's rate
// matters little; the terms' daily limit is MinInterval's job.
func New() *Adapter {
	c := ingest.NewHTTPClient(0.1)
	c.Attempts = 2 // retries count against the 4-a-day limit too
	return &Adapter{Client: c, FeedURL: FeedURL}
}

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "remotive" }

// StaleAfter makes this a feed: see ingest.FeedAdapter.
func (*Adapter) StaleAfter() time.Duration { return ingest.FeedStaleAfter }

// MinInterval keeps a source within Remotive's 4 requests a day.
func (*Adapter) MinInterval() time.Duration { return 6 * time.Hour }

// ValidateConfig accepts only the category filter.
func (*Adapter) ValidateConfig(config json.RawMessage) error {
	_, err := ingest.FeedParams("all", "search", config, "category")
	return err
}

type job struct {
	ID              int64  `json:"id"`
	URL             string `json:"url"`
	Title           string `json:"title"`
	CompanyName     string `json:"company_name"`
	JobType         string `json:"job_type"`
	PublicationDate string `json:"publication_date"`
	Location        string `json:"candidate_required_location"`
	Description     string `json:"description"`
}

// Fetch reads the feed once.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	params, err := ingest.FeedParams(src.BoardToken, "search", src.Config, "category")
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	u := a.FeedURL
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	body, err := a.Client.GetBody(ctx, u)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Jobs []json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode feed: %w", err)
	}
	out := make([]ingest.RawPosting, 0, len(resp.Jobs))
	for _, raw := range resp.Jobs {
		var j job
		if err := json.Unmarshal(raw, &j); err != nil || j.ID == 0 {
			return nil, fmt.Errorf("decode job: id missing or invalid (%v)", err)
		}
		out = append(out, ingest.RawPosting{ExternalID: strconv.FormatInt(j.ID, 10), URL: j.URL, Payload: raw})
	}
	return out, nil
}

var employment = map[string]string{
	"full_time": "full_time", "part_time": "part_time", "contract": "contract",
	"freelance": "contract", "internship": "internship",
}

// Normalize maps one Remotive job to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var j job
	if err := json.Unmarshal(raw.Payload, &j); err != nil {
		return ingest.NormalizedJob{}, err
	}
	n := ingest.NormalizedJob{
		Title:           j.Title,
		CompanyName:     j.CompanyName,
		DescriptionHTML: j.Description,
		LocationText:    "Remote",
		RemotePolicy:    "remote", // a remote-only board
		EmploymentType:  "unknown",
	}
	if j.Location != "" {
		n.LocationText = "Remote: " + j.Location
	}
	if v, ok := employment[j.JobType]; ok {
		n.EmploymentType = v
	}
	if t, err := time.Parse("2006-01-02T15:04:05", j.PublicationDate); err == nil {
		n.PostedAt = &t // no zone in the feed: read as UTC
	}
	return n, nil
}
