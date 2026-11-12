// Package ashby reads Ashby job boards.
//
// API: GET https://api.ashbyhq.com/posting-api/job-board/{job_board_name}?includeCompensation=true
// (docs: https://developers.ashbyhq.com/docs/public-job-posting-api, checked 2026-11-13).
// Public: no auth, rate limits, pagination or terms of use are stated.
//
// Notes on the data:
//   - Compensation is per job (the docs' example also shows it at the top level; real
//     responses have it per job). The salary comes from compensation.summaryComponents,
//     the Salary entry: one range in one currency across all of the job's tiers.
//   - Postings with isListed=false are reachable only by direct link; the company chose not
//     to list them, so they are skipped.
//   - Countries come as names ("Germany", "European Union"), not ISO codes. They go into the
//     location text; the countries field stays empty.
//     shortcut: no name-to-ISO table; add one if matching needs structured countries.
//   - employmentType "Temporary" is not a contract, so it stays unknown.
package ashby

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// BaseURL is the Ashby posting API; tests replace it.
const BaseURL = "https://api.ashbyhq.com/posting-api/job-board/"

// Adapter implements ingest.Adapter for Ashby.
type Adapter struct {
	Client  *ingest.HTTPClient
	BaseURL string
}

// New returns an adapter for the real API.
func New(client *ingest.HTTPClient) *Adapter { return &Adapter{Client: client, BaseURL: BaseURL} }

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "ashby" }

type job struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	Location           string `json:"location"`
	SecondaryLocations []struct {
		Location string `json:"location"`
	} `json:"secondaryLocations"`
	IsListed        *bool  `json:"isListed"`
	WorkplaceType   string `json:"workplaceType"`
	EmploymentType  string `json:"employmentType"`
	PublishedAt     string `json:"publishedAt"`
	JobURL          string `json:"jobUrl"`
	DescriptionHTML string `json:"descriptionHtml"`
	Compensation    *struct {
		SummaryComponents []struct {
			CompensationType string   `json:"compensationType"`
			Interval         string   `json:"interval"`
			CurrencyCode     string   `json:"currencyCode"`
			MinValue         *float64 `json:"minValue"`
			MaxValue         *float64 `json:"maxValue"`
		} `json:"summaryComponents"`
	} `json:"compensation"`
}

// Fetch returns every listed posting of the board.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	u := a.BaseURL + url.PathEscape(src.BoardToken) + "?includeCompensation=true"
	body, err := a.Client.GetBody(ctx, u)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Jobs []json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode board: %w", err)
	}
	out := make([]ingest.RawPosting, 0, len(resp.Jobs))
	for _, raw := range resp.Jobs {
		var j job
		if err := json.Unmarshal(raw, &j); err != nil || j.ID == "" {
			return nil, fmt.Errorf("decode job: id missing or invalid (%v)", err)
		}
		if j.IsListed != nil && !*j.IsListed {
			continue
		}
		out = append(out, ingest.RawPosting{ExternalID: j.ID, URL: j.JobURL, Payload: raw})
	}
	return out, nil
}

var (
	workplace  = map[string]string{"Remote": "remote", "Hybrid": "hybrid", "OnSite": "onsite"}
	employment = map[string]string{
		"FullTime": "full_time", "PartTime": "part_time", "Contract": "contract", "Intern": "internship",
	}
	intervals = map[string]string{"1 YEAR": "year", "1 MONTH": "month", "1 DAY": "day", "1 HOUR": "hour"}
)

// Normalize maps one Ashby posting to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var j job
	if err := json.Unmarshal(raw.Payload, &j); err != nil {
		return ingest.NormalizedJob{}, err
	}
	locations := []string{j.Location}
	for _, s := range j.SecondaryLocations {
		if s.Location != "" {
			locations = append(locations, s.Location)
		}
	}
	n := ingest.NormalizedJob{
		Title:           j.Title,
		DescriptionHTML: j.DescriptionHTML,
		LocationText:    strings.Join(locations, "; "),
		RemotePolicy:    "unknown",
		EmploymentType:  "unknown",
	}
	if v, ok := workplace[j.WorkplaceType]; ok {
		n.RemotePolicy = v
	}
	if v, ok := employment[j.EmploymentType]; ok {
		n.EmploymentType = v
	}
	if t, err := time.Parse(time.RFC3339, j.PublishedAt); err == nil {
		t = t.UTC()
		n.PostedAt = &t
	}
	if j.Compensation != nil {
		for _, c := range j.Compensation.SummaryComponents {
			if c.CompensationType != "Salary" || (c.MinValue == nil && c.MaxValue == nil) {
				continue
			}
			n.SalaryMin, n.SalaryMax = c.MinValue, c.MaxValue
			if len(c.CurrencyCode) == 3 {
				code := strings.ToUpper(c.CurrencyCode)
				n.SalaryCurrency = &code
			}
			if p, ok := intervals[c.Interval]; ok {
				n.SalaryPeriod = &p
			}
			break
		}
	}
	return n, nil
}
