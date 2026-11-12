package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NobodyOne04/jobradar/internal/store"
)

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

// Sync runs one full sync of src and records it in sync_runs. Running it twice in a row
// creates and updates nothing the second time (ADR 0005).
func (s *Syncer) Sync(ctx context.Context, src store.Source) (Counts, error) {
	q := store.New(s.Pool)
	run, err := q.StartRun(ctx, src.ID)
	if err != nil {
		return Counts{}, fmt.Errorf("start run: %w", err)
	}
	log := s.Log.With("source_id", src.ID, "kind", src.Kind, "sync_run_id", run.ID)

	counts, failed, err := s.sync(ctx, q, src, run, log)
	status := "succeeded"
	var errText *string
	switch {
	case err != nil:
		status = "failed"
		msg := err.Error()
		errText = &msg
	case failed > 0:
		status = "partial"
		msg := fmt.Sprintf("%d postings could not be stored; see logs", failed)
		errText = &msg
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
	log.Info("sync finished", "status", status, "fetched", counts.Fetched, "created", counts.Created,
		"updated", counts.Updated, "unchanged", counts.Unchanged, "closed", counts.Closed)
	return counts, err
}

func (s *Syncer) sync(ctx context.Context, q *store.Queries, src store.Source, run store.SyncRun,
	log *slog.Logger) (Counts, int, error) {
	var c Counts
	adapter, err := s.Adapters.Get(src.Kind)
	if err != nil {
		return c, 0, err
	}
	postings, err := adapter.Fetch(ctx, src)
	if err != nil {
		return c, 0, fmt.Errorf("fetch: %w", err)
	}
	c.Fetched = int32(len(postings))

	failed := 0
	for _, raw := range postings {
		if ctx.Err() != nil {
			return c, failed, ctx.Err()
		}
		result, err := s.upsert(ctx, adapter, src, raw)
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
	if failed > 0 {
		// Unstored postings were not seen this run; closing "missing" ones would be wrong.
		return c, failed, nil
	}

	jobIDs, err := q.ClosePostingsNotSeenSince(ctx, store.ClosePostingsNotSeenSinceParams{
		SourceID: src.ID, LastSeenAt: run.StartedAt,
	})
	if err != nil {
		return c, failed, fmt.Errorf("close missing postings: %w", err)
	}
	c.Closed = int32(len(jobIDs))
	if len(jobIDs) > 0 {
		if _, err := q.CloseJobsWithoutOpenPostings(ctx, jobIDs); err != nil {
			return c, failed, fmt.Errorf("close jobs: %w", err)
		}
	}
	return c, failed, nil
}

type upsertResult int

const (
	unchanged upsertResult = iota
	created
	updated
)

// upsert stores one posting and its canonical job in one transaction.
func (s *Syncer) upsert(ctx context.Context, a Adapter, src store.Source, raw RawPosting) (upsertResult, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit
	q := store.New(tx)
	payloadHash := PayloadHash(raw.Payload)

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
		RawPayload: raw.Payload, PayloadHash: payloadHash,
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
