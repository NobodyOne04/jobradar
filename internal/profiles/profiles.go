// Package profiles stores candidate profiles as immutable, schema-validated revisions
// (ADR 0006). Profile content is personal: it is never logged, only ids and revisions are.
package profiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	apidocs "github.com/NobodyOne04/jobradar/docs/api"
	"github.com/NobodyOne04/jobradar/internal/store"
)

// SchemaVersion is the only profile schema this backend accepts.
const SchemaVersion = 1

// MaxDocumentBytes bounds one profile document.
const MaxDocumentBytes = 256 << 10

// NamePattern is the allowed profile name (also enforced by the database).
var NamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

var schema = func() *jsonschema.Schema {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(apidocs.ProfileSchemaV1))
	if err != nil {
		panic("profile schema is not JSON: " + err.Error())
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("profile.schema.json", doc); err != nil {
		panic(err)
	}
	return c.MustCompile("profile.schema.json")
}()

// InvalidError lists why a document does not match the schema: JSON paths and rules only.
type InvalidError struct{ Problems []string }

func (e *InvalidError) Error() string { return "invalid profile: " + strings.Join(e.Problems, "; ") }

// Validate checks doc against the schema and returns it unchanged if valid.
func Validate(doc []byte) error {
	if len(doc) > MaxDocumentBytes {
		return &InvalidError{[]string{fmt.Sprintf("document over %d bytes", MaxDocumentBytes)}}
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return &InvalidError{[]string{"not valid JSON"}}
	}
	err = schema.Validate(inst)
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		var problems []string
		collect(ve, &problems)
		return &InvalidError{problems}
	}
	return err
}

var english = message.NewPrinter(language.English)

// collect lists the leaves of the error tree: the library's flat output says only
// "validation failed" for rules reached through a $ref, while the leaves keep the reason.
func collect(ve *jsonschema.ValidationError, out *[]string) {
	if len(ve.Causes) > 0 {
		for _, c := range ve.Causes {
			collect(c, out)
		}
		return
	}
	*out = append(*out, "/"+strings.Join(ve.InstanceLocation, "/")+": "+ve.ErrorKind.LocalizedString(english))
}

// FromYAML converts a YAML profile file to JSON.
func FromYAML(src []byte) ([]byte, error) {
	var v any
	if err := yaml.Unmarshal(src, &v); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	return json.Marshal(v)
}

// Service creates profiles and revisions.
type Service struct{ Pool *pgxpool.Pool }

// Result is a stored revision, and whether this call created it.
type Result struct {
	Revision store.ProfileRevision
	Created  bool // false: the document equalled the latest revision, nothing was written
}

// AddRevision validates doc and stores it as the profile's next revision, unless it equals
// the latest one (then it returns that one): importing the same file twice is a no-op.
func (s *Service) AddRevision(ctx context.Context, profileID int64, doc []byte) (Result, error) {
	if err := Validate(doc); err != nil {
		return Result{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if _, err := q.LockProfile(ctx, profileID); err != nil {
		return Result{}, err // pgx.ErrNoRows: no such profile
	}

	next := int32(1)
	latest, err := q.LatestRevisionCompare(ctx, store.LatestRevisionCompareParams{ProfileID: profileID, Data: doc})
	switch {
	case err == nil && latest.Same:
		rev, err := q.GetRevision(ctx, store.GetRevisionParams{ProfileID: profileID, Revision: latest.Revision})
		return Result{Revision: rev}, err
	case err == nil:
		next = latest.Revision + 1
	case !errors.Is(err, pgx.ErrNoRows):
		return Result{}, err
	}
	rev, err := q.InsertRevision(ctx, store.InsertRevisionParams{
		ProfileID: profileID, Revision: next, SchemaVersion: SchemaVersion, Data: doc,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Revision: rev, Created: true}, tx.Commit(ctx)
}

// Import adds doc as a revision of the profile called name, creating the profile if needed.
func (s *Service) Import(ctx context.Context, name string, doc []byte) (Result, error) {
	if !NamePattern.MatchString(name) {
		return Result{}, fmt.Errorf("profile name %q: use lowercase letters, digits, - and _", name)
	}
	if err := Validate(doc); err != nil { // before creating an empty profile
		return Result{}, err
	}
	q := store.New(s.Pool)
	p, err := q.GetProfileByName(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		p, err = q.CreateProfile(ctx, name)
	}
	if err != nil {
		return Result{}, err
	}
	return s.AddRevision(ctx, p.ID, doc)
}
