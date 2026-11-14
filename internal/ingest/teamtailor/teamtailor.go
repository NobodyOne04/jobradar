// Package teamtailor reads Teamtailor company career sites (ADR 0014).
//
// API: GET https://{career host}/jobs.rss, an RSS feed of published jobs with descriptions,
// no paging, no key. Undocumented by Teamtailor; listed in the community catalog
// (https://github.com/Mersetti/ats-job-board-apis, checked 2026-11-14). No terms stated.
//
// board_token is the career site's host name, e.g. career.teamtailor.com or a company's own
// domain (careers.example.com): {slug}.teamtailor.com often 404s. Since the token is a whole
// host, ValidateBoardToken refuses IP addresses, ports, paths and single-label names
// (localhost), so a source cannot point the backend at itself or the compose network.
//
// Notes on the data:
//   - XML, so each item is decoded into a struct and stored as JSON (raw_payload is jsonb).
//   - remoteStatus: fully → remote, hybrid → hybrid, none → onsite; temporary or missing →
//     unknown.
//   - Locations are in the teamtailor.com/locations namespace, with country names (no codes).
package teamtailor

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// Scheme is how the feed is fetched; tests use http.
const Scheme = "https://"

// Adapter implements ingest.Adapter for Teamtailor.
type Adapter struct {
	Client *ingest.HTTPClient
	Scheme string
	// AllowHost, if set, accepts a host the validation would refuse; tests use it for
	// 127.0.0.1:port.
	AllowHost string
}

// New returns an adapter for the real sites.
func New(client *ingest.HTTPClient) *Adapter { return &Adapter{Client: client, Scheme: Scheme} }

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "teamtailor" }

var hostRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// ValidateBoardToken accepts a public DNS host name only.
func (a *Adapter) ValidateBoardToken(token string) error {
	if a.AllowHost != "" && token == a.AllowHost {
		return nil
	}
	h := strings.ToLower(token)
	if net.ParseIP(h) != nil || !hostRE.MatchString(h) || strings.HasSuffix(h, ".local") ||
		strings.HasSuffix(h, ".internal") || strings.HasSuffix(h, ".localhost") {
		return fmt.Errorf("%q is not a career site host name (e.g. career.teamtailor.com)", token)
	}
	return nil
}

// item is one RSS <item>, and also its stored JSON form.
type item struct {
	GUID         string `xml:"guid" json:"guid"`
	Title        string `xml:"title" json:"title"`
	Description  string `xml:"description" json:"description"`
	PubDate      string `xml:"pubDate" json:"pub_date"`
	Link         string `xml:"link" json:"link"`
	RemoteStatus string `xml:"remoteStatus" json:"remote_status"`
	Department   string `xml:"https://teamtailor.com/locations department" json:"department,omitempty"`
	Locations    []struct {
		Name    string `xml:"https://teamtailor.com/locations name" json:"name,omitempty"`
		City    string `xml:"https://teamtailor.com/locations city" json:"city,omitempty"`
		Country string `xml:"https://teamtailor.com/locations country" json:"country,omitempty"`
	} `xml:"https://teamtailor.com/locations locations>location" json:"locations,omitempty"`
}

// Fetch reads the site's feed.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	if err := a.ValidateBoardToken(src.BoardToken); err != nil {
		return nil, err
	}
	body, err := a.Client.GetBody(ctx, a.Scheme+strings.ToLower(src.BoardToken)+"/jobs.rss")
	if err != nil {
		return nil, err
	}
	var feed struct {
		Items []item `xml:"channel>item"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("decode feed: %w", err)
	}
	out := make([]ingest.RawPosting, 0, len(feed.Items))
	for _, it := range feed.Items {
		if it.GUID == "" {
			return nil, fmt.Errorf("decode item: guid missing")
		}
		payload, err := json.Marshal(it)
		if err != nil {
			return nil, err
		}
		out = append(out, ingest.RawPosting{ExternalID: it.GUID, URL: it.Link, Payload: payload})
	}
	return out, nil
}

var remote = map[string]string{"fully": "remote", "hybrid": "hybrid", "none": "onsite"}

// Normalize maps one stored item to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var it item
	if err := json.Unmarshal(raw.Payload, &it); err != nil {
		return ingest.NormalizedJob{}, err
	}
	n := ingest.NormalizedJob{
		Title:           it.Title,
		DescriptionHTML: it.Description,
		RemotePolicy:    "unknown",
		EmploymentType:  "unknown",
	}
	if v, ok := remote[it.RemoteStatus]; ok {
		n.RemotePolicy = v
	}
	var places []string
	for _, l := range it.Locations {
		place := strings.Trim(strings.Join([]string{l.City, l.Country}, ", "), ", ")
		if place == "" {
			place = l.Name
		}
		if place != "" {
			places = append(places, place)
		}
	}
	n.LocationText = strings.Join(places, "; ")
	if t, err := time.Parse(time.RFC1123Z, it.PubDate); err == nil {
		t = t.UTC()
		n.PostedAt = &t
	}
	return n, nil
}
