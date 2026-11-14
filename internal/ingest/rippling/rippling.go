// Package rippling reads Rippling company job boards (ADR 0014).
//
// API, undocumented by Rippling (community catalog: https://github.com/Mersetti/ats-job-board-apis,
// checked 2026-11-14), no key, no terms stated; the schema may change without notice:
//   - GET https://ats.rippling.com/api/v2/board/{board}/jobs?page=N: 20 jobs a page, with
//     totalPages; no descriptions.
//   - GET .../jobs/{id}: one job with its description, employment type and pay ranges.
//
// board_token is the board slug from ats.rippling.com/{board}/jobs.
//
// Notes on the data:
//   - The list repeats a job once per location (same id): Fetch merges the repeats.
//   - This is an ingest.Enricher: only new or changed list entries cost a detail request.
//     The stored payload is {"list": entry, "detail": job}: workplace type and country codes
//     are only in the list entry, the rest only in the detail.
//   - workplaceType REMOTE/HYBRID/ON_SITE is structured: used when all locations agree.
//   - payRangeDetails: the first range, if its frequency is YEAR or HOUR.
package rippling

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// BaseURL is the board API root; tests replace it.
const BaseURL = "https://ats.rippling.com/api/v2/board/"

// MaxPages bounds the list at 5,000 jobs; past it the listing is reported as truncated.
const MaxPages = 250

// Adapter implements ingest.Adapter for Rippling.
type Adapter struct {
	Client  *ingest.HTTPClient
	BaseURL string
}

// New returns an adapter for the real API.
func New(client *ingest.HTTPClient) *Adapter { return &Adapter{Client: client, BaseURL: BaseURL} }

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "rippling" }

// ValidateBoardToken accepts a board slug.
func (*Adapter) ValidateBoardToken(token string) error { return ingest.ValidSlug(token) }

// entry is one job in the list (locations merged across its repeats), as stored.
type entry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Locations []struct {
		Name          string `json:"name"`
		CountryCode   string `json:"countryCode"`
		WorkplaceType string `json:"workplaceType"`
	} `json:"locations"`
}

type detail struct {
	UUID        string            `json:"uuid"`
	Name        string            `json:"name"`
	CompanyName string            `json:"companyName"`
	Description map[string]string `json:"description"`
	CreatedOn   string            `json:"createdOn"`
	URL         string            `json:"url"`
	Employment  struct {
		Label string `json:"label"`
	} `json:"employmentType"`
	PayRanges []struct {
		Currency   string   `json:"currency"`
		Frequency  string   `json:"frequency"`
		RangeStart *float64 `json:"rangeStart"`
		RangeEnd   *float64 `json:"rangeEnd"`
	} `json:"payRangeDetails"`
}

type stored struct {
	List   json.RawMessage `json:"list"`
	Detail json.RawMessage `json:"detail"`
}

func (a *Adapter) jobsURL(token string) string { return a.BaseURL + url.PathEscape(token) + "/jobs" }

// Fetch reads every page of the board. The list repeats a job once per location, with the
// same id: those entries are merged into one, with all the locations.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	if err := ingest.ValidSlug(src.BoardToken); err != nil {
		return nil, err
	}
	var order []string
	jobs := map[string]*entry{}
	for page := 0; ; page++ {
		if page == MaxPages {
			out, err := postings(order, jobs)
			if err != nil {
				return nil, err
			}
			return out, fmt.Errorf("%w at %d pages (%d jobs)", ingest.ErrTruncated, MaxPages, len(out))
		}
		body, err := a.Client.GetBody(ctx, fmt.Sprintf("%s?page=%d", a.jobsURL(src.BoardToken), page))
		if err != nil {
			return nil, err
		}
		var resp struct {
			Items      []entry `json:"items"`
			TotalPages int     `json:"totalPages"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("decode page %d: %w", page, err)
		}
		for _, e := range resp.Items {
			if e.ID == "" {
				return nil, fmt.Errorf("decode job: id missing")
			}
			if j, ok := jobs[e.ID]; ok {
				j.Locations = append(j.Locations, e.Locations...)
				continue
			}
			order = append(order, e.ID)
			jobs[e.ID] = &e
		}
		if len(resp.Items) == 0 || page+1 >= resp.TotalPages {
			return postings(order, jobs)
		}
	}
}

func postings(order []string, jobs map[string]*entry) ([]ingest.RawPosting, error) {
	out := make([]ingest.RawPosting, 0, len(order))
	for _, id := range order {
		payload, err := json.Marshal(jobs[id])
		if err != nil {
			return nil, err
		}
		out = append(out, ingest.RawPosting{ExternalID: id, URL: jobs[id].URL, Payload: payload})
	}
	return out, nil
}

// Enrich fetches the job's detail and stores it with the list entry.
func (a *Adapter) Enrich(ctx context.Context, src store.Source, raw ingest.RawPosting) (ingest.RawPosting, error) {
	body, err := a.Client.GetBody(ctx, a.jobsURL(src.BoardToken)+"/"+url.PathEscape(raw.ExternalID))
	if err != nil {
		return raw, err
	}
	var d detail
	if err := json.Unmarshal(body, &d); err != nil || d.UUID != raw.ExternalID {
		return raw, fmt.Errorf("decode job %s: id missing or different (%v)", raw.ExternalID, err)
	}
	payload, err := json.Marshal(stored{List: raw.Payload, Detail: body})
	if err != nil {
		return raw, err
	}
	raw.Payload = payload
	return raw, nil
}

var (
	employment = map[string]string{
		"SALARIED_FT": "full_time", "HOURLY_FT": "full_time", "SALARIED_PT": "part_time",
		"HOURLY_PT": "part_time", "CONTRACTOR": "contract", "INTERN": "internship",
	}
	workplace = map[string]string{"REMOTE": "remote", "HYBRID": "hybrid", "ON_SITE": "onsite"}
	periods   = map[string]string{"YEAR": "year", "HOUR": "hour"}
)

// Normalize maps one stored job ({"list","detail"}) to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var s stored
	if err := json.Unmarshal(raw.Payload, &s); err != nil {
		return ingest.NormalizedJob{}, err
	}
	var e entry
	var d detail
	if err := json.Unmarshal(s.List, &e); err != nil {
		return ingest.NormalizedJob{}, fmt.Errorf("list entry: %w", err)
	}
	if err := json.Unmarshal(s.Detail, &d); err != nil {
		return ingest.NormalizedJob{}, fmt.Errorf("detail: %w", err)
	}
	n := ingest.NormalizedJob{
		Title:           d.Name,
		CompanyName:     d.CompanyName,
		DescriptionHTML: d.Description["company"] + d.Description["role"],
		RemotePolicy:    "unknown",
		EmploymentType:  "unknown",
	}
	var places, types []string
	for _, l := range e.Locations {
		places = append(places, l.Name)
		if !slices.Contains(types, l.WorkplaceType) {
			types = append(types, l.WorkplaceType)
		}
		if c := strings.ToUpper(l.CountryCode); len(c) == 2 && !slices.Contains(n.Countries, c) {
			n.Countries = append(n.Countries, c)
		}
	}
	n.LocationText = strings.Join(places, "; ")
	if len(types) == 1 {
		if v, ok := workplace[types[0]]; ok {
			n.RemotePolicy = v
		}
	}
	if v, ok := employment[d.Employment.Label]; ok {
		n.EmploymentType = v
	}
	if t, err := time.Parse(time.RFC3339, d.CreatedOn); err == nil {
		t = t.UTC()
		n.PostedAt = &t
	}
	if len(d.PayRanges) > 0 {
		r := d.PayRanges[0]
		if p, ok := periods[r.Frequency]; ok && (r.RangeStart != nil || r.RangeEnd != nil) {
			n.SalaryMin, n.SalaryMax, n.SalaryPeriod = r.RangeStart, r.RangeEnd, &p
			if len(r.Currency) == 3 {
				c := strings.ToUpper(r.Currency)
				n.SalaryCurrency = &c
			}
		}
	}
	return n, nil
}
