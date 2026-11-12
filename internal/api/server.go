// Package api is the public HTTP API (ADR 0002): routing, middleware, problem+json errors.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// Readiness reports whether a dependency is ready; nil means ready.
type Readiness func(ctx context.Context) error

// NewHandler builds the router with all middleware applied. register adds resource routes.
func NewHandler(log *slog.Logger, ready Readiness, timeout time.Duration,
	register ...func(*http.ServeMux)) http.Handler {
	mux := http.NewServeMux()
	for _, reg := range register {
		reg(mux)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := ready(r.Context()); err != nil {
			log.Warn("not ready", "request_id", RequestID(r.Context()), "reason", err.Error())
			WriteProblem(w, http.StatusServiceUnavailable, "not ready", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	// Anything unmatched gets a problem+json 404 instead of ServeMux's plain text.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		WriteProblem(w, http.StatusNotFound, "not found", "")
	})

	return withRequestLog(log, withTimeout(timeout, mux))
}

// withTimeout gives each request a deadline. Handlers pass r.Context() to the database and
// HTTP clients, so slow work is cancelled; http.Server.WriteTimeout is the hard backstop.
func withTimeout(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
