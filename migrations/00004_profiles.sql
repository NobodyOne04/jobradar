-- Candidate profiles (ADR 0006): a named profile and its immutable revisions.

-- +goose Up
CREATE TABLE candidate_profiles (
    id         bigserial PRIMARY KEY,
    name       text        NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Never updated or deleted by the application: a match result points to the exact revision
-- it used (reproducibility, SPEC 6.2).
CREATE TABLE profile_revisions (
    id             bigserial PRIMARY KEY,
    profile_id     bigint      NOT NULL REFERENCES candidate_profiles (id),
    revision       integer     NOT NULL CHECK (revision > 0),
    schema_version integer     NOT NULL,
    data           jsonb       NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (profile_id, revision)
);

-- +goose Down
DROP TABLE profile_revisions;
DROP TABLE candidate_profiles;
