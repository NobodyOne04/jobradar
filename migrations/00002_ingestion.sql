-- Job ingestion (ADR 0005): sources, sync runs, canonical jobs, and source postings.

-- +goose Up
CREATE TABLE sources (
    id           bigserial PRIMARY KEY,
    kind         text        NOT NULL CHECK (kind IN ('greenhouse', 'lever', 'ashby', 'himalayas')),
    -- The board/site/job-board name; for himalayas, the search query (a saved search).
    board_token  text        NOT NULL CHECK (length(board_token) BETWEEN 1 AND 200),
    display_name text        NOT NULL DEFAULT '',
    enabled      boolean     NOT NULL DEFAULT true,
    config       jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (kind, board_token)
);

CREATE TABLE sync_runs (
    id          bigserial PRIMARY KEY,
    source_id   bigint      NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    status      text        NOT NULL DEFAULT 'running'
                CHECK (status IN ('running', 'succeeded', 'failed', 'partial')),
    fetched     integer     NOT NULL DEFAULT 0,
    created     integer     NOT NULL DEFAULT 0,
    updated     integer     NOT NULL DEFAULT 0,
    unchanged   integer     NOT NULL DEFAULT 0,
    closed      integer     NOT NULL DEFAULT 0,
    error       text
);
CREATE INDEX sync_runs_source_started ON sync_runs (source_id, started_at DESC);

CREATE TABLE jobs (
    id               bigserial PRIMARY KEY,
    title            text        NOT NULL,
    company_name     text        NOT NULL,
    company_key      text        NOT NULL,
    description_text text        NOT NULL DEFAULT '',
    description_html text        NOT NULL DEFAULT '',
    location_text    text        NOT NULL DEFAULT '',
    remote_policy    text        NOT NULL DEFAULT 'unknown'
                     CHECK (remote_policy IN ('remote', 'hybrid', 'onsite', 'unknown')),
    countries        text[]      NOT NULL DEFAULT '{}',
    employment_type  text        NOT NULL DEFAULT 'unknown'
                     CHECK (employment_type IN ('full_time', 'part_time', 'contract', 'internship', 'unknown')),
    salary_min       numeric(14, 2),
    salary_max       numeric(14, 2),
    salary_currency  char(3),
    salary_period    text CHECK (salary_period IN ('year', 'month', 'day', 'hour')),
    posted_at        timestamptz,
    closed_at        timestamptz,
    content_hash     text        NOT NULL,
    dedup_key        text        NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
-- Cross-source dedup looks up open jobs by key.
CREATE INDEX jobs_open_dedup_key ON jobs (dedup_key) WHERE closed_at IS NULL;

CREATE TABLE source_postings (
    id            bigserial PRIMARY KEY,
    source_id     bigint      NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    external_id   text        NOT NULL,
    job_id        bigint      NOT NULL REFERENCES jobs (id),
    url           text        NOT NULL DEFAULT '',
    raw_payload   jsonb       NOT NULL,
    payload_hash  text        NOT NULL,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    closed_at     timestamptz,
    UNIQUE (source_id, external_id)
);
CREATE INDEX source_postings_job ON source_postings (job_id);

-- +goose Down
DROP TABLE source_postings;
DROP TABLE jobs;
DROP TABLE sync_runs;
DROP TABLE sources;
