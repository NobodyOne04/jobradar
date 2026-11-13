package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/NobodyOne04/jobradar/internal/profiles"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// Profiles serves /api/v1/profiles (ADR 0006). Bodies hold personal data: never log them.
type Profiles struct {
	Queries *store.Queries
	Service *profiles.Service
	Log     *slog.Logger
}

// Register adds the routes to mux.
func (p *Profiles) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/profiles", p.list)
	mux.HandleFunc("POST /api/v1/profiles", p.create)
	mux.HandleFunc("POST /api/v1/profiles/{id}/revisions", p.addRevision)
	mux.HandleFunc("GET /api/v1/profiles/{id}/revisions/{rev}", p.getRevision)
}

func (p *Profiles) list(w http.ResponseWriter, r *http.Request) {
	rows, err := p.Queries.ListProfiles(r.Context())
	if err != nil {
		p.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": nonNil(rows)})
}

func (p *Profiles) create(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !profiles.NamePattern.MatchString(in.Name) {
		WriteProblem(w, http.StatusUnprocessableEntity, "invalid profile",
			"name: 1-64 lowercase letters, digits, - and _, starting with a letter or digit")
		return
	}
	row, err := p.Queries.CreateProfile(r.Context(), in.Name)
	if isUniqueViolation(err) {
		WriteProblem(w, http.StatusConflict, "profile exists", "")
		return
	}
	if err != nil {
		p.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

// addRevision takes the profile document itself as the body. 201: a new revision; 200: the
// document equals the latest revision, which is returned unchanged.
func (p *Profiles) addRevision(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	doc, err := io.ReadAll(http.MaxBytesReader(w, r.Body, profiles.MaxDocumentBytes))
	if err != nil {
		WriteProblem(w, http.StatusRequestEntityTooLarge, "profile too large", "")
		return
	}
	res, err := p.Service.AddRevision(r.Context(), id, doc)
	var invalid *profiles.InvalidError
	switch {
	case errors.As(err, &invalid):
		WriteProblem(w, http.StatusUnprocessableEntity, "invalid profile", strings.Join(invalid.Problems, "; "))
		return
	case errors.Is(err, pgx.ErrNoRows):
		WriteProblem(w, http.StatusNotFound, "profile not found", "")
		return
	case err != nil:
		p.internal(w, r, err)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	p.Log.Info("profile revision", "request_id", RequestID(r.Context()), "profile_id", id,
		"revision", res.Revision.Revision, "created", res.Created)
	writeJSON(w, status, res.Revision)
}

func (p *Profiles) getRevision(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var (
		rev store.ProfileRevision
		err error
	)
	if raw := r.PathValue("rev"); raw == "latest" {
		rev, err = p.Queries.GetLatestRevision(r.Context(), id)
	} else if n, perr := strconv.ParseInt(raw, 10, 32); perr == nil && n > 0 {
		rev, err = p.Queries.GetRevision(r.Context(), store.GetRevisionParams{ProfileID: id, Revision: int32(n)})
	} else {
		WriteProblem(w, http.StatusNotFound, "revision not found", "")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		WriteProblem(w, http.StatusNotFound, "revision not found", "")
		return
	}
	if err != nil {
		p.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rev)
}

func (p *Profiles) internal(w http.ResponseWriter, r *http.Request, err error) {
	p.Log.Error("request failed", "request_id", RequestID(r.Context()), "error", err)
	WriteProblem(w, http.StatusInternalServerError, "internal error", "")
}
