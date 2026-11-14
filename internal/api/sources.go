package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/store"
)

const maxRequestBody = 64 << 10

// Sources serves /api/v1/sources (ADR 0005): the list of boards and searches to sync.
type Sources struct {
	Queries   *store.Queries
	Scheduler *ingest.Scheduler
	Log       *slog.Logger
	// Background is the server's lifetime context; manual syncs outlive their request.
	Background context.Context
	wg         sync.WaitGroup
}

// Register adds the routes to mux.
func (s *Sources) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/sources", s.list)
	mux.HandleFunc("POST /api/v1/sources", s.create)
	mux.HandleFunc("PATCH /api/v1/sources/{id}", s.update)
	mux.HandleFunc("POST /api/v1/sources/{id}/sync", s.sync)
	mux.HandleFunc("GET /api/v1/sources/{id}/runs", s.runs)
}

// Wait blocks until manual syncs started through the API have finished.
func (s *Sources) Wait() { s.wg.Wait() }

func (s *Sources) list(w http.ResponseWriter, r *http.Request) {
	sources, err := s.Queries.ListSources(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": nonNil(sources)})
}

type createSource struct {
	Kind        string          `json:"kind"`
	BoardToken  string          `json:"board_token"`
	DisplayName string          `json:"display_name"`
	Config      json.RawMessage `json:"config"`
}

func (s *Sources) create(w http.ResponseWriter, r *http.Request) {
	var in createSource
	if !decode(w, r, &in) {
		return
	}
	in.BoardToken = strings.TrimSpace(in.BoardToken)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	var problems []string
	adapter, err := s.Scheduler.Syncer.Adapters.Get(in.Kind)
	if err != nil {
		problems = append(problems, fmt.Sprintf("kind %q is not supported", in.Kind))
	}
	if msg := checkText("board_token", in.BoardToken, 1, 200); msg != "" {
		problems = append(problems, msg)
	}
	if msg := checkText("display_name", in.DisplayName, 0, 200); msg != "" {
		problems = append(problems, msg)
	}
	if len(in.Config) == 0 {
		in.Config = json.RawMessage(`{}`)
	} else if !isJSONObject(in.Config) {
		problems = append(problems, "config must be a JSON object")
	} else if v, ok := adapter.(ingest.ConfigValidator); ok {
		if err := v.ValidateConfig(in.Config); err != nil {
			problems = append(problems, "config: "+err.Error())
		}
	}
	if len(problems) > 0 {
		WriteProblem(w, http.StatusUnprocessableEntity, "invalid source", strings.Join(problems, "; "))
		return
	}

	src, err := s.Queries.CreateSource(r.Context(), store.CreateSourceParams{
		Kind: in.Kind, BoardToken: in.BoardToken, DisplayName: in.DisplayName, Config: in.Config,
	})
	if isUniqueViolation(err) {
		WriteProblem(w, http.StatusConflict, "source exists", "this kind and board_token are already added")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, src)
}

type updateSource struct {
	Enabled     *bool   `json:"enabled"`
	DisplayName *string `json:"display_name"`
}

func (s *Sources) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in updateSource
	if !decode(w, r, &in) {
		return
	}
	if in.DisplayName != nil {
		trimmed := strings.TrimSpace(*in.DisplayName)
		if msg := checkText("display_name", trimmed, 0, 200); msg != "" {
			WriteProblem(w, http.StatusUnprocessableEntity, "invalid source", msg)
			return
		}
		in.DisplayName = &trimmed
	}
	src, err := s.Queries.UpdateSource(r.Context(), store.UpdateSourceParams{
		ID: id, Enabled: in.Enabled, DisplayName: in.DisplayName,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		WriteProblem(w, http.StatusNotFound, "source not found", "")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, src)
}

func (s *Sources) sync(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	src, err := s.Queries.GetSource(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		WriteProblem(w, http.StatusNotFound, "source not found", "")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// Checked here as well as in SyncLocked, so the caller hears about it (ADR 0013).
	err = ingest.CheckMinInterval(r.Context(), s.Scheduler.Syncer.Pool, s.Scheduler.Syncer.Adapters, src, time.Now())
	var tooSoon *ingest.TooSoonError
	if errors.As(err, &tooSoon) {
		w.Header().Set("Retry-After", strconv.Itoa(int(tooSoon.Wait.Seconds())+1))
		WriteProblem(w, http.StatusTooManyRequests, "sync too soon", tooSoon.Error())
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	requestID := RequestID(r.Context())
	s.wg.Go(func() {
		err := s.Scheduler.SyncLocked(s.Background, src)
		switch {
		case errors.Is(err, ingest.ErrSyncRunning):
			s.Log.Info("manual sync skipped: already running", "request_id", requestID, "source_id", src.ID)
		case err != nil:
			s.Log.Warn("manual sync failed", "request_id", requestID, "source_id", src.ID, "error", err)
		}
	})
	// The sync runs in the background; GET .../runs shows its outcome.
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (s *Sources) runs(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit := int32(20)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			WriteProblem(w, http.StatusUnprocessableEntity, "invalid limit", "limit must be 1-100")
			return
		}
		limit = int32(n)
	}
	runs, err := s.Queries.ListRuns(r.Context(), store.ListRunsParams{SourceID: id, Limit: limit})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": nonNil(runs)})
}

func (s *Sources) internal(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "request_id", RequestID(r.Context()), "error", err)
	WriteProblem(w, http.StatusInternalServerError, "internal error", "")
}

// decode reads a small JSON body strictly: unknown fields and trailing data are errors.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		WriteProblem(w, http.StatusBadRequest, "invalid JSON body", "expected one JSON object with known fields")
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		WriteProblem(w, http.StatusNotFound, "source not found", "")
		return 0, false
	}
	return id, true
}

// checkText bounds a length in characters and rejects control characters.
func checkText(field, v string, minLen, maxLen int) string {
	n := len([]rune(v))
	if n < minLen || n > maxLen {
		return fmt.Sprintf("%s must be %d-%d characters", field, minLen, maxLen)
	}
	if strings.ContainsFunc(v, unicode.IsControl) {
		return field + " must not contain control characters"
	}
	return ""
}

// nonNil makes an empty result encode as [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func isJSONObject(raw json.RawMessage) bool {
	var m map[string]any
	return json.Unmarshal(raw, &m) == nil && m != nil
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
