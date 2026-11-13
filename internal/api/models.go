package api

import (
	"log/slog"
	"net/http"

	"github.com/NobodyOne04/jobradar/internal/store"
)

// Models serves /api/v1/models (ADR 0010): the model versions the backend has seen and every
// change of champion it observed. Promotion and rollback happen in MLflow, not here.
type Models struct {
	Queries *store.Queries
	Log     *slog.Logger
}

// Register adds the routes to mux.
func (m *Models) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/models", m.list)
	mux.HandleFunc("GET /api/v1/models/activations", m.activations)
}

func (m *Models) list(w http.ResponseWriter, r *http.Request) {
	rows, err := m.Queries.ListModelVersions(r.Context())
	if err != nil {
		m.Log.Error("request failed", "request_id", RequestID(r.Context()), "error", err)
		WriteProblem(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": nonNil(rows)})
}

func (m *Models) activations(w http.ResponseWriter, r *http.Request) {
	rows, err := m.Queries.ListActivations(r.Context(), 100)
	if err != nil {
		m.Log.Error("request failed", "request_id", RequestID(r.Context()), "error", err)
		WriteProblem(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"activations": nonNil(rows)})
}
