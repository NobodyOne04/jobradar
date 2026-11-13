// Package metrics holds the backend's Prometheus metrics (ADR 0009), served at /metrics.
// Label values are bounded: routes are ServeMux patterns, never raw paths.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// HTTPRequests counts API requests by route pattern and status code.
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "jobradar_http_requests_total", Help: "API requests by route pattern and status code.",
	}, []string{"route", "code"})

	// HTTPDuration is API request latency by route pattern.
	HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "jobradar_http_request_duration_seconds", Help: "API request latency by route pattern.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})

	// SyncRuns counts finished source syncs by kind and outcome.
	SyncRuns = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "jobradar_sync_runs_total", Help: "Finished source syncs by source kind and status.",
	}, []string{"kind", "status"})

	// Postings counts postings handled by syncs, by kind and what happened to them.
	Postings = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "jobradar_postings_total",
		Help: "Postings handled by syncs, by source kind and result (created, updated, unchanged, closed).",
	}, []string{"kind", "result"})
)

var (
	// MatchingPending is the open jobs still without a result from the champion.
	MatchingPending = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "jobradar_matching_pending_jobs", Help: "Open jobs without a result from the champion model version.",
	})

	// MatchBatchDuration is how long one /v1/match batch took, retries included.
	MatchBatchDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name: "jobradar_match_batch_duration_seconds", Help: "Duration of one matcher batch, retries included.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	})

	// MatcherErrors counts failed batches (unavailable, rejected) and rejected jobs (invalid_job).
	MatcherErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "jobradar_matcher_errors_total", Help: "Matcher failures by reason: unavailable, rejected, invalid_job.",
	}, []string{"reason"})
)
