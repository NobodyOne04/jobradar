package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NobodyOne04/jobradar/internal/applications"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// Applications serves /api/v1/applications and /api/v1/labels/export (ADR 0008).
type Applications struct {
	Queries *store.Queries
	Service *applications.Service
	Log     *slog.Logger
}

// Register adds the routes to mux.
func (a *Applications) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/applications", a.list)
	mux.HandleFunc("GET /api/v1/applications/{job_id}", a.get)
	mux.HandleFunc("PUT /api/v1/applications/{job_id}", a.set)
	mux.HandleFunc("GET /api/v1/applications/{job_id}/events", a.events)
	mux.HandleFunc("GET /api/v1/labels/export", a.export)
}

// profileID is ?profile_id=, or the only profile when there is exactly one.
func (a *Applications) profileID(w http.ResponseWriter, r *http.Request, fromBody int64) (int64, bool) {
	if fromBody > 0 {
		return fromBody, true
	}
	if raw := r.URL.Query().Get("profile_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 1 {
			WriteProblem(w, http.StatusUnprocessableEntity, "invalid profile_id", "")
			return 0, false
		}
		return id, true
	}
	ids, err := a.Queries.ProfileIDs(r.Context())
	switch {
	case err != nil:
		a.internal(w, r, err)
		return 0, false
	case len(ids) == 0:
		WriteProblem(w, http.StatusUnprocessableEntity, "no profile", "import a profile first")
		return 0, false
	case len(ids) > 1:
		WriteProblem(w, http.StatusUnprocessableEntity, "profile_id required", "there are several profiles")
		return 0, false
	}
	return ids[0], true
}

func jobID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("job_id"), 10, 64)
	if err != nil || id < 1 {
		WriteProblem(w, http.StatusNotFound, "job not found", "")
		return 0, false
	}
	return id, true
}

type setRequest struct {
	Status     string     `json:"status"`
	Note       string     `json:"note"`
	OccurredAt *time.Time `json:"occurred_at"`
	Force      bool       `json:"force"`
	ProfileID  int64      `json:"profile_id"`
}

func (a *Applications) set(w http.ResponseWriter, r *http.Request) {
	job, ok := jobID(w, r)
	if !ok {
		return
	}
	var in setRequest
	if !decode(w, r, &in) {
		return
	}
	if msg := checkText("note", in.Note, 0, 5000); msg != "" {
		WriteProblem(w, http.StatusUnprocessableEntity, "invalid application", msg)
		return
	}
	profile, ok := a.profileID(w, r, in.ProfileID)
	if !ok {
		return
	}
	c := applications.Change{JobID: job, ProfileID: profile, Status: in.Status, Note: in.Note, Force: in.Force}
	if in.OccurredAt != nil {
		c.OccurredAt = *in.OccurredAt
	}
	res, err := a.Service.Set(r.Context(), c)
	switch {
	case errors.Is(err, applications.ErrNotFound):
		WriteProblem(w, http.StatusNotFound, "job not found", "")
		return
	case errors.Is(err, applications.ErrBackwards):
		WriteProblem(w, http.StatusConflict, "status change needs force", err.Error())
		return
	case errors.Is(err, applications.ErrInvalid):
		WriteProblem(w, http.StatusUnprocessableEntity, "invalid application", err.Error())
		return
	case isForeignKeyViolation(err):
		WriteProblem(w, http.StatusUnprocessableEntity, "profile not found", "")
		return
	case err != nil:
		a.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"application": res.Application, "event": res.Event, "changed": res.Event != nil,
	})
}

func (a *Applications) get(w http.ResponseWriter, r *http.Request) {
	job, ok := jobID(w, r)
	if !ok {
		return
	}
	profile, ok := a.profileID(w, r, 0)
	if !ok {
		return
	}
	app, err := a.Queries.GetApplication(r.Context(), store.GetApplicationParams{JobID: job, ProfileID: profile})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"job_id": job, "profile_id": profile, "status": applications.Discovered})
		return
	}
	if err != nil {
		a.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (a *Applications) list(w http.ResponseWriter, r *http.Request) {
	profile, ok := a.profileID(w, r, 0)
	if !ok {
		return
	}
	var status *string
	if s := r.URL.Query().Get("status"); s != "" {
		if !applications.Valid(s) {
			WriteProblem(w, http.StatusUnprocessableEntity, "invalid status", "one of saved, applied, interview, offer, rejected, archived")
			return
		}
		status = &s
	}
	rows, err := a.Queries.ListApplications(r.Context(), store.ListApplicationsParams{ProfileID: profile, Status: status})
	if err != nil {
		a.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applications": nonNil(rows)})
}

func (a *Applications) events(w http.ResponseWriter, r *http.Request) {
	job, ok := jobID(w, r)
	if !ok {
		return
	}
	profile, ok := a.profileID(w, r, 0)
	if !ok {
		return
	}
	app, err := a.Queries.GetApplication(r.Context(), store.GetApplicationParams{JobID: job, ProfileID: profile})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
		return
	}
	if err != nil {
		a.internal(w, r, err)
		return
	}
	evs, err := a.Queries.ListApplicationEvents(r.Context(), app.ID)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": nonNil(evs)})
}

// export writes one JSON line per application: the job as the matcher reads it, your outcome
// and when it happened. These are the labels for retraining (matcher ADR 0004). Personal data:
// it goes to the caller only and is never logged.
func (a *Applications) export(w http.ResponseWriter, r *http.Request) {
	profile, ok := a.profileID(w, r, 0)
	if !ok {
		return
	}
	rows, err := a.Queries.LabelRows(r.Context(), profile)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	var revision *int64
	if rev, err := a.Queries.GetLatestRevision(r.Context(), profile); err == nil {
		revision = &rev.ID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		a.internal(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="labels.jsonl"`)
	enc := json.NewEncoder(w)
	for _, row := range rows {
		_ = enc.Encode(map[string]any{
			"job_id": strconv.FormatInt(row.JobID, 10),
			"job": map[string]any{
				"title": row.Title, "company_name": row.CompanyName, "description_text": row.DescriptionText,
				"location_text": row.LocationText, "remote_policy": row.RemotePolicy,
				"countries": row.Countries, "employment_type": row.EmploymentType,
				"salary_min": row.SalaryMin, "salary_max": row.SalaryMax,
				"salary_currency": row.SalaryCurrency, "salary_period": row.SalaryPeriod,
			},
			"profile_revision_id": revision,
			"outcome":             row.Status,
			"occurred_at":         row.OccurredAt,
		})
	}
	a.Log.Info("labels exported", "request_id", RequestID(r.Context()), "profile_id", profile, "rows", len(rows))
}

func (a *Applications) internal(w http.ResponseWriter, r *http.Request, err error) {
	a.Log.Error("request failed", "request_id", RequestID(r.Context()), "error", err)
	WriteProblem(w, http.StatusInternalServerError, "internal error", "")
}
