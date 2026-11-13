// Package api is the public HTTP API (ADR 0002): routing, middleware, problem+json errors.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/NobodyOne04/jobradar/internal/metrics"
)

// Readiness reports whether the backend can serve (nil error) and, in info, the state of
// dependencies it works without (the matcher: matching pauses, the API does not).
type Readiness func(ctx context.Context) (info map[string]string, err error)

// NewHandler builds the router with all middleware applied. allowedHosts are the Host
// names the API answers to (see withLocalOnly); register adds resource routes.
func NewHandler(log *slog.Logger, ready Readiness, timeout time.Duration, allowedHosts []string,
	register ...func(*http.ServeMux)) http.Handler {
	mux := http.NewServeMux()
	for _, reg := range register {
		reg(mux)
	}
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		info, err := ready(r.Context())
		if err != nil {
			log.Warn("not ready", "request_id", RequestID(r.Context()), "reason", err.Error())
			WriteProblem(w, http.StatusServiceUnavailable, "not ready", err.Error())
			return
		}
		body := map[string]string{"status": "ready"}
		for k, v := range info {
			body[k] = v
		}
		writeJSON(w, http.StatusOK, body)
	})
	// Anything unmatched gets a problem+json 404 instead of ServeMux's plain text.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		WriteProblem(w, http.StatusNotFound, "not found", "")
	})

	return withRequestLog(log, withLocalOnly(allowedHosts, withTimeout(timeout, withMetrics(mux))))
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

// withMetrics records each request by its ServeMux pattern ("GET /api/v1/sources/{id}"), never
// by raw path, so label values stay bounded. It wraps the mux directly: the mux sets
// r.Pattern on the request it is given.
func withMetrics(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		mux.ServeHTTP(rec, r)
		route := r.Pattern
		if route == "" || route == "/" {
			route = "unmatched"
		}
		metrics.HTTPRequests.WithLabelValues(route, strconv.Itoa(rec.status)).Inc()
		metrics.HTTPDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}
