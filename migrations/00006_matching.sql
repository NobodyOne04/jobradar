-- Matching (ADR 0010): model versions seen in MLflow, every observed change of the champion,
-- and match results kept per (job, profile revision, model version, job content).

-- +goose Up
CREATE TABLE model_versions (
    id              bigserial PRIMARY KEY,
    version         text        NOT NULL UNIQUE,  -- MLflow's version number, as text
    mlflow_run_id   text        NOT NULL,
    matcher_version text,
    metrics         jsonb       NOT NULL DEFAULT '{}',
    tags            jsonb       NOT NULL DEFAULT '{}',
    first_seen_at   timestamptz NOT NULL DEFAULT now()
);

-- MLflow keeps no history of alias moves: this is the audit trail of what the backend saw.
CREATE TABLE model_activations (
    id               bigserial PRIMARY KEY,
    model_version_id bigint      NOT NULL REFERENCES model_versions (id),
    previous_id      bigint      REFERENCES model_versions (id),
    reason           text,
    gate             text,
    observed_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE match_results (
    id                  bigserial PRIMARY KEY,
    job_id              bigint      NOT NULL REFERENCES jobs (id),
    profile_revision_id bigint      NOT NULL REFERENCES profile_revisions (id),
    model_version_id    bigint      NOT NULL REFERENCES model_versions (id),
    job_content_hash    text        NOT NULL,
    score               smallint    NOT NULL CHECK (score BETWEEN 0 AND 100),
    band                text        NOT NULL CHECK (band IN ('strong', 'good', 'weak', 'excluded')),
    confidence          text        NOT NULL CHECK (confidence IN ('high', 'medium', 'low')),
    completeness        real        NOT NULL,
    hard_constraints    text        NOT NULL CHECK (hard_constraints IN ('pass', 'fail', 'unknown')),
    details             jsonb       NOT NULL,
    latency_ms          integer,
    created_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (job_id, profile_revision_id, model_version_id, job_content_hash)
);
CREATE INDEX match_results_revision_version ON match_results (profile_revision_id, model_version_id);

-- +goose Down
DROP TABLE match_results;
DROP TABLE model_activations;
DROP TABLE model_versions;
