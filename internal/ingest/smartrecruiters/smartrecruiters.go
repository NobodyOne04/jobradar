// Package smartrecruiters reads SmartRecruiters company job boards (ADR 0014).
//
// API (Posting API, docs: https://developers.smartrecruiters.com/docs/endpoints, checked
// 2026-11-14), public, no key; no rate limit or terms of use beyond the docs are stated:
//   - GET /v1/companies/{company}/postings?limit=100&offset=N: the list, without descriptions.
//   - GET /v1/companies/{company}/postings/{id}: one posting with its job ad sections.
//
// board_token is the company identifier from jobs.smartrecruiters.com/{company}.
//
// Notes on the data:
//   - An unknown company answers 200 with no postings: the sync warns on an empty first run.
//   - Descriptions need the detail request: this is an ingest.Enricher, so only new or
//     changed list entries cost one (a big board's first sync is slow: Bosch has ~5,000).
//   - location.remote and location.hybrid give remote and hybrid; neither flag is not proof of
//     an office job (the flags default to false), so it stays unknown.
package smartrecruiters

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// BaseURL is the Posting API root; tests replace it.
const BaseURL = "https://api.smartrecruiters.com/v1/companies/"

const pageSize = 100

// Adapter implements ingest.Adapter for SmartRecruiters.
type Adapter struct {
	Client  *ingest.HTTPClient
	BaseURL string
}

// New returns an adapter for the real API.
func New(client *ingest.HTTPClient) *Adapter { return &Adapter{Client: client, BaseURL: BaseURL} }

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "smartrecruiters" }

// ValidateBoardToken accepts a company identifier.
func (*Adapter) ValidateBoardToken(token string) error { return ingest.ValidSlug(token) }

type posting struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ReleasedDate string `json:"releasedDate"`
	PostingURL   string `json:"postingUrl"`
	Company      struct {
		Name string `json:"name"`
	} `json:"company"`
	Location struct {
		Country      string `json:"country"`
		FullLocation string `json:"fullLocation"`
		Remote       bool   `json:"remote"`
		Hybrid       bool   `json:"hybrid"`
	} `json:"location"`
	TypeOfEmployment struct {
		ID string `json:"id"`
	} `json:"typeOfEmployment"`
	JobAd struct {
		Sections map[string]struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"sections"`
	} `json:"jobAd"`
}

func (a *Adapter) companyURL(token string) string {
	return a.BaseURL + url.PathEscape(token) + "/postings"
}

// Fetch reads the whole list, page by page.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	if err := ingest.ValidSlug(src.BoardToken); err != nil {
		return nil, err
	}
	var out []ingest.RawPosting
	seen := map[string]bool{}
	for offset := 0; ; offset += pageSize {
		body, err := a.Client.GetBody(ctx, fmt.Sprintf("%s?limit=%d&offset=%d", a.companyURL(src.BoardToken), pageSize, offset))
		if err != nil {
			return nil, err
		}
		var page struct {
			TotalFound int               `json:"totalFound"`
			Content    []json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode page: %w", err)
		}
		for _, raw := range page.Content {
			var p posting
			if err := json.Unmarshal(raw, &p); err != nil || p.ID == "" {
				return nil, fmt.Errorf("decode posting: id missing or invalid (%v)", err)
			}
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			out = append(out, ingest.RawPosting{
				ExternalID: p.ID,
				URL:        "https://jobs.smartrecruiters.com/" + url.PathEscape(src.BoardToken) + "/" + p.ID,
				Payload:    raw,
			})
		}
		if len(page.Content) == 0 || offset+pageSize >= page.TotalFound {
			return out, nil
		}
	}
}

// Enrich fetches the posting with its job ad.
func (a *Adapter) Enrich(ctx context.Context, src store.Source, raw ingest.RawPosting) (ingest.RawPosting, error) {
	body, err := a.Client.GetBody(ctx, a.companyURL(src.BoardToken)+"/"+url.PathEscape(raw.ExternalID))
	if err != nil {
		return raw, err
	}
	var p posting
	if err := json.Unmarshal(body, &p); err != nil || p.ID != raw.ExternalID {
		return raw, fmt.Errorf("decode posting %s: id missing or different (%v)", raw.ExternalID, err)
	}
	raw.Payload = body
	if strings.HasPrefix(p.PostingURL, "https://") {
		raw.URL = p.PostingURL
	}
	return raw, nil
}

var (
	employment = map[string]string{
		"permanent": "full_time", "full_time": "full_time", "part_time": "part_time",
		"contract": "contract", "temporary": "contract", "intern": "internship", "internship": "internship",
	}
	sectionOrder = []string{"companyDescription", "jobDescription", "qualifications", "additionalInformation"}
)

// Normalize maps one posting (the detail response) to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var p posting
	if err := json.Unmarshal(raw.Payload, &p); err != nil {
		return ingest.NormalizedJob{}, err
	}
	var desc strings.Builder
	for _, key := range sectionOrder {
		if sec, ok := p.JobAd.Sections[key]; ok && sec.Text != "" {
			fmt.Fprintf(&desc, "<h3>%s</h3>%s", html.EscapeString(sec.Title), sec.Text)
		}
	}
	n := ingest.NormalizedJob{
		Title:           p.Name,
		CompanyName:     p.Company.Name,
		DescriptionHTML: desc.String(),
		LocationText:    p.Location.FullLocation,
		RemotePolicy:    "unknown",
		EmploymentType:  "unknown",
	}
	switch {
	case p.Location.Remote:
		n.RemotePolicy = "remote"
	case p.Location.Hybrid:
		n.RemotePolicy = "hybrid"
	}
	if len(p.Location.Country) == 2 {
		n.Countries = []string{strings.ToUpper(p.Location.Country)}
	}
	if v, ok := employment[p.TypeOfEmployment.ID]; ok {
		n.EmploymentType = v
	}
	if t, err := time.Parse(time.RFC3339, p.ReleasedDate); err == nil {
		t = t.UTC()
		n.PostedAt = &t
	}
	return n, nil
}
