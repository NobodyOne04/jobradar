package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/NobodyOne04/jobradar/internal/store"
)

// FeedAdapter is implemented by sources that list only recent jobs, such as remote-job
// boards (ADR 0013). Their fetch is never a complete listing, so a posting missing from it
// is not closed; it closes once no sync has seen it for StaleAfter.
type FeedAdapter interface {
	StaleAfter() time.Duration
}

// FeedStaleAfter is how long a feed posting stays open unseen (ADR 0013).
const FeedStaleAfter = 30 * 24 * time.Hour

// MinIntervaler is implemented by sources whose terms limit how often they may be called.
// No sync of such a source, scheduled or manual, starts sooner than MinInterval after the
// previous one started, whatever its outcome.
type MinIntervaler interface {
	MinInterval() time.Duration
}

// TooSoonError means a source's MinInterval has not passed since its last run.
type TooSoonError struct{ Wait time.Duration }

func (e *TooSoonError) Error() string {
	return fmt.Sprintf("this source may be synced again in %s (its terms limit requests)", e.Wait.Round(time.Minute))
}

// CheckMinInterval returns a *TooSoonError when src's adapter has a MinInterval that has not
// passed since src's last run started; nil otherwise.
func CheckMinInterval(ctx context.Context, db store.DBTX, adapters Registry, src store.Source, now time.Time) error {
	a, err := adapters.Get(src.Kind)
	if err != nil {
		return nil // the sync itself reports the missing adapter
	}
	m, ok := a.(MinIntervaler)
	if !ok {
		return nil
	}
	runs, err := store.New(db).ListRuns(ctx, store.ListRunsParams{SourceID: src.ID, Limit: 1})
	if err != nil {
		return fmt.Errorf("last run: %w", err)
	}
	if len(runs) == 0 {
		return nil
	}
	if wait := runs[0].StartedAt.Add(m.MinInterval()).Sub(now); wait > 0 {
		return &TooSoonError{Wait: wait}
	}
	return nil
}

// FeedParams builds a feed's query: board_token goes in tokenParam unless it is "all", and
// config may set only the named string filters. Adapters use it for Fetch and ValidateConfig.
func FeedParams(boardToken, tokenParam string, config json.RawMessage, filters ...string) (url.Values, error) {
	v := url.Values{}
	if t := strings.TrimSpace(boardToken); t != "" && !strings.EqualFold(t, "all") {
		v.Set(tokenParam, t)
	}
	if len(config) == 0 {
		return v, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, err
	}
	for k, raw := range cfg {
		if !slices.Contains(filters, k) {
			return nil, fmt.Errorf("unknown filter %q (allowed: %s)", k, strings.Join(filters, ", "))
		}
		s, ok := raw.(string)
		if !ok || s == "" || len(s) > 200 {
			return nil, fmt.Errorf("%s must be a non-empty string", k)
		}
		v.Set(k, s)
	}
	return v, nil
}
