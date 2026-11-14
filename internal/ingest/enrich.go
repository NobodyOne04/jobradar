package ingest

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/NobodyOne04/jobradar/internal/store"
)

// Enricher is implemented by sources whose listing lacks what Normalize needs (usually the
// description), so each posting needs its own request (ADR 0014). The sync calls Enrich only
// for postings whose list entry is new or changed since the last sync, so a steady board
// costs its list requests only.
type Enricher interface {
	// Enrich returns raw with the full posting as Payload (and URL, if it has a better one).
	Enrich(ctx context.Context, src store.Source, raw RawPosting) (RawPosting, error)
}

// enrich returns raw unchanged, apart from its ListHash, when its list entry matches what is
// stored; otherwise the enriched posting.
func enrich(ctx context.Context, q *store.Queries, en Enricher, src store.Source, raw RawPosting) (RawPosting, error) {
	listHash := PayloadHash(raw.Payload)
	existing, err := q.GetPosting(ctx, store.GetPostingParams{SourceID: src.ID, ExternalID: raw.ExternalID})
	switch {
	case err == nil && existing.PayloadHash == listHash:
		raw.ListHash = listHash
		return raw, nil // unchanged: the sync only touches it
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return raw, err
	}
	full, err := en.Enrich(ctx, src, raw)
	if err != nil {
		return raw, err
	}
	full.ListHash = listHash
	return full, nil
}
