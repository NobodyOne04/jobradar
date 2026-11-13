package applications_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/NobodyOne04/jobradar/internal/applications"
	"github.com/NobodyOne04/jobradar/internal/store"
	"github.com/NobodyOne04/jobradar/internal/testdb"
)

// seed creates one profile and two jobs; returns their ids.
func seed(t *testing.T, st *store.Store) (profile, job1, job2 int64) {
	t.Helper()
	ctx := context.Background()
	q := store.New(st.Pool)
	p, err := q.CreateProfile(ctx, "anna")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 2)
	for i := range ids {
		ids[i], err = q.InsertJob(ctx, store.InsertJobParams{
			Title: "Data Engineer", CompanyName: "Acme", CompanyKey: "acme", RemotePolicy: "unknown",
			Countries: []string{}, EmploymentType: "unknown", ContentHash: "h", DedupKey: string(rune('a' + i)),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return p.ID, ids[0], ids[1]
}

func TestLifecycleAndEvents(t *testing.T) {
	st := testdb.Migrated(t)
	svc := &applications.Service{Pool: st.Pool}
	ctx := context.Background()
	profile, job, _ := seed(t, st)
	set := func(status, note string, force bool, at time.Time) (applications.Result, error) {
		return svc.Set(ctx, applications.Change{JobID: job, ProfileID: profile, Status: status, Note: note, Force: force, OccurredAt: at})
	}

	monday := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	if r, err := set("saved", "", false, time.Time{}); err != nil || r.Event == nil || r.Event.FromStatus != nil {
		t.Fatalf("first save: %+v %v", r, err)
	}
	if r, err := set("applied", "sent CV", false, monday); err != nil || r.Event.OccurredAt.UTC() != monday {
		t.Fatalf("backdated apply: %+v %v", r, err)
	}
	// Repeating without a note is a no-op; with a note it logs one.
	if r, err := set("applied", "", false, time.Time{}); err != nil || r.Event != nil {
		t.Fatalf("repeat: %+v %v", r, err)
	}
	if r, err := set("applied", "recruiter replied", false, time.Time{}); err != nil || r.Event == nil || r.Application.Notes != "recruiter replied" {
		t.Fatalf("repeat with note: %+v %v", r, err)
	}
	if _, err := set("saved", "", false, time.Time{}); !errors.Is(err, applications.ErrBackwards) {
		t.Fatalf("backwards without force: %v", err)
	}
	r, err := set("saved", "clicked wrong", true, time.Time{})
	if err != nil || !r.Event.Forced {
		t.Fatalf("forced correction: %+v %v", r, err)
	}
	if _, err := set("offer", "", false, time.Now().Add(time.Hour)); !errors.Is(err, applications.ErrInvalid) {
		t.Fatalf("future time: %v", err)
	}

	q := store.New(st.Pool)
	app, _ := q.GetApplication(ctx, store.GetApplicationParams{JobID: job, ProfileID: profile})
	evs, _ := q.ListApplicationEvents(ctx, app.ID)
	if app.Status != "saved" || len(evs) != 4 {
		t.Fatalf("status %s, %d events; want saved, 4", app.Status, len(evs))
	}
	if evs[0].OccurredAt.UTC() != monday { // ordered by occurred_at: the backdated one first
		t.Errorf("events not ordered by occurred_at: %v", evs[0].OccurredAt)
	}
}

func TestUnknownJob(t *testing.T) {
	st := testdb.Migrated(t)
	profile, _, _ := seed(t, st)
	_, err := (&applications.Service{Pool: st.Pool}).Set(context.Background(),
		applications.Change{JobID: 999, ProfileID: profile, Status: "saved"})
	if !errors.Is(err, applications.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
