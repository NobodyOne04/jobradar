package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NobodyOne04/jobradar/internal/metrics"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// DB is what a sync needs: queries plus transactions. Both *pgxpool.Pool and one acquired
// *pgxpool.Conn satisfy it.
type DB interface {
	store.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Syncer runs one source's sync: fetch, upsert each posting, close what disappeared.
type Syncer struct {
	Pool     *pgxpool.Pool
	Adapters Registry
	Log      *slog.Logger
}

// Counts is what one sync did; it is stored on the sync_runs row.
type Counts struct {
	Fetched, Created, Updated, Unchanged, Closed int32
}

// Sync runs one full sync of src on the pool. See SyncOn.
func (s *Syncer) Sync(ctx context.Context, src store.Source) (Counts, error) {
	return s.SyncOn(ctx, s.Pool, src)
}

// SyncOn runs one full sync of src using only db, and records it in sync_runs. Running it
// twice in a row creates and updates nothing the second time (ADR 0005).
func (s *Syncer) SyncOn(ctx context.Context, db DB, src store.Source) (Counts, error) {
	q := store.New(db)
	run, err := q.StartRun(ctx, src.ID)
	if err != nil {
		return Counts{}, fmt.Errorf("start run: %w", err)
	}
	log := s.Log.With("source_id", src.ID, "kind", src.Kind, "sync_run_id", run.ID)

	counts, partial, warning, err := s.sync(ctx, db, q, src, run, log)
	status := "succeeded"
	var errText *string
	switch {
	case err != nil:
		status = "failed"
		msg := err.Error()
		errText = &msg
	case partial != "":
		status = "partial"
		errText = &partial
	case warning != "":
		errText = &warning // still succeeded: a partial run would be retried every tick
	}
	// Record the outcome even if ctx was cancelled (shutdown): use a fresh context.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultDBTimeout)
	defer cancel()
	if ferr := q.FinishRun(finishCtx, store.FinishRunParams{
		ID: run.ID, Status: status, Fetched: counts.Fetched, Created: counts.Created,
		Updated: counts.Updated, Unchanged: counts.Unchanged, Closed: counts.Closed, Error: errText,
	}); ferr != nil {
		err = errors.Join(err, fmt.Errorf("finish run: %w", ferr))
	}
	metrics.SyncRuns.WithLabelValues(src.Kind, status).Inc()
	for result, n := range map[string]int32{
		"created": counts.Created, "updated": counts.Updated, "unchanged": counts.Unchanged, "closed": counts.Closed,
	} {
		metrics.Postings.WithLabelValues(src.Kind, result).Add(float64(n))
	}
	log.Info("sync finished", "status", status, "fetched", counts.Fetched, "created", counts.Created,
		"updated", counts.Updated, "unchanged", counts.Unchanged, "closed", counts.Closed)
	return counts, err
}

// sync does the work of SyncOn. partial is non-empty, and says why, when the run stored what
// it could but must not count as complete. warning is a note on a complete run.
func (s *Syncer) sync(ctx context.Context, db DB, q *store.Queries, src store.Source, run store.SyncRun,
	log *slog.Logger) (c Counts, partial, warning string, err error) {
	adapter, err := s.Adapters.Get(src.Kind)
	if err != nil {
		return c, "", "", err
	}
	postings, err := adapter.Fetch(ctx, src)
	truncated := errors.Is(err, ErrTruncated)
	if err != nil && !truncated {
		return c, "", "", fmt.Errorf("fetch: %w", err)
	}
	c.Fetched = int32(len(postings))

	failed := 0
	for _, raw := range postings {
		if ctx.Err() != nil {
			return c, "", "", ctx.Err()
		}
		if en, ok := adapter.(Enricher); ok {
			if raw, err = enrich(ctx, q, en, src, raw); err != nil {
				failed++
				log.Warn("posting not enriched", "external_id", raw.ExternalID, "error", err)
				continue
			}
		}
		result, err := s.upsert(ctx, db, adapter, src, raw)
		if err != nil {
			// One bad posting must not stop the others; the run ends 'partial'.
			failed++
			log.Warn("posting not stored", "external_id", raw.ExternalID, "error", err)
			continue
		}
		switch result {
		case created:
			c.Created++
		case updated:
			c.Updated++
		default:
			c.Unchanged++
		}
	}
	// Expired postings close on every run: expiry is the source's own word, not an inference.
	jobIDs, err := q.ClosePostingsExpired(ctx, src.ID)
	if err != nil {
		return c, "", "", fmt.Errorf("close expired postings: %w", err)
	}
	feed, isFeed := adapter.(FeedAdapter)
	if failed == 0 && (isFeed || !truncated) {
		// Only a complete, fully stored listing proves the others are gone. A feed is never
		// complete: its postings close once unseen for StaleAfter (ADR 0013).
		seenSince := run.StartedAt
		if isFeed {
			seenSince = run.StartedAt.Add(-feed.StaleAfter())
		}
		missing, err := q.ClosePostingsNotSeenSince(ctx, store.ClosePostingsNotSeenSinceParams{
			SourceID: src.ID, LastSeenAt: seenSince,
		})
		if err != nil {
			return c, "", "", fmt.Errorf("close missing postings: %w", err)
		}
		jobIDs = append(jobIDs, missing...)
	}
	switch {
	case failed > 0:
		partial = fmt.Sprintf("%d postings could not be stored, so none were closed as missing; see logs", failed)
	case truncated:
		partial = "listing truncated: only the first postings were fetched, so none were closed as missing; narrow the search"
	}
	if c.Fetched == 0 && !isFeed && partial == "" {
		// An unknown board can look like an empty one (SmartRecruiters answers 200): say so
		// on the first run, when a typo is most likely (ADR 0014).
		if runs, err := q.ListRuns(ctx, store.ListRunsParams{SourceID: src.ID, Limit: 2}); err == nil && len(runs) == 1 {
			warning = "no postings on the first sync: check board_token"
		}
	}
	c.Closed = int32(len(jobIDs))
	if len(jobIDs) > 0 {
		if _, err := q.CloseJobsWithoutOpenPostings(ctx, jobIDs); err != nil {
			return c, "", "", fmt.Errorf("close jobs: %w", err)
		}
	}
	return c, partial, warning, nil
}

type upsertResult int

const (
	unchanged upsertResult = iota
	created
	updated
)

// upsert stores one posting and its canonical job in one transaction.
func (s *Syncer) upsert(ctx context.Context, db DB, a Adapter, src store.Source, raw RawPosting) (upsertResult, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit
	q := store.New(tx)
	payloadHash := raw.ListHash
	if payloadHash == "" {
		payloadHash = PayloadHash(raw.Payload)
	}

	existing, err := q.GetPosting(ctx, store.GetPostingParams{SourceID: src.ID, ExternalID: raw.ExternalID})
	exists := err == nil // decided here: err is reused below
	switch {
	case exists && existing.PayloadHash == payloadHash:
		if err := q.TouchPosting(ctx, existing.ID); err != nil {
			return 0, err
		}
		if err := q.ReopenJob(ctx, existing.JobID); err != nil {
			return 0, err
		}
		return unchanged, tx.Commit(ctx)
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return 0, err
	}

	job, err := a.Normalize(raw)
	if err != nil {
		return 0, fmt.Errorf("normalize: %w", err)
	}
	if strings.TrimSpace(job.Title) == "" {
		return 0, errors.New("normalize: empty title")
	}
	if job.CompanyName == "" {
		job.CompanyName = sourceName(src)
	}
	text := HTMLToText(job.DescriptionHTML)
	companyKey := CompanyKey(job.CompanyName)
	dedupKey := DedupKey(companyKey, job.Title, job.LocationText)
	contentHash := ContentHash(job, job.CompanyName, text)
	if job.Countries == nil {
		job.Countries = []string{}
	}

	if exists { // its payload changed
		if err := q.UpdateJob(ctx, store.UpdateJobParams{
			ID: existing.JobID, Title: job.Title, CompanyName: job.CompanyName, CompanyKey: companyKey,
			DescriptionText: text, DescriptionHtml: job.DescriptionHTML, LocationText: job.LocationText,
			RemotePolicy: job.RemotePolicy, Countries: job.Countries, EmploymentType: job.EmploymentType,
			SalaryMin: job.SalaryMin, SalaryMax: job.SalaryMax, SalaryCurrency: job.SalaryCurrency,
			SalaryPeriod: job.SalaryPeriod, PostedAt: job.PostedAt, ContentHash: contentHash, DedupKey: dedupKey,
		}); err != nil {
			return 0, err
		}
		if err := q.UpdatePosting(ctx, store.UpdatePostingParams{
			ID: existing.ID, Url: raw.URL, RawPayload: raw.Payload, PayloadHash: payloadHash,
			ExpiresAt: raw.ExpiresAt,
		}); err != nil {
			return 0, err
		}
		return updated, tx.Commit(ctx)
	}

	// New posting: link it to an open job with the same dedup key, or create the job.
	jobID, err := q.FindOpenJobByDedupKey(ctx, dedupKey)
	if errors.Is(err, pgx.ErrNoRows) {
		jobID, err = q.InsertJob(ctx, store.InsertJobParams{
			Title: job.Title, CompanyName: job.CompanyName, CompanyKey: companyKey,
			DescriptionText: text, DescriptionHtml: job.DescriptionHTML, LocationText: job.LocationText,
			RemotePolicy: job.RemotePolicy, Countries: job.Countries, EmploymentType: job.EmploymentType,
			SalaryMin: job.SalaryMin, SalaryMax: job.SalaryMax, SalaryCurrency: job.SalaryCurrency,
			SalaryPeriod: job.SalaryPeriod, PostedAt: job.PostedAt, ContentHash: contentHash, DedupKey: dedupKey,
		})
	}
	if err != nil {
		return 0, err
	}
	if err := q.InsertPosting(ctx, store.InsertPostingParams{
		SourceID: src.ID, ExternalID: raw.ExternalID, JobID: jobID, Url: raw.URL,
		RawPayload: raw.Payload, PayloadHash: payloadHash, ExpiresAt: raw.ExpiresAt,
	}); err != nil {
		return 0, err
	}
	return created, tx.Commit(ctx)
}

func sourceName(src store.Source) string {
	if src.DisplayName != "" {
		return src.DisplayName
	}
	return src.BoardToken
}
