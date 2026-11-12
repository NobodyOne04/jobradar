// Package himalayas reads saved searches on Himalayas, a remote-jobs board.
//
// API: GET https://himalayas.app/jobs/api/search?q=...&page=N, 20 jobs a page, pages until
// an empty one (docs: https://himalayas.app/api, checked 2026-11-13). No key needed; without
// one the rate limits are "lower" and unstated, so this adapter gets its own slow client.
// The data refreshes daily.
//
// A Himalayas source is a saved search (decided 2026-11-13): board_token is the query `q`,
// and config holds the other search filters (see ValidateConfig).
//
// Terms (https://himalayas.app/terms §113, checked 2026-11-13). Personal use needs no
// approval, but:
//   - (a) Apply only through the API's applicationLink: it is the posting URL here.
//   - (b) Show Himalayas as the source, with a visible link: anything displaying these jobs
//     must say "via Himalayas" and link the posting URL.
//   - (c) Do not pass the jobs on to third parties (other boards, feeds, aggregators).
//   - (d) Remove jobs once they expire: expiryDate becomes the posting's expires_at, and the
//     sync closes expired postings every run; already-expired jobs are not fetched.
//
// Notes on the data:
//   - Every job is remote (it is a remote-jobs board), so remote_policy is "remote".
//   - Countries come as names ("United States"), not ISO codes; they go into the location
//     text, as for Ashby. Timezone restrictions become a "UTC-5 to UTC+3" range there too.
//   - pubDate is "when the job was last updated", the closest there is to a posting date.
//   - salaryPeriod weekly/fortnightly has no canonical period: the period stays unknown.
package himalayas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

const (
	// SearchURL is the search endpoint; tests replace it.
	SearchURL = "https://himalayas.app/jobs/api/search"
	// MaxPages bounds one sync at 500 jobs. A broader search is stored up to that and
	// reported as truncated: narrow it with config filters.
	MaxPages = 25
)

// Adapter implements ingest.Adapter for Himalayas.
type Adapter struct {
	Client    *ingest.HTTPClient
	SearchURL string
	Now       func() time.Time // for expiry checks; tests fix it
}

// New returns an adapter for the real API, with its own client at one request every 2s.
func New() *Adapter {
	return &Adapter{Client: ingest.NewHTTPClient(0.5), SearchURL: SearchURL, Now: time.Now}
}

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "himalayas" }

// filters are the search parameters a source's config may set, besides q (the board_token).
// string: any value; bool: true/false.
var filters = map[string]string{
	"country": "string", "seniority": "string", "employment_type": "string",
	"timezone": "string", "company": "string", "sort": "string",
	"worldwide": "bool", "exclude_worldwide": "bool",
}

// ValidateConfig accepts only the documented search filters, with string or bool values.
func (*Adapter) ValidateConfig(config json.RawMessage) error {
	_, err := searchParams("x", config)
	return err
}

func searchParams(query string, config json.RawMessage) (url.Values, error) {
	var cfg map[string]any
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, err
	}
	v := url.Values{"q": {query}, "sort": {"recent"}}
	for k, raw := range cfg {
		switch filters[k] {
		case "string":
			s, ok := raw.(string)
			if !ok || s == "" || len(s) > 200 {
				return nil, fmt.Errorf("%s must be a non-empty string", k)
			}
			v.Set(k, s)
		case "bool":
			b, ok := raw.(bool)
			if !ok {
				return nil, fmt.Errorf("%s must be true or false", k)
			}
			v.Set(k, strconv.FormatBool(b))
		default:
			return nil, fmt.Errorf("unknown search filter %q", k)
		}
	}
	return v, nil
}

type job struct {
	GUID                 string    `json:"guid"`
	Title                string    `json:"title"`
	CompanyName          string    `json:"companyName"`
	EmploymentType       string    `json:"employmentType"`
	MinSalary            *float64  `json:"minSalary"`
	MaxSalary            *float64  `json:"maxSalary"`
	Currency             string    `json:"currency"`
	SalaryPeriod         string    `json:"salaryPeriod"`
	LocationRestrictions []string  `json:"locationRestrictions"`
	TimezoneRestrictions []float64 `json:"timezoneRestrictions"`
	Description          string    `json:"description"`
	PubDate              int64     `json:"pubDate"`
	ExpiryDate           int64     `json:"expiryDate"`
	ApplicationLink      string    `json:"applicationLink"`
}

// Fetch runs the saved search page by page. Past MaxPages it returns what it has together
// with ingest.ErrTruncated.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	params, err := searchParams(src.BoardToken, src.Config)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	now := a.Now()
	var out []ingest.RawPosting
	seen := map[string]bool{}
	for page := 1; page <= MaxPages; page++ {
		params.Set("page", strconv.Itoa(page))
		body, err := a.Client.GetBody(ctx, a.SearchURL+"?"+params.Encode())
		if err != nil {
			return nil, err
		}
		var resp struct {
			Jobs []json.RawMessage `json:"jobs"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("decode page %d: %w", page, err)
		}
		if len(resp.Jobs) == 0 {
			return out, nil
		}
		for _, raw := range resp.Jobs {
			var j job
			if err := json.Unmarshal(raw, &j); err != nil || j.GUID == "" {
				return nil, fmt.Errorf("decode job: guid missing or invalid (%v)", err)
			}
			// Pages can shift while we read them (new jobs arrive); keep the first copy.
			if seen[j.GUID] {
				continue
			}
			seen[j.GUID] = true
			var expires *time.Time
			if j.ExpiryDate > 0 {
				t := time.Unix(j.ExpiryDate, 0).UTC()
				if !t.After(now) {
					continue // terms (d): expired jobs are not stored
				}
				expires = &t
			}
			out = append(out, ingest.RawPosting{
				ExternalID: j.GUID, URL: j.ApplicationLink, Payload: raw, ExpiresAt: expires,
			})
		}
	}
	return out, fmt.Errorf("%w at %d pages (%d jobs)", ingest.ErrTruncated, MaxPages, len(out))
}

var (
	employment = map[string]string{
		"Full Time": "full_time", "Part Time": "part_time", "Contractor": "contract", "Intern": "internship",
	}
	periods = map[string]string{"annual": "year", "monthly": "month", "hourly": "hour"}
)

// Normalize maps one Himalayas job to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var j job
	if err := json.Unmarshal(raw.Payload, &j); err != nil {
		return ingest.NormalizedJob{}, err
	}
	n := ingest.NormalizedJob{
		Title:           j.Title,
		CompanyName:     j.CompanyName,
		DescriptionHTML: j.Description,
		LocationText:    locationText(j),
		RemotePolicy:    "remote",
		EmploymentType:  "unknown",
	}
	if v, ok := employment[j.EmploymentType]; ok {
		n.EmploymentType = v
	}
	if j.PubDate > 0 {
		t := time.Unix(j.PubDate, 0).UTC()
		n.PostedAt = &t
	}
	if j.MinSalary != nil || j.MaxSalary != nil {
		n.SalaryMin, n.SalaryMax = j.MinSalary, j.MaxSalary
		if len(j.Currency) == 3 {
			c := strings.ToUpper(j.Currency)
			n.SalaryCurrency = &c
		}
		if p, ok := periods[j.SalaryPeriod]; ok {
			n.SalaryPeriod = &p
		}
	}
	return n, nil
}

// locationText reads like "Remote: Germany, Poland (UTC-1 to UTC+3)" or "Remote: worldwide".
func locationText(j job) string {
	where := "worldwide"
	if len(j.LocationRestrictions) > 0 {
		where = strings.Join(j.LocationRestrictions, ", ")
	}
	text := "Remote: " + where
	if len(j.TimezoneRestrictions) > 0 {
		lo, hi := j.TimezoneRestrictions[0], j.TimezoneRestrictions[0]
		for _, tz := range j.TimezoneRestrictions {
			lo, hi = min(lo, tz), max(hi, tz)
		}
		text += fmt.Sprintf(" (UTC%s to UTC%s)", offset(lo), offset(hi))
	}
	return text
}

func offset(h float64) string {
	s := strconv.FormatFloat(h, 'f', -1, 64)
	if h >= 0 {
		return "+" + s
	}
	return s
}
