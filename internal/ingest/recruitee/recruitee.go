// Package recruitee reads Recruitee company job boards (ADR 0014).
//
// API: GET https://{company}.recruitee.com/api/offers/, every published offer with its
// description and requirements, no paging, no key (docs:
// https://docs.recruitee.com/reference/offers, checked 2026-11-14). No rate limit or terms
// beyond the docs are stated.
//
// board_token is the subdomain from {company}.recruitee.com; an unknown one is a 404.
//
// Notes on the data:
//   - remote, hybrid and on_site are explicit booleans; none set leaves remote_policy unknown.
//   - employment_type_code is e.g. fulltime_permanent, fulltime_fixed_term, parttime_*.
//   - The URL is careers_url (the company's own careers site).
package recruitee

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// URLTemplate is the offers endpoint, %s being the company; tests replace it.
const URLTemplate = "https://%s.recruitee.com/api/offers/"

// Adapter implements ingest.Adapter for Recruitee.
type Adapter struct {
	Client      *ingest.HTTPClient
	URLTemplate string
}

// New returns an adapter for the real API.
func New(client *ingest.HTTPClient) *Adapter {
	return &Adapter{Client: client, URLTemplate: URLTemplate}
}

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "recruitee" }

// ValidateBoardToken accepts a subdomain.
func (*Adapter) ValidateBoardToken(token string) error { return ingest.ValidSlug(token) }

type offer struct {
	ID                 int64  `json:"id"`
	Title              string `json:"title"`
	CompanyName        string `json:"company_name"`
	Description        string `json:"description"`
	Requirements       string `json:"requirements"`
	Location           string `json:"location"`
	Remote             bool   `json:"remote"`
	Hybrid             bool   `json:"hybrid"`
	OnSite             bool   `json:"on_site"`
	EmploymentTypeCode string `json:"employment_type_code"`
	PublishedAt        string `json:"published_at"`
	CareersURL         string `json:"careers_url"`
	Locations          []struct {
		CountryCode string `json:"country_code"`
	} `json:"locations"`
	Salary struct {
		Min      *json.Number `json:"min"`
		Max      *json.Number `json:"max"`
		Period   *string      `json:"period"`
		Currency *string      `json:"currency"`
	} `json:"salary"`
}

// Fetch reads the company's offers.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	if err := ingest.ValidSlug(src.BoardToken); err != nil {
		return nil, err
	}
	body, err := a.Client.GetBody(ctx, fmt.Sprintf(a.URLTemplate, strings.ToLower(src.BoardToken)))
	if err != nil {
		return nil, err
	}
	var resp struct {
		Offers []json.RawMessage `json:"offers"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode offers: %w", err)
	}
	out := make([]ingest.RawPosting, 0, len(resp.Offers))
	for _, raw := range resp.Offers {
		var o offer
		if err := json.Unmarshal(raw, &o); err != nil || o.ID == 0 {
			return nil, fmt.Errorf("decode offer: id missing or invalid (%v)", err)
		}
		out = append(out, ingest.RawPosting{ExternalID: strconv.FormatInt(o.ID, 10), URL: o.CareersURL, Payload: raw})
	}
	return out, nil
}

var periods = map[string]string{
	"year": "year", "yearly": "year", "annual": "year", "month": "month", "monthly": "month",
	"day": "day", "daily": "day", "hour": "hour", "hourly": "hour",
}

// Normalize maps one Recruitee offer to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var o offer
	if err := json.Unmarshal(raw.Payload, &o); err != nil {
		return ingest.NormalizedJob{}, err
	}
	desc := o.Description
	if o.Requirements != "" {
		desc += "<h3>Requirements</h3>" + o.Requirements
	}
	n := ingest.NormalizedJob{
		Title:           o.Title,
		CompanyName:     o.CompanyName,
		DescriptionHTML: desc,
		LocationText:    o.Location,
		RemotePolicy:    "unknown",
		EmploymentType:  "unknown",
	}
	switch {
	case o.Remote:
		n.RemotePolicy = "remote"
	case o.Hybrid:
		n.RemotePolicy = "hybrid"
	case o.OnSite:
		n.RemotePolicy = "onsite"
	}
	switch kind, _, _ := strings.Cut(o.EmploymentTypeCode, "_"); kind {
	case "fulltime":
		n.EmploymentType = "full_time"
	case "parttime":
		n.EmploymentType = "part_time"
	case "internship":
		n.EmploymentType = "internship"
	case "freelance", "contract":
		n.EmploymentType = "contract"
	}
	for _, l := range o.Locations {
		if c := strings.ToUpper(l.CountryCode); len(c) == 2 && !slices.Contains(n.Countries, c) {
			n.Countries = append(n.Countries, c)
		}
	}
	if t, err := time.Parse("2006-01-02 15:04:05 MST", o.PublishedAt); err == nil {
		t = t.UTC()
		n.PostedAt = &t
	}
	if o.Salary.Min != nil || o.Salary.Max != nil {
		n.SalaryMin, n.SalaryMax = number(o.Salary.Min), number(o.Salary.Max)
		if c := o.Salary.Currency; c != nil && len(*c) == 3 {
			up := strings.ToUpper(*c)
			n.SalaryCurrency = &up
		}
		if p := o.Salary.Period; p != nil {
			if v, ok := periods[strings.ToLower(*p)]; ok {
				n.SalaryPeriod = &v
			}
		}
	}
	return n, nil
}

func number(n *json.Number) *float64 {
	if n == nil {
		return nil
	}
	f, err := n.Float64()
	if err != nil {
		return nil
	}
	return &f
}
