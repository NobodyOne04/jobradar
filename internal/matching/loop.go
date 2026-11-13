package matching

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NobodyOne04/jobradar/internal/matcher"
	"github.com/NobodyOne04/jobradar/internal/metrics"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// Current is the champion the backend works with: the latest activation it recorded.
type Current struct {
	ModelVersionID int64
	Version        string
}

// Loop follows the champion and keeps match results current.
type Loop struct {
	Pool     *pgxpool.Pool
	Registry *Registry
	Matcher  *matcher.Client
	Pinned   string // contracts.MatcherVersion
	Log      *slog.Logger

	FollowEvery time.Duration // champion poll, ADR 0010: 60 s
	MatchEvery  time.Duration // pending-work check: 30 s
	MaxBatches  int           // per profile and tick, so a big backfill does not block shutdown

	mu          sync.Mutex
	current     Current
	mlflowOK    bool
	failures    int                       // consecutive failed batches
	pausedUntil time.Time                 // the circuit breaker (ADR 0004): 5 failures, 1 minute
	skip        map[string]map[int64]bool // model version -> job ids the matcher rejected
}

// State is what /readyz reports.
func (l *Loop) State() map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	champion := l.current.Version
	if champion == "" {
		champion = "none"
	}
	mlflow := "ok"
	if !l.mlflowOK {
		mlflow = "unreachable"
	}
	return map[string]string{"champion": champion, "mlflow": mlflow}
}

// Run follows and matches until ctx is cancelled.
func (l *Loop) Run(ctx context.Context) {
	l.Follow(ctx)
	follow := time.NewTicker(l.FollowEvery)
	match := time.NewTicker(l.MatchEvery)
	defer follow.Stop()
	defer match.Stop()
	for {
		if err := l.MatchPending(ctx); err != nil && ctx.Err() == nil {
			l.Log.Warn("matching", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-follow.C:
			l.Follow(ctx)
		case <-match.C:
			// Until a champion is known (fresh install, or MLflow still starting), look on every
			// tick instead of once a minute.
			if l.State()["champion"] == "none" {
				l.Follow(ctx)
			}
		}
	}
}

// Follow reads the champion from MLflow and records a change. When MLflow is down or has no
// champion, the last recorded one stays in use: the matcher has those versions cached.
func (l *Loop) Follow(ctx context.Context) {
	q := store.New(l.Pool)
	last, err := q.LatestActivation(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		l.Log.Error("read latest activation", "error", err)
		return
	}
	known := Current{ModelVersionID: last.ModelVersionID, Version: last.Version}

	champ, err := l.Registry.Champion(ctx)
	l.mu.Lock()
	l.mlflowOK = err == nil || errors.Is(err, ErrNoChampion)
	l.current = known
	l.mu.Unlock()
	switch {
	case errors.Is(err, ErrNoChampion):
		if known.Version == "" {
			l.Log.Info("no champion in MLflow yet: matching waits",
				"hint", "matcher train --from-defaults, then matcher promote 1")
		} else {
			l.Log.Warn("champion alias gone from MLflow; keeping the last one", "version", known.Version)
		}
		return
	case err != nil:
		l.Log.Warn("MLflow unreachable; keeping the last champion", "version", known.Version, "error", err)
		return
	case champ.Version == known.Version:
		return
	}

	cur, err := l.activate(ctx, champ, known)
	if err != nil {
		l.Log.Error("record champion", "version", champ.Version, "error", err)
		return
	}
	l.mu.Lock()
	l.current = cur
	l.mu.Unlock()
	l.Log.Info("champion changed", "from", known.Version, "to", champ.Version,
		"reason", champ.Tags["promoted_reason"], "gate", champ.Tags["gate"])
}

func (l *Loop) activate(ctx context.Context, champ Champion, previous Current) (Current, error) {
	tx, err := l.Pool.Begin(ctx)
	if err != nil {
		return Current{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	metricsJSON, _ := json.Marshal(champ.Metrics)
	tagsJSON, _ := json.Marshal(champ.Tags)
	var matcherVersion *string
	if champ.MatcherVersion != "" {
		matcherVersion = &champ.MatcherVersion
	}
	mv, err := q.UpsertModelVersion(ctx, store.UpsertModelVersionParams{
		Version: champ.Version, MlflowRunID: champ.RunID, MatcherVersion: matcherVersion,
		Metrics: metricsJSON, Tags: tagsJSON,
	})
	if err != nil {
		return Current{}, err
	}
	var prev *int64
	if previous.ModelVersionID != 0 {
		prev = &previous.ModelVersionID
	}
	optional := func(k string) *string {
		if v, ok := champ.Tags[k]; ok {
			return &v
		}
		return nil // the alias was moved by hand in the MLflow UI: no gate, no reason
	}
	if _, err := q.InsertActivation(ctx, store.InsertActivationParams{
		ModelVersionID: mv.ID, PreviousID: prev, Reason: optional("promoted_reason"), Gate: optional("gate"),
	}); err != nil {
		return Current{}, err
	}
	return Current{ModelVersionID: mv.ID, Version: mv.Version}, tx.Commit(ctx)
}

// MatchPending sends pending jobs of every profile's current revision to the matcher.
func (l *Loop) MatchPending(ctx context.Context) error {
	l.mu.Lock()
	cur, paused := l.current, time.Now().Before(l.pausedUntil)
	l.mu.Unlock()
	if cur.Version == "" || paused {
		return nil
	}
	if status, detail := l.Matcher.Status(ctx, l.Pinned); status == "incompatible" {
		metrics.MatcherErrors.WithLabelValues("incompatible").Inc()
		l.Log.Warn("matching skipped: incompatible matcher", "detail", detail)
		return nil
	}
	q := store.New(l.Pool)
	revisions, err := q.LatestRevisions(ctx)
	if err != nil {
		return err
	}
	var pending int64
	for _, rev := range revisions {
		for range l.MaxBatches {
			done, err := l.batch(ctx, q, rev, cur)
			if err != nil {
				return err
			}
			if done {
				break
			}
		}
		n, err := q.CountPending(ctx, store.CountPendingParams{ProfileRevisionID: rev.ID, ModelVersionID: cur.ModelVersionID})
		if err != nil {
			return err
		}
		pending += n
	}
	metrics.MatchingPending.Set(float64(pending))
	return nil
}

// batch matches up to one batch; done means nothing is left for this revision.
func (l *Loop) batch(ctx context.Context, q *store.Queries, rev store.LatestRevisionsRow, cur Current) (bool, error) {
	l.mu.Lock()
	if l.skip == nil {
		l.skip = map[string]map[int64]bool{}
	}
	skipSet := l.skip[cur.Version]
	l.mu.Unlock()
	skip := make([]int64, 0, len(skipSet))
	for id := range skipSet {
		skip = append(skip, id)
	}
	jobs, err := q.PendingJobs(ctx, store.PendingJobsParams{
		Skip: skip, RevisionID: rev.ID, ModelVersionID: cur.ModelVersionID, Lim: matcher.MaxBatch,
	})
	if err != nil || len(jobs) == 0 {
		return true, err
	}
	req := matcher.MatchRequest{ModelVersion: cur.Version, Profile: rev.Data}
	hashes := map[string]string{}
	for _, j := range jobs {
		id := strconv.FormatInt(j.ID, 10)
		hashes[id] = j.ContentHash
		req.Jobs = append(req.Jobs, matcher.Job{
			JobID: id, Title: j.Title, CompanyName: j.CompanyName, DescriptionText: j.DescriptionText,
			LocationText: j.LocationText, RemotePolicy: j.RemotePolicy, Countries: j.Countries,
			EmploymentType: j.EmploymentType, SalaryMin: j.SalaryMin, SalaryMax: j.SalaryMax,
			SalaryCurrency: j.SalaryCurrency, SalaryPeriod: j.SalaryPeriod, ContentHash: j.ContentHash,
		})
	}

	start := time.Now()
	resp, err := l.Matcher.Match(ctx, "", req)
	elapsed := time.Since(start)
	metrics.MatchBatchDuration.Observe(elapsed.Seconds())
	if err != nil {
		l.failed(err)
		return true, nil // the breaker decides when to try again; ingestion is unaffected
	}
	l.mu.Lock()
	l.failures = 0
	l.mu.Unlock()

	perJob := int32(elapsed.Milliseconds() / int64(len(jobs)))
	for _, r := range resp.Results {
		jobID, err := strconv.ParseInt(r.JobID, 10, 64)
		if err != nil {
			continue
		}
		if r.Error != "" {
			// The matcher rejects this job's data: retrying every tick would not help.
			metrics.MatcherErrors.WithLabelValues("invalid_job").Inc()
			l.mu.Lock()
			if l.skip[cur.Version] == nil {
				l.skip[cur.Version] = map[int64]bool{}
			}
			l.skip[cur.Version][jobID] = true
			l.mu.Unlock()
			l.Log.Warn("matcher rejected a job", "job_id", jobID, "model_version", cur.Version)
			continue
		}
		if err := q.InsertMatchResult(ctx, store.InsertMatchResultParams{
			JobID: jobID, ProfileRevisionID: rev.ID, ModelVersionID: cur.ModelVersionID,
			JobContentHash: hashes[r.JobID], Score: int16(r.Score), Band: r.Band, Confidence: r.Confidence,
			Completeness: float32(r.Completeness), HardConstraints: r.HardConstraints, Details: r.Raw,
			LatencyMs: &perJob,
		}); err != nil {
			return true, err
		}
	}
	l.Log.Info("matched", "profile_revision_id", rev.ID, "model_version", cur.Version,
		"jobs", len(jobs), "duration_ms", elapsed.Milliseconds())
	return len(jobs) < matcher.MaxBatch, nil
}

func (l *Loop) failed(err error) {
	reason := "unavailable"
	var apiErr *matcher.APIError
	if errors.As(err, &apiErr) && !apiErr.Retryable() {
		reason = "rejected"
	}
	metrics.MatcherErrors.WithLabelValues(reason).Inc()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures++
	if l.failures >= 5 {
		l.pausedUntil = time.Now().Add(time.Minute)
		l.failures = 0
		l.Log.Warn("matching paused for a minute after 5 failed batches", "error", err)
		return
	}
	l.Log.Warn("match batch failed", "error", err, "consecutive", l.failures)
}
