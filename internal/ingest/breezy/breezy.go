// Package breezy reads Breezy HR company job boards (ADR 0014).
//
// API: GET https://{company}.breezy.hr/json, a JSON array of published positions, no paging,
// no key. Undocumented by Breezy; listed in the community catalog
// (https://github.com/Mersetti/ats-job-board-apis, checked 2026-11-14). No terms stated.
// Breezy's CDN occasionally answers 403 to scripted requests; the run then fails and the
// scheduler tries again later.
//
// board_token is the subdomain from {company}.breezy.hr; an unknown one is a 404.
//
// Notes on the data:
//   - The list has no description and Breezy has no JSON detail endpoint: jobs are stored
//     with an empty description, so the matcher sees only title, location and type.
//   - location.is_remote=true means remote; false or missing stays unknown.
//   - salary is free text ("$0.05 – $0.06 / hour"): left unknown (ADR 0005).
package breezy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// URLTemplate is the positions endpoint, %s being the company; tests replace it.
const URLTemplate = "https://%s.breezy.hr/json"

// Adapter implements ingest.Adapter for Breezy HR.
type Adapter struct {
	Client      *ingest.HTTPClient
	URLTemplate string
}

// New returns an adapter for the real API.
func New(client *ingest.HTTPClient) *Adapter {
	return &Adapter{Client: client, URLTemplate: URLTemplate}
}

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "breezy" }

// ValidateBoardToken accepts a subdomain.
func (*Adapter) ValidateBoardToken(token string) error { return ingest.ValidSlug(token) }

type position struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	PublishedDate string `json:"published_date"`
	Type          struct {
		ID string `json:"id"`
	} `json:"type"`
	Location struct {
		Name     string `json:"name"`
		IsRemote *bool  `json:"is_remote"`
		Country  struct {
			ID string `json:"id"`
		} `json:"country"`
	} `json:"location"`
	Company struct {
		Name string `json:"name"`
	} `json:"company"`
}

// Fetch reads the company's positions.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	if err := ingest.ValidSlug(src.BoardToken); err != nil {
		return nil, err
	}
	body, err := a.Client.GetBody(ctx, fmt.Sprintf(a.URLTemplate, strings.ToLower(src.BoardToken)))
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode positions: %w", err)
	}
	out := make([]ingest.RawPosting, 0, len(items))
	for _, raw := range items {
		var p position
		if err := json.Unmarshal(raw, &p); err != nil || p.ID == "" {
			return nil, fmt.Errorf("decode position: id missing or invalid (%v)", err)
		}
		out = append(out, ingest.RawPosting{ExternalID: p.ID, URL: p.URL, Payload: raw})
	}
	return out, nil
}

var employment = map[string]string{
	"fullTime": "full_time", "partTime": "part_time", "contract": "contract",
	"temporary": "contract", "internship": "internship",
}

// Normalize maps one Breezy position to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var p position
	if err := json.Unmarshal(raw.Payload, &p); err != nil {
		return ingest.NormalizedJob{}, err
	}
	n := ingest.NormalizedJob{
		Title:          p.Name,
		CompanyName:    p.Company.Name,
		LocationText:   p.Location.Name,
		RemotePolicy:   "unknown",
		EmploymentType: "unknown",
	}
	if p.Location.IsRemote != nil && *p.Location.IsRemote {
		n.RemotePolicy = "remote"
	}
	if v, ok := employment[p.Type.ID]; ok {
		n.EmploymentType = v
	}
	if c := strings.ToUpper(p.Location.Country.ID); len(c) == 2 {
		n.Countries = []string{c}
	}
	if t, err := time.Parse(time.RFC3339, p.PublishedDate); err == nil {
		t = t.UTC()
		n.PostedAt = &t
	}
	return n, nil
}
