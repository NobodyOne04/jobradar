// Package greenhouse reads Greenhouse job boards.
//
// API: GET https://boards-api.greenhouse.io/v1/boards/{board_token}/jobs?content=true
// (docs: https://developers.greenhouse.io/job-board.html, checked 2026-11-13). Public: "Job
// Board data is publicly available, so authentication is not required for any GET
// endpoints." No terms of use, rate limits, pagination or updated-since filter are documented.
//
// Notes on the data:
//   - `content` is HTML with entities escaped once (&lt;p&gt;), so it is unescaped here.
//   - `company_name` and `first_published` are returned but not documented; used when present.
//   - Pay is not in the list (only per job, with pay_transparency=true); skipped for now, so
//     salary is unknown for Greenhouse jobs (decided 2026-11-13).
//   - There is no structured remote, country or employment-type field: they stay unknown,
//     and the matcher reads hints from the text.
package greenhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"time"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// BaseURL is the Greenhouse job board API; tests point it at a local server.
const BaseURL = "https://boards-api.greenhouse.io/v1/boards/"

// Adapter implements ingest.Adapter for Greenhouse.
type Adapter struct {
	Client  *ingest.HTTPClient
	BaseURL string
}

// New returns an adapter for the real API.
func New(client *ingest.HTTPClient) *Adapter { return &Adapter{Client: client, BaseURL: BaseURL} }

// Kind is the sources.kind value.
func (*Adapter) Kind() string { return "greenhouse" }

type job struct {
	ID             int64                 `json:"id"`
	Title          string                `json:"title"`
	AbsoluteURL    string                `json:"absolute_url"`
	Content        string                `json:"content"`
	CompanyName    string                `json:"company_name"`
	FirstPublished string                `json:"first_published"`
	Location       struct{ Name string } `json:"location"`
}

// Fetch returns every published job of the board, with its description.
func (a *Adapter) Fetch(ctx context.Context, src store.Source) ([]ingest.RawPosting, error) {
	u := a.BaseURL + url.PathEscape(src.BoardToken) + "/jobs?content=true"
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
		if err := json.Unmarshal(raw, &j); err != nil || j.ID == 0 {
			return nil, fmt.Errorf("decode job: id missing or invalid (%v)", err)
		}
		out = append(out, ingest.RawPosting{
			ExternalID: strconv.FormatInt(j.ID, 10),
			URL:        j.AbsoluteURL,
			Payload:    raw,
		})
	}
	return out, nil
}

// Normalize maps one Greenhouse job to the canonical job.
func (*Adapter) Normalize(raw ingest.RawPosting) (ingest.NormalizedJob, error) {
	var j job
	if err := json.Unmarshal(raw.Payload, &j); err != nil {
		return ingest.NormalizedJob{}, err
	}
	n := ingest.NormalizedJob{
		Title:           j.Title,
		CompanyName:     j.CompanyName,
		DescriptionHTML: html.UnescapeString(j.Content),
		LocationText:    j.Location.Name,
		RemotePolicy:    "unknown",
		EmploymentType:  "unknown",
	}
	if t, err := time.Parse(time.RFC3339, j.FirstPublished); err == nil {
		t = t.UTC()
		n.PostedAt = &t
	}
	return n, nil
}
