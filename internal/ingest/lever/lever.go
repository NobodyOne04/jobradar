// Package lever reads Lever job sites.
//
// API: GET https://api.lever.co/v0/postings/{site}?mode=json&skip=N&limit=N, or
// api.eu.lever.co for sites on Lever's EU instance (docs: https://github.com/lever/postings-api,
// checked 2026-11-13). Public: no auth for GET, no rate limit or terms of use stated; the
// README notes published postings "may be scraped by third parties".
//
// Source config: {"region": "eu"} selects the EU host; anything else, or no config, the global one.
//
// Notes on the data:
//   - Requirements and responsibilities are in `lists`, not in `description`, so the
//     description HTML is rebuilt from description + lists + additional.
//   - `createdAt` (epoch milliseconds) is returned but not documented; used when present.
//   - `workplaceType` is "onsite" in real responses although the docs say "on-site"; both map.
//   - `categories.commitment` is a free label per company (leverdemo uses "Remote" as one),
//     so employment type stays unknown rather than being guessed from it.
//   - Salary interval values other than "per-year-salary" are not documented; the period is
//     read from the word in it (year, month, day, hour) and left unknown otherwise.
package lever

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

const (
	// GlobalURL and EUURL are Lever's two postings hosts; tests replace them.
	GlobalURL = "https://api.lever.co/v0/postings/"
	EUURL     = "https://api.eu.lever.co/v0/postings/"
	pageSize  = 100
	maxPages  = 100 // 10,000 postings: far beyond any company; stops a runaway loop
)

// Adapter implements ingest.Adapter for Lever.
type Adapter struct {
	Client    *ingest.HTTPClient
	GlobalURL string
	EUURL     string
}

// New returns an adapter for the real API.
func New(client *ingest.HTTPClient) *Adapter {
	return &Adapter{Client: client, GlobalURL: GlobalURL, EUURL: EUURL}
}

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "lever" }

type posting struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	HostedURL   string `json:"hostedUrl"`
	Description string `json:"description"`
	Additional  string `json:"additional"`
	Lists       []struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	} `json:"lists"`
	Categories struct {
		Location     string   `json:"location"`
		AllLocations []string `json:"allLocations"`
	} `json:"categories"`
	WorkplaceType string `json:"workplaceType"`
	Country       string `json:"country"`
	CreatedAt     int64  `json:"createdAt"`
	SalaryRange   *struct {
		Min      *float64 `json:"min"`
		Max      *float64 `json:"max"`
		Currency string   `json:"currency"`
		Interval string   `json:"interval"`
	} `json:"salaryRange"`
}

// Fetch returns every published posting of the site, page by page.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	var cfg struct {
		Region string `json:"region"`
	}
	_ = json.Unmarshal(src.Config, &cfg) // config is validated as a JSON object on create
	base := a.GlobalURL
	if strings.EqualFold(cfg.Region, "eu") {
		base = a.EUURL
	}

	var out []ingest.RawPosting
	for page := range maxPages {
		u := fmt.Sprintf("%s%s?mode=json&skip=%d&limit=%d",
			base, url.PathEscape(src.BoardToken), page*pageSize, pageSize)
		body, err := a.Client.GetBody(ctx, u)
		if err != nil {
			return nil, err
		}
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("decode page %d: %w", page, err)
		}
		for _, raw := range items {
			var p posting
			if err := json.Unmarshal(raw, &p); err != nil || p.ID == "" {
				return nil, fmt.Errorf("decode posting: id missing or invalid (%v)", err)
			}
			out = append(out, ingest.RawPosting{ExternalID: p.ID, URL: p.HostedURL, Payload: raw})
		}
		if len(items) < pageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("more than %d pages: refusing a possibly endless listing", maxPages)
}

var workplace = map[string]string{
	"remote": "remote", "hybrid": "hybrid", "onsite": "onsite", "on-site": "onsite",
}

// Normalize maps one Lever posting to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var p posting
	if err := json.Unmarshal(raw.Payload, &p); err != nil {
		return ingest.NormalizedJob{}, err
	}
	n := ingest.NormalizedJob{
		Title:           p.Text,
		DescriptionHTML: descriptionHTML(p),
		LocationText:    p.Categories.Location,
		RemotePolicy:    "unknown",
		EmploymentType:  "unknown",
	}
	if len(p.Categories.AllLocations) > 1 {
		n.LocationText = strings.Join(p.Categories.AllLocations, "; ")
	}
	if wp, ok := workplace[p.WorkplaceType]; ok {
		n.RemotePolicy = wp
	}
	if len(p.Country) == 2 {
		n.Countries = []string{strings.ToUpper(p.Country)}
	}
	if p.CreatedAt > 0 {
		t := time.UnixMilli(p.CreatedAt).UTC()
		n.PostedAt = &t
	}
	if s := p.SalaryRange; s != nil && (s.Min != nil || s.Max != nil) {
		n.SalaryMin, n.SalaryMax = s.Min, s.Max
		if len(s.Currency) == 3 {
			c := strings.ToUpper(s.Currency)
			n.SalaryCurrency = &c
		}
		for _, period := range []string{"year", "month", "day", "hour"} {
			if strings.Contains(strings.ToLower(s.Interval), period) {
				n.SalaryPeriod = &period
				break
			}
		}
	}
	return n, nil
}

// descriptionHTML joins the parts Lever splits apart; list content is HTML list items.
func descriptionHTML(p posting) string {
	var b strings.Builder
	b.WriteString(p.Description)
	for _, l := range p.Lists {
		fmt.Fprintf(&b, "<h3>%s</h3><ul>%s</ul>", html.EscapeString(l.Text), l.Content)
	}
	b.WriteString(p.Additional)
	return b.String()
}
