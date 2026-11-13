// Package matching keeps match results current (ADR 0010): it follows the `champion` alias in
// MLflow, records every change it sees, and sends pending jobs to the matcher in batches.
package matching

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ModelName is the registered model in MLflow (matcher ADR 0004).
const ModelName = "jobradar-matcher"

// ErrNoChampion means MLflow has no `champion` yet: no registered model (404) or no alias (400).
var ErrNoChampion = errors.New("no champion model version in MLflow")

// Champion is the model version behind the `champion` alias, with what MLflow knows about it.
type Champion struct {
	Version        string
	RunID          string
	Tags           map[string]string  // promoted_reason, gate, ...
	Metrics        map[string]float64 // the eval metrics of its training run
	MatcherVersion string             // the run's matcher_version param
}

// Registry reads MLflow's REST API (checked against MLflow 3.18, recorded in testdata/).
type Registry struct {
	BaseURL string
	HTTP    *http.Client
}

// NewRegistry returns a client with a short timeout: the follower polls, it can wait a minute.
func NewRegistry(baseURL string) *Registry {
	return &Registry{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 10 * time.Second}}
}

type mlflowError struct {
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
}

func (r *Registry) get(ctx context.Context, path string, query url.Values, into any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.BaseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return 0, err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		var e mlflowError
		_ = json.Unmarshal(body, &e)
		return resp.StatusCode, fmt.Errorf("mlflow %s: HTTP %d %s: %s", path, resp.StatusCode, e.ErrorCode, e.Message)
	}
	return resp.StatusCode, json.Unmarshal(body, into)
}

type kv struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Champion returns the champion version, ErrNoChampion, or an error when MLflow is unreachable.
func (r *Registry) Champion(ctx context.Context) (Champion, error) {
	var alias struct {
		ModelVersion struct {
			Version string `json:"version"`
			RunID   string `json:"run_id"`
			Tags    []kv   `json:"tags"`
		} `json:"model_version"`
	}
	status, err := r.get(ctx, "/api/2.0/mlflow/registered-models/alias",
		url.Values{"name": {ModelName}, "alias": {"champion"}}, &alias)
	if status == http.StatusNotFound || (status == http.StatusBadRequest && strings.Contains(err.Error(), "alias")) {
		return Champion{}, ErrNoChampion
	}
	if err != nil {
		return Champion{}, err
	}
	c := Champion{Version: alias.ModelVersion.Version, RunID: alias.ModelVersion.RunID,
		Tags: map[string]string{}, Metrics: map[string]float64{}}
	for _, t := range alias.ModelVersion.Tags {
		c.Tags[t.Key] = t.Value
	}
	if c.RunID == "" {
		return c, nil
	}
	var run struct {
		Run struct {
			Data struct {
				Metrics []struct {
					Key   string  `json:"key"`
					Value float64 `json:"value"`
				} `json:"metrics"`
				Params []kv `json:"params"`
			} `json:"data"`
		} `json:"run"`
	}
	if _, err := r.get(ctx, "/api/2.0/mlflow/runs/get", url.Values{"run_id": {c.RunID}}, &run); err != nil {
		return Champion{}, err
	}
	for _, m := range run.Run.Data.Metrics {
		c.Metrics[m.Key] = m.Value
	}
	for _, p := range run.Run.Data.Params {
		if p.Key == "matcher_version" {
			c.MatcherVersion = p.Value
		}
	}
	return c, nil
}
