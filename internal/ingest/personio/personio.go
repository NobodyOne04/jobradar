// Package personio reads Personio company job boards (ADR 0014).
//
// API: GET https://{company}.jobs.personio.de/xml?language=en (or .com), an XML list of
// published positions with their descriptions, no paging, no key (docs:
// https://developer.personio.de/docs/retrieving-open-job-positions, checked 2026-11-14).
// No rate limit or terms beyond the docs are stated.
//
// board_token is the subdomain from {company}.jobs.personio.de; config {"domain":"com"} for
// companies on jobs.personio.com.
//
// Notes on the data:
//   - XML, so each position is decoded into a struct and stored as JSON: raw_payload stays
//     jsonb and payload hashing works as for every other kind.
//   - The feed has no URL: it is built as {board}/job/{id}, Personio's position page (checked
//     on ottonova and tado; a company that turned its Personio pages off, like Personio
//     itself, 404s there).
//   - jobDescriptions are named HTML sections (CDATA), joined with their names as headings.
//   - No remote field: remote_policy stays unknown. employmentType + schedule give the
//     employment type (intern/trainee → internship, freelance → contract, else the schedule).
package personio

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// URLTemplate is the board root, %s being the company and %s the domain; tests replace it.
const URLTemplate = "https://%s.jobs.personio.%s"

// Adapter implements ingest.Adapter for Personio.
type Adapter struct {
	Client      *ingest.HTTPClient
	URLTemplate string
}

// New returns an adapter for the real API.
func New(client *ingest.HTTPClient) *Adapter {
	return &Adapter{Client: client, URLTemplate: URLTemplate}
}

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "personio" }

// ValidateBoardToken accepts a subdomain.
func (*Adapter) ValidateBoardToken(token string) error { return ingest.ValidSlug(token) }

// ValidateConfig accepts only {"domain":"de"|"com"}.
func (*Adapter) ValidateConfig(config json.RawMessage) error {
	_, err := domain(config)
	return err
}

func domain(config json.RawMessage) (string, error) {
	var cfg map[string]any
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return "", err
		}
	}
	d := "de"
	for k, v := range cfg {
		s, ok := v.(string)
		if k != "domain" || !ok || (s != "de" && s != "com") {
			return "", fmt.Errorf(`config may only set "domain": "de" or "com"`)
		}
		d = s
	}
	return d, nil
}

// position is one <position>, and also its stored JSON form.
type position struct {
	ID             string   `xml:"id" json:"id"`
	Subcompany     string   `xml:"subcompany" json:"subcompany,omitempty"`
	Office         string   `xml:"office" json:"office"`
	OtherOffices   []string `xml:"additionalOffices>office" json:"additional_offices,omitempty"`
	Department     string   `xml:"department" json:"department"`
	Name           string   `xml:"name" json:"name"`
	EmploymentType string   `xml:"employmentType" json:"employment_type"`
	Seniority      string   `xml:"seniority" json:"seniority"`
	Schedule       string   `xml:"schedule" json:"schedule"`
	CreatedAt      string   `xml:"createdAt" json:"created_at"`
	Descriptions   []struct {
		Name  string `xml:"name" json:"name"`
		Value string `xml:"value" json:"value"`
	} `xml:"jobDescriptions>jobDescription" json:"descriptions"`
}

// Fetch reads the company's positions.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	if err := ingest.ValidSlug(src.BoardToken); err != nil {
		return nil, err
	}
	d, err := domain(src.Config)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	board := fmt.Sprintf(a.URLTemplate, strings.ToLower(src.BoardToken), d)
	body, err := a.Client.GetBody(ctx, board+"/xml?language=en")
	if err != nil {
		return nil, err
	}
	var feed struct {
		Positions []position `xml:"position"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("decode positions: %w", err)
	}
	out := make([]ingest.RawPosting, 0, len(feed.Positions))
	for _, p := range feed.Positions {
		if p.ID == "" {
			return nil, fmt.Errorf("decode position: id missing")
		}
		payload, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		out = append(out, ingest.RawPosting{ExternalID: p.ID, URL: board + "/job/" + p.ID, Payload: payload})
	}
	return out, nil
}

// Normalize maps one stored position to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var p position
	if err := json.Unmarshal(raw.Payload, &p); err != nil {
		return ingest.NormalizedJob{}, err
	}
	var desc strings.Builder
	for _, d := range p.Descriptions {
		fmt.Fprintf(&desc, "<h3>%s</h3>%s", html.EscapeString(strings.TrimSpace(d.Name)), strings.TrimSpace(d.Value))
	}
	n := ingest.NormalizedJob{
		Title:           p.Name,
		CompanyName:     p.Subcompany,
		DescriptionHTML: desc.String(),
		LocationText:    strings.Join(append([]string{p.Office}, p.OtherOffices...), "; "),
		RemotePolicy:    "unknown",
		EmploymentType:  "unknown",
	}
	switch {
	case p.EmploymentType == "intern" || p.EmploymentType == "trainee":
		n.EmploymentType = "internship"
	case p.EmploymentType == "freelance":
		n.EmploymentType = "contract"
	case p.Schedule == "full-time":
		n.EmploymentType = "full_time"
	case p.Schedule == "part-time":
		n.EmploymentType = "part_time"
	}
	if t, err := time.Parse(time.RFC3339, p.CreatedAt); err == nil {
		t = t.UTC()
		n.PostedAt = &t
	}
	return n, nil
}
