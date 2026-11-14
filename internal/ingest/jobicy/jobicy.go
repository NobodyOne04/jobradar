// Package jobicy reads the Jobicy remote-jobs feed (ADR 0013).
//
// API: GET https://jobicy.com/api/jobs?count=100[&tag=&geo=&industry=][&cursor=], newest
// first, cursor pages over a 7-day window that lags 3 hours (docs:
// https://jobicy.com/jobs-rss-feed, checked 2026-11-14). No key needed.
//
// A Jobicy source is a saved feed filter: board_token is `tag` (a keyword matched against
// titles), or "all"; config may set `geo` and `industry` slugs.
//
// Terms (the docs and every response's friendlyNotice, checked 2026-11-14):
//   - Credit Jobicy with a direct link to the source: the API shows "via Jobicy" next to the
//     job's Jobicy URL, which is the posting URL here.
//   - Application buttons must go to that URL.
//   - Poll at most once an hour (docs/sources.json): MinInterval is one hour.
//
// It is a feed (ingest.FeedAdapter): jobs older than 7 days drop out while still open, so
// postings close only after 30 days unseen.
package jobicy

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

const (
	// FeedURL is the jobs endpoint; tests replace it.
	FeedURL = "https://jobicy.com/api/jobs"
	// MaxPages bounds one sync at 2,000 jobs; a feed needs no more (it is a window anyway).
	MaxPages = 20
)

// Adapter implements ingest.Adapter for Jobicy.
type Adapter struct {
	Client  *ingest.HTTPClient
	FeedURL string
}

// New returns an adapter for the real API, with its own client at one request every 2s.
func New() *Adapter { return &Adapter{Client: ingest.NewHTTPClient(0.5), FeedURL: FeedURL} }

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "jobicy" }

// StaleAfter makes this a feed: see ingest.FeedAdapter.
func (*Adapter) StaleAfter() time.Duration { return ingest.FeedStaleAfter }

// MinInterval is Jobicy's "at most once an hour".
func (*Adapter) MinInterval() time.Duration { return time.Hour }

// ValidateConfig accepts only the geo and industry filters.
func (*Adapter) ValidateConfig(config json.RawMessage) error {
	_, err := ingest.FeedParams("all", "tag", config, "geo", "industry")
	return err
}

type job struct {
	ID             int64    `json:"id"`
	URL            string   `json:"url"`
	JobTitle       string   `json:"jobTitle"`
	CompanyName    string   `json:"companyName"`
	JobType        []string `json:"jobType"`
	JobGeo         string   `json:"jobGeo"`
	JobDescription string   `json:"jobDescription"`
	PubDate        string   `json:"pubDate"`
	SalaryMin      *float64 `json:"salaryMin"`
	SalaryMax      *float64 `json:"salaryMax"`
	SalaryCurrency string   `json:"salaryCurrency"`
	SalaryPeriod   string   `json:"salaryPeriod"`
}

// Fetch reads every page of the feed for the source's filters.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	params, err := ingest.FeedParams(src.BoardToken, "tag", src.Config, "geo", "industry")
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	params.Set("count", "100")
	var out []ingest.RawPosting
	seen := map[int64]bool{}
	for range MaxPages {
		body, err := a.Client.GetBody(ctx, a.FeedURL+"?"+params.Encode())
		if err != nil {
			return nil, err
		}
		var resp struct {
			Jobs       []json.RawMessage `json:"jobs"`
			NextCursor *string           `json:"nextCursor"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("decode page: %w", err)
		}
		for _, raw := range resp.Jobs {
			var j job
			if err := json.Unmarshal(raw, &j); err != nil || j.ID == 0 {
				return nil, fmt.Errorf("decode job: id missing or invalid (%v)", err)
			}
			if seen[j.ID] {
				continue
			}
			seen[j.ID] = true
			out = append(out, ingest.RawPosting{ExternalID: strconv.FormatInt(j.ID, 10), URL: j.URL, Payload: raw})
		}
		if resp.NextCursor == nil || *resp.NextCursor == "" {
			break
		}
		params.Set("cursor", *resp.NextCursor)
	}
	return out, nil
}

var (
	employment = map[string]string{
		"full-time": "full_time", "part-time": "part_time", "contract": "contract", "internship": "internship",
	}
	periods = map[string]string{"yearly": "year", "monthly": "month", "daily": "day", "hourly": "hour"}
)

// Normalize maps one Jobicy job to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var j job
	if err := json.Unmarshal(raw.Payload, &j); err != nil {
		return ingest.NormalizedJob{}, err
	}
	n := ingest.NormalizedJob{
		Title:           j.JobTitle,
		CompanyName:     j.CompanyName,
		DescriptionHTML: j.JobDescription,
		LocationText:    "Remote",
		RemotePolicy:    "remote", // a remote-only board
		EmploymentType:  "unknown",
	}
	if geo := strings.Join(strings.Fields(j.JobGeo), " "); geo != "" {
		n.LocationText = "Remote: " + geo // "Canada,  USA" -> "Remote: Canada, USA"
	}
	if len(j.JobType) == 1 { // several types: no single answer
		if v, ok := employment[strings.ToLower(j.JobType[0])]; ok {
			n.EmploymentType = v
		}
	}
	if t, err := time.Parse(time.RFC3339, j.PubDate); err == nil {
		t = t.UTC()
		n.PostedAt = &t
	}
	if j.SalaryMin != nil || j.SalaryMax != nil {
		n.SalaryMin, n.SalaryMax = j.SalaryMin, j.SalaryMax
		if len(j.SalaryCurrency) == 3 {
			c := strings.ToUpper(j.SalaryCurrency)
			n.SalaryCurrency = &c
		}
		if p, ok := periods[j.SalaryPeriod]; ok {
			n.SalaryPeriod = &p
		}
	}
	return n, nil
}
