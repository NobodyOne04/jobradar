package profiles_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/NobodyOne04/jobradar/internal/profiles"
	"github.com/NobodyOne04/jobradar/internal/testdb"
)

func exampleJSON(t *testing.T) []byte {
	t.Helper()
	src, err := os.ReadFile("../../profiles/example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := profiles.FromYAML(src)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestImportIsIdempotentAndNumbersRevisions(t *testing.T) {
	st := testdb.Migrated(t)
	svc := &profiles.Service{Pool: st.Pool}
	ctx := context.Background()
	doc := exampleJSON(t)

	first, err := svc.Import(ctx, "anna", doc)
	if err != nil || !first.Created || first.Revision.Revision != 1 || first.Revision.SchemaVersion != 2 {
		t.Fatalf("first import: %+v, %v", first, err)
	}

	// Same content with keys in another order and different whitespace: still unchanged.
	var m map[string]any
	_ = json.Unmarshal(doc, &m)
	reordered, _ := json.MarshalIndent(m, "", "    ")
	again, err := svc.Import(ctx, "anna", reordered)
	if err != nil || again.Created || again.Revision.Revision != 1 {
		t.Fatalf("re-import: %+v, %v", again, err)
	}

	m["years_of_experience"] = 9
	changed, _ := json.Marshal(m)
	second, err := svc.Import(ctx, "anna", changed)
	if err != nil || !second.Created || second.Revision.Revision != 2 {
		t.Fatalf("changed import: %+v, %v", second, err)
	}

	// A v1 document is still accepted, and its revision records version 1 (ADR 0015).
	m["schema_version"] = 1
	delete(m, "target_roles")
	delete(m, "languages")
	prefs := m["preferences"].(map[string]any)
	delete(prefs, "remote_regions")
	delete(prefs, "relocation")
	prefs["hard_constraints"] = []any{"salary"}
	v1, _ := json.Marshal(m)
	third, err := svc.Import(ctx, "anna", v1)
	if err != nil || third.Revision.Revision != 3 || third.Revision.SchemaVersion != 1 {
		t.Fatalf("v1 import: %+v, %v", third, err)
	}
}

func TestInvalidImportCreatesNothing(t *testing.T) {
	st := testdb.Migrated(t)
	svc := &profiles.Service{Pool: st.Pool}
	_, err := svc.Import(context.Background(), "anna", []byte(`{"schema_version":1}`))
	var inv *profiles.InvalidError
	if !errors.As(err, &inv) {
		t.Fatalf("got %v", err)
	}
	var n int
	_ = st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM candidate_profiles`).Scan(&n)
	if n != 0 {
		t.Errorf("a profile was created for an invalid document")
	}
	if _, err := svc.Import(context.Background(), "Anna Smith", exampleJSON(t)); err == nil {
		t.Error("bad profile name accepted")
	}
}

func TestConcurrentRevisionsGetDistinctNumbers(t *testing.T) {
	st := testdb.Migrated(t)
	svc := &profiles.Service{Pool: st.Pool}
	ctx := context.Background()
	if _, err := svc.Import(ctx, "anna", exampleJSON(t)); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(exampleJSON(t), &m)
	var wg sync.WaitGroup
	for i := range 5 {
		m2 := map[string]any{}
		for k, v := range m {
			m2[k] = v
		}
		m2["years_of_experience"] = 10 + i
		doc, _ := json.Marshal(m2)
		wg.Go(func() {
			if _, err := svc.AddRevision(ctx, 1, doc); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var n, maxRev int
	_ = st.Pool.QueryRow(ctx, `SELECT count(*), max(revision) FROM profile_revisions`).Scan(&n, &maxRev)
	if n != 6 || maxRev != 6 {
		t.Errorf("revisions: %d rows, max %d; want 6 and 6", n, maxRev)
	}
}
