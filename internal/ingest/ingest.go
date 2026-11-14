// Package ingest fetches job postings from sources and stores them as canonical jobs
// (ADR 0005). Each source kind is an Adapter in its own subpackage.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/NobodyOne04/jobradar/internal/store"
)

// RawPosting is one posting as the source returned it.
type RawPosting struct {
	ExternalID string          // the source's own ID, unique within the source
	URL        string          // where a person views or applies to it
	Payload    json.RawMessage // the source's JSON for this posting, kept as is
	ExpiresAt  *time.Time      // when the source says it stops being listed; nil if it does not say
	// ListHash, set by the sync for an Enricher, is the hash of the list entry Payload was
	// built from: stored as payload_hash, so an unchanged entry is not enriched again.
	ListHash string
}

// ErrTruncated is returned by Fetch, together with the postings it did get, when the source
// has more than the adapter will fetch. The sync stores what it got but closes nothing as
// "missing", because the rest may still be listed.
var ErrTruncated = errors.New("listing truncated")

// ConfigValidator is implemented by adapters whose sources take settings in config. The API
// calls it when a source is created, so a bad config fails then, not at every sync.
type ConfigValidator interface {
	ValidateConfig(config json.RawMessage) error
}

// BoardTokenValidator is implemented by adapters that put board_token into a URL's host or
// path. The API calls it when a source is created; Fetch calls it too (ADR 0014).
type BoardTokenValidator interface {
	ValidateBoardToken(token string) error
}

var slugRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$`)

// ValidSlug accepts a platform's company identifier: letters, digits, '-' and '_', so it
// is safe in a URL host label or path segment.
func ValidSlug(token string) error {
	if !slugRE.MatchString(token) {
		return fmt.Errorf("%q is not a company identifier (letters, digits, - and _)", token)
	}
	return nil
}

// NormalizedJob is the source-independent job. Unknown values stay unknown: empty strings,
// "unknown" enums and nil pointers, never a guess. Go does not interpret free text (ADR 0005):
// remote hints or skills inside descriptions are the matcher's job.
type NormalizedJob struct {
	Title           string
	CompanyName     string // empty: the sync fills in the source's display name
	DescriptionHTML string
	LocationText    string
	RemotePolicy    string   // remote | hybrid | onsite | unknown
	Countries       []string // ISO 3166-1 alpha-2, upper case
	EmploymentType  string   // full_time | part_time | contract | internship | unknown
	SalaryMin       *float64
	SalaryMax       *float64
	SalaryCurrency  *string // ISO 4217, upper case
	SalaryPeriod    *string // year | month | day | hour
	PostedAt        *time.Time
}

// Adapter is one source kind. Fetch does I/O; Normalize is pure, so it is tested on
// recorded payloads.
type Adapter interface {
	Kind() string
	// Fetch returns the source's full current list of postings.
	Fetch(ctx context.Context, src store.Source) ([]RawPosting, error)
	Normalize(raw RawPosting) (NormalizedJob, error)
}

// Registry maps a source kind to its adapter.
type Registry map[string]Adapter

// NewRegistry indexes adapters by kind.
func NewRegistry(adapters ...Adapter) Registry {
	r := Registry{}
	for _, a := range adapters {
		r[a.Kind()] = a
	}
	return r
}

// Get returns the adapter for kind.
func (r Registry) Get(kind string) (Adapter, error) {
	a, ok := r[kind]
	if !ok {
		return nil, fmt.Errorf("no adapter for source kind %q", kind)
	}
	return a, nil
}
