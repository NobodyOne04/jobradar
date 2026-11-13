package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NobodyOne04/jobradar/internal/applications"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// Jobs serves /api/v1/jobs (ADR 0011): open jobs with their match against your profile and
// your application status. Responses carry your match explanations: never log them.
type Jobs struct {
	Queries *store.Queries
	Log     *slog.Logger
}

// Register adds the routes to mux.
func (j *Jobs) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/jobs", j.list)
	mux.HandleFunc("GET /api/v1/jobs/{job_id}", j.get)
}

var (
	bands       = []string{"strong", "good", "weak", "excluded"}
	remotes     = []string{"remote", "hybrid", "onsite", "unknown"}
	sourceKinds = []string{"greenhouse", "lever", "ashby", "himalayas"}
	sorts       = []string{"score", "posted", "updated"}
)

type source struct {
	Kind        string `json:"kind"`
	URL         string `json:"url"`
	Attribution string `json:"attribution,omitempty"`
	Closed      bool   `json:"closed,omitempty"`
}

type salary struct {
	Min      *float64 `json:"min"`
	Max      *float64 `json:"max"`
	Currency *string  `json:"currency"`
	Period   *string  `json:"period"`
}

func salaryOf(lo, hi *float64, cur, per *string) *salary {
	if lo == nil && hi == nil {
		return nil
	}
	return &salary{Min: lo, Max: hi, Currency: cur, Period: per}
}

// cursor is the last item of a page; Filters ties it to the sort and filters it was made for.
type cursor struct {
	Filters string  `json:"f"`
	K1      float64 `json:"a"`
	K2      float64 `json:"b"`
	ID      int64   `json:"i"`
}

func (j *Jobs) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := store.SearchJobsParams{Sort: "score", Lim: 25, Bands: []string{}}
	var problems []string
	bad := func(msg string) { problems = append(problems, msg) }

	if s := strings.TrimSpace(q.Get("q")); s != "" {
		if len(s) > 200 {
			bad("q: at most 200 characters")
		}
		p.Q = &s
	}
	for _, b := range q["band"] {
		if !slices.Contains(bands, b) {
			bad("band: one of strong, good, weak, excluded")
		}
		p.Bands = append(p.Bands, b)
	}
	if s := q.Get("min_score"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 100 {
			bad("min_score: 0-100")
		}
		n32 := int32(n)
		p.MinScore = &n32
	}
	oneOf := func(name string, allowed []string) *string {
		v := q.Get(name)
		if v == "" {
			return nil
		}
		if !slices.Contains(allowed, v) {
			bad(name + ": one of " + strings.Join(allowed, ", "))
		}
		return &v
	}
	p.Remote = oneOf("remote", remotes)
	p.Source = oneOf("source", sourceKinds)
	if s := q.Get("status"); s != "" {
		if s != applications.Discovered && !applications.Valid(s) {
			bad("status: discovered, saved, applied, interview, offer, rejected or archived")
		}
		p.Status = &s
	}
	if s := q.Get("posted_after"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			bad("posted_after: an RFC 3339 time, e.g. 2026-11-01T00:00:00Z")
		}
		p.PostedAfter = &t
	}
	if s := q.Get("sort"); s != "" {
		if !slices.Contains(sorts, s) {
			bad("sort: score, posted or updated")
		}
		p.Sort = s
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			bad("limit: 1-100")
		}
		p.Lim = int32(n)
	}
	if len(problems) > 0 {
		WriteProblem(w, http.StatusUnprocessableEntity, "invalid query", strings.Join(problems, "; "))
		return
	}

	filters := filterHash(q)
	if s := q.Get("cursor"); s != "" {
		c, ok := decodeCursor(s)
		if !ok || c.Filters != filters {
			WriteProblem(w, http.StatusUnprocessableEntity, "invalid cursor",
				"use the next_cursor of a page with the same sort and filters")
			return
		}
		p.After, p.AfterK1, p.AfterK2, p.AfterID = true, c.K1, c.K2, c.ID
	}

	profile, ok := resolveProfile(w, r, j.Queries, func(err error) { j.internal(w, r, err) }, 0)
	if !ok {
		return
	}
	if !j.context(w, r, profile, &p.RevisionID, &p.ChampionID) {
		return
	}
	p.ProfileID = profile
	limit := p.Lim
	p.Lim++ // one extra row says whether there is a next page
	rows, err := j.Queries.SearchJobs(r.Context(), p)
	if err != nil {
		j.internal(w, r, err)
		return
	}
	var next *string
	if int32(len(rows)) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		s := encodeCursor(cursor{Filters: filters, K1: last.K1, K2: last.K2, ID: last.ID})
		next = &s
	}

	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	srcs, err := j.sources(r, ids, false)
	if err != nil {
		j.internal(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var match any
		if row.Match != nil {
			match = row.Match
		}
		items = append(items, map[string]any{
			"id": row.ID, "title": row.Title, "company_name": row.CompanyName,
			"location_text": row.LocationText, "remote_policy": row.RemotePolicy,
			"employment_type": row.EmploymentType, "posted_at": row.PostedAt,
			"salary":      salaryOf(row.SalaryMin, row.SalaryMax, row.SalaryCurrency, row.SalaryPeriod),
			"sources":     nonNil(srcs[row.ID]),
			"match":       match,
			"application": map[string]string{"status": row.ApplicationStatus},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": items, "next_cursor": next})
}

// context finds the profile's current revision and the champion; 0 when there is none yet.
func (j *Jobs) context(w http.ResponseWriter, r *http.Request, profile int64, revision, champion *int64) bool {
	rev, err := j.Queries.GetLatestRevision(r.Context(), profile)
	switch {
	case err == nil:
		*revision = rev.ID
	case !errors.Is(err, pgx.ErrNoRows):
		j.internal(w, r, err)
		return false
	}
	c, err := j.Queries.ChampionVersionID(r.Context())
	if err != nil {
		j.internal(w, r, err)
		return false
	}
	*champion = c
	return true
}

func (j *Jobs) sources(r *http.Request, ids []int64, includeClosed bool) (map[int64][]source, error) {
	rows, err := j.Queries.JobSources(r.Context(), ids)
	if err != nil {
		return nil, err
	}
	out := map[int64][]source{}
	for _, row := range rows {
		if row.Closed && !includeClosed {
			continue
		}
		s := source{Kind: row.Kind, URL: row.Url, Closed: row.Closed}
		if row.Kind == "himalayas" {
			s.Attribution = "via Himalayas" // Himalayas terms §113 (b): show the source, link it
		}
		out[row.JobID] = append(out[row.JobID], s)
	}
	return out, nil
}

func (j *Jobs) get(w http.ResponseWriter, r *http.Request) {
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	job, err := j.Queries.GetJob(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		WriteProblem(w, http.StatusNotFound, "job not found", "")
		return
	}
	if err != nil {
		j.internal(w, r, err)
		return
	}
	profile, ok := resolveProfile(w, r, j.Queries, func(err error) { j.internal(w, r, err) }, 0)
	if !ok {
		return
	}
	var revision, champion int64
	if !j.context(w, r, profile, &revision, &champion) {
		return
	}
	srcs, err := j.sources(r, []int64{id}, true)
	if err != nil {
		j.internal(w, r, err)
		return
	}

	var match any
	m, err := j.Queries.JobMatch(r.Context(), store.JobMatchParams{
		JobID: id, ProfileID: profile, RevisionID: revision, ChampionID: champion,
	})
	switch {
	case err == nil:
		var details map[string]any
		_ = json.Unmarshal(m.Details, &details)
		match = map[string]any{
			"score": m.Score, "band": m.Band, "confidence": m.Confidence, "completeness": m.Completeness,
			"hard_constraints": m.HardConstraints, "model_version": m.ModelVersion, "stale": m.Stale,
			"scored_at": m.CreatedAt, "gates": details["gates"], "dimensions": details["dimensions"],
			"skills": details["skills"], "explanations": details["explanations"],
		}
	case !errors.Is(err, pgx.ErrNoRows):
		j.internal(w, r, err)
		return
	}

	application := map[string]any{"status": applications.Discovered, "events": []any{}}
	app, err := j.Queries.GetApplication(r.Context(), store.GetApplicationParams{JobID: id, ProfileID: profile})
	switch {
	case err == nil:
		events, err := j.Queries.ListApplicationEvents(r.Context(), app.ID)
		if err != nil {
			j.internal(w, r, err)
			return
		}
		application = map[string]any{"status": app.Status, "notes": app.Notes, "events": nonNil(events)}
	case !errors.Is(err, pgx.ErrNoRows):
		j.internal(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id": job.ID, "title": job.Title, "company_name": job.CompanyName,
		"description_text": job.DescriptionText, "location_text": job.LocationText,
		"remote_policy": job.RemotePolicy, "countries": job.Countries,
		"employment_type": job.EmploymentType, "posted_at": job.PostedAt, "closed_at": job.ClosedAt,
		"salary":      salaryOf(job.SalaryMin, job.SalaryMax, job.SalaryCurrency, job.SalaryPeriod),
		"sources":     nonNil(srcs[id]),
		"match":       match,
		"application": application,
	})
}

func (j *Jobs) internal(w http.ResponseWriter, r *http.Request, err error) {
	j.Log.Error("request failed", "request_id", RequestID(r.Context()), "error", err)
	WriteProblem(w, http.StatusInternalServerError, "internal error", "")
}

// filterHash identifies a sort and filter set, so a cursor is only used with its own.
func filterHash(q url.Values) string {
	keep := url.Values{}
	for k, v := range q {
		if k != "cursor" && k != "limit" {
			keep[k] = slices.Sorted(slices.Values(v))
		}
	}
	sum := sha256.Sum256([]byte(keep.Encode())) // Encode sorts by key
	return hex.EncodeToString(sum[:8])
}

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (cursor, bool) {
	var c cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) > 512 || json.Unmarshal(b, &c) != nil || c.ID < 1 {
		return cursor{}, false
	}
	return c, true
}
