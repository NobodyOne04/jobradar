// Package applications tracks your applications (ADR 0008): one status per job and profile,
// and an append-only event log with when each change happened. No row means "discovered".
package applications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NobodyOne04/jobradar/internal/store"
)

// Discovered is the status of a job with no application row; it is never stored.
const Discovered = "discovered"

// forward is the pipeline order; a move to a later stage may skip stages.
var forward = map[string]int{"saved": 1, "applied": 2, "interview": 3, "offer": 4}

// terminal statuses can be reached from anywhere; leaving them is a correction.
var terminal = map[string]bool{"rejected": true, "archived": true}

// Valid reports whether s is a status that can be set.
func Valid(s string) bool { return forward[s] > 0 || terminal[s] }

// ErrInvalid means the request itself is wrong (unknown status, a time in the future).
var ErrInvalid = errors.New("invalid change")

// ErrBackwards means the move needs force: true (a correction).
var ErrBackwards = errors.New("moving backwards needs force")

// Transition says whether from -> to is allowed. from is Discovered for a job without a row.
// Forward moves and moves to rejected or archived are free; anything else is a correction and
// needs force. The same status again is allowed (it may carry a note).
func Transition(from, to string, force bool) error {
	if !Valid(to) {
		return fmt.Errorf("%w: unknown status %q", ErrInvalid, to)
	}
	if from != Discovered && !Valid(from) {
		return fmt.Errorf("%w: unknown status %q", ErrInvalid, from)
	}
	switch {
	case from == to, terminal[to], from == Discovered:
		return nil
	case !terminal[from] && forward[to] > forward[from]:
		return nil
	case force:
		return nil
	default:
		return fmt.Errorf("%s -> %s: %w", from, to, ErrBackwards)
	}
}

// Change is one requested status update.
type Change struct {
	JobID      int64
	ProfileID  int64
	Status     string
	Note       string
	OccurredAt time.Time // zero: now
	Force      bool
}

// Result is what Set did.
type Result struct {
	Application store.Application
	Event       *store.ApplicationEvent // nil: nothing changed (same status, no note)
}

// ErrNotFound means the job does not exist.
var ErrNotFound = errors.New("job not found")

// Service applies changes, each in one transaction with its event.
type Service struct{ Pool *pgxpool.Pool }

// Set moves a job's application to c.Status and logs it. Setting the current status again
// without a note changes nothing, so the call is safe to repeat.
func (s *Service) Set(ctx context.Context, c Change) (Result, error) {
	now := time.Now().UTC()
	if c.OccurredAt.IsZero() {
		c.OccurredAt = now
	}
	if c.OccurredAt.After(now.Add(5 * time.Minute)) {
		return Result{}, fmt.Errorf("%w: occurred_at is in the future", ErrInvalid)
	}
	note := strings.TrimSpace(c.Note)

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if ok, err := q.JobExists(ctx, c.JobID); err != nil {
		return Result{}, err
	} else if !ok {
		return Result{}, ErrNotFound
	}

	app, err := q.GetApplicationForUpdate(ctx, store.GetApplicationForUpdateParams{JobID: c.JobID, ProfileID: c.ProfileID})
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}
	from := Discovered
	if exists {
		from = app.Status
	}
	if err := Transition(from, c.Status, c.Force); err != nil {
		return Result{}, err
	}
	if exists && from == c.Status && note == "" {
		return Result{Application: app}, tx.Commit(ctx)
	}

	notes := note
	if exists && note == "" {
		notes = app.Notes // keep the last note when a change comes without one
	}
	if exists {
		app, err = q.UpdateApplication(ctx, store.UpdateApplicationParams{ID: app.ID, Status: c.Status, Notes: notes})
	} else {
		app, err = q.InsertApplication(ctx, store.InsertApplicationParams{
			JobID: c.JobID, ProfileID: c.ProfileID, Status: c.Status, Notes: notes,
		})
	}
	if err != nil {
		return Result{}, err
	}
	var fromPtr, notePtr *string
	if exists {
		fromPtr = &from
	}
	if note != "" {
		notePtr = &note
	}
	ev, err := q.InsertApplicationEvent(ctx, store.InsertApplicationEventParams{
		ApplicationID: app.ID, FromStatus: fromPtr, ToStatus: c.Status, Note: notePtr,
		Forced: c.Force && Transition(from, c.Status, false) != nil, OccurredAt: c.OccurredAt.UTC(),
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Application: app, Event: &ev}, tx.Commit(ctx)
}
