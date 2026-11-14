// Package workable reads Workable company job boards (ADR 0014).
//
// API: GET https://apply.workable.com/api/v1/widget/accounts/{account}?details=true, the
// public careers-widget feed: every published job with its description, no paging, no key
// (docs: https://workable.readme.io/reference/jobs-1, checked 2026-11-14). No rate limit or
// terms beyond the docs are stated.
//
// board_token is the account from apply.workable.com/{account}; an unknown one is a 404.
//
// Notes on the data:
//   - telecommuting=true means remote; false is not proof of an office job, so it stays unknown.
//   - locations[] has ISO country codes; published_on is a date (no time).
//   - Company name is on the account, not the job: the sync uses the source's display name.
package workable

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

// BaseURL is the widget API root; tests replace it.
const BaseURL = "https://apply.workable.com/api/v1/widget/accounts/"

// Adapter implements ingest.Adapter for Workable.
type Adapter struct {
	Client  *ingest.HTTPClient
	BaseURL string
}

// New returns an adapter for the real API.
func New(client *ingest.HTTPClient) *Adapter { return &Adapter{Client: client, BaseURL: BaseURL} }

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "workable" }

// ValidateBoardToken accepts an account slug.
func (*Adapter) ValidateBoardToken(token string) error { return ingest.ValidSlug(token) }

type job struct {
	Title          string `json:"title"`
	Shortcode      string `json:"shortcode"`
	EmploymentType string `json:"employment_type"`
	Telecommuting  bool   `json:"telecommuting"`
	URL            string `json:"url"`
	PublishedOn    string `json:"published_on"`
	Country        string `json:"country"`
	City           string `json:"city"`
	Description    string `json:"description"`
	Locations      []struct {
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
		City        string `json:"city"`
	} `json:"locations"`
}

// Fetch reads the account's jobs.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	if err := ingest.ValidSlug(src.BoardToken); err != nil {
		return nil, err
	}
	body, err := a.Client.GetBody(ctx, a.BaseURL+url.PathEscape(src.BoardToken)+"?details=true")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Jobs []json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode jobs: %w", err)
	}
	out := make([]ingest.RawPosting, 0, len(resp.Jobs))
	for _, raw := range resp.Jobs {
		var j job
		if err := json.Unmarshal(raw, &j); err != nil || j.Shortcode == "" {
			return nil, fmt.Errorf("decode job: shortcode missing or invalid (%v)", err)
		}
		out = append(out, ingest.RawPosting{ExternalID: j.Shortcode, URL: j.URL, Payload: raw})
	}
	return out, nil
}

var employment = map[string]string{
	"full-time": "full_time", "part-time": "part_time", "contract": "contract",
	"temporary": "contract", "internship": "internship",
}

// Normalize maps one Workable job to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var j job
	if err := json.Unmarshal(raw.Payload, &j); err != nil {
		return ingest.NormalizedJob{}, err
	}
	n := ingest.NormalizedJob{
		Title:           j.Title,
		DescriptionHTML: j.Description,
		RemotePolicy:    "unknown",
		EmploymentType:  "unknown",
	}
	if j.Telecommuting {
		n.RemotePolicy = "remote"
	}
	if v, ok := employment[strings.ToLower(j.EmploymentType)]; ok {
		n.EmploymentType = v
	}
	var places []string
	for _, l := range j.Locations {
		places = append(places, strings.Trim(strings.Join([]string{l.City, l.Country}, ", "), ", "))
		if len(l.CountryCode) == 2 && !slices.Contains(n.Countries, strings.ToUpper(l.CountryCode)) {
			n.Countries = append(n.Countries, strings.ToUpper(l.CountryCode))
		}
	}
	if len(places) == 0 {
		places = []string{strings.Trim(strings.Join([]string{j.City, j.Country}, ", "), ", ")}
	}
	n.LocationText = strings.Join(places, "; ")
	if t, err := time.Parse(time.DateOnly, j.PublishedOn); err == nil {
		n.PostedAt = &t
	}
	return n, nil
}
