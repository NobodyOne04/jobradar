// Package remoteok reads the Remote OK feed (ADR 0013).
//
// API: GET https://remoteok.com/api[?tag=], one JSON array of recent jobs (about 100), no
// paging, no key (https://remoteok.com/api and its FAQ, checked 2026-11-14). Element 0 is the
// legal notice, not a job.
//
// A Remote OK source is a saved feed filter: board_token is a `tag` (e.g. "data"), or "all".
//
// Terms (the notice in element 0, checked 2026-11-14):
//   - Link back to the job's Remote OK URL, as a followed link (no rel=nofollow), and name
//     Remote OK as the source: the API shows "via Remote OK" next to that URL.
//   - Do not use the Remote OK logo; the name is fine.
//
// It is a feed (ingest.FeedAdapter): postings close only after 30 days unseen.
//
// Notes on the data: salary_min/max are USD a year when set, but some jobs carry nonsense
// (21, or 114000000); a salary outside 1,000-2,000,000 is left unknown rather than stored.
package remoteok

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// FeedURL is the API endpoint; tests replace it.
const FeedURL = "https://remoteok.com/api"

// Adapter implements ingest.Adapter for Remote OK.
type Adapter struct {
	Client  *ingest.HTTPClient
	FeedURL string
}

// New returns an adapter for the real API, with its own client at one request every 2s.
func New() *Adapter { return &Adapter{Client: ingest.NewHTTPClient(0.5), FeedURL: FeedURL} }

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "remoteok" }

// StaleAfter makes this a feed: see ingest.FeedAdapter.
func (*Adapter) StaleAfter() time.Duration { return ingest.FeedStaleAfter }

// ValidateConfig accepts no filters besides the board_token tag.
func (*Adapter) ValidateConfig(config json.RawMessage) error {
	_, err := ingest.FeedParams("all", "tag", config)
	return err
}

type job struct {
	ID          json.Number `json:"id"` // a string in the feed; Number accepts both
	URL         string      `json:"url"`
	Position    string      `json:"position"`
	Company     string      `json:"company"`
	Description string      `json:"description"`
	Location    string      `json:"location"`
	Date        string      `json:"date"`
	SalaryMin   float64     `json:"salary_min"`
	SalaryMax   float64     `json:"salary_max"`
	Legal       string      `json:"legal"`
}

// Fetch reads the feed once.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	params, err := ingest.FeedParams(src.BoardToken, "tag", src.Config)
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
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode feed: %w", err)
	}
	var out []ingest.RawPosting
	seen := map[string]bool{}
	for _, raw := range items {
		var j job
		if err := json.Unmarshal(raw, &j); err != nil {
			return nil, fmt.Errorf("decode job: %w", err)
		}
		if j.Legal != "" || j.ID == "" { // the legal notice
			continue
		}
		if seen[j.ID.String()] {
			continue
		}
		seen[j.ID.String()] = true
		out = append(out, ingest.RawPosting{ExternalID: j.ID.String(), URL: j.URL, Payload: raw})
	}
	return out, nil
}

// Normalize maps one Remote OK job to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var j job
	if err := json.Unmarshal(raw.Payload, &j); err != nil {
		return ingest.NormalizedJob{}, err
	}
	n := ingest.NormalizedJob{
		Title:           j.Position,
		CompanyName:     j.Company,
		DescriptionHTML: j.Description,
		LocationText:    "Remote",
		RemotePolicy:    "remote", // a remote-only board
		EmploymentType:  "unknown",
	}
	if j.Location != "" {
		n.LocationText = "Remote: " + j.Location
	}
	if t, err := time.Parse(time.RFC3339, j.Date); err == nil {
		t = t.UTC()
		n.PostedAt = &t
	}
	if plausible(j.SalaryMin) && plausible(j.SalaryMax) && j.SalaryMin <= j.SalaryMax {
		lo, hi, usd, year := j.SalaryMin, j.SalaryMax, "USD", "year"
		n.SalaryMin, n.SalaryMax, n.SalaryCurrency, n.SalaryPeriod = &lo, &hi, &usd, &year
	}
	return n, nil
}

// plausible bounds a yearly USD salary; outside it the feed's value is noise (package doc).
func plausible(v float64) bool { return v >= 1_000 && v <= 2_000_000 }
