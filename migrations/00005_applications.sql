-- Application tracking (ADR 0008): one row per (job, profile) with its current status, and an
-- append-only event log with the timestamps and notes. No row means "discovered".

-- +goose Up
CREATE TABLE applications (
    id         bigserial PRIMARY KEY,
    job_id     bigint      NOT NULL REFERENCES jobs (id),
    profile_id bigint      NOT NULL REFERENCES candidate_profiles (id),
    status     text        NOT NULL
               CHECK (status IN ('saved', 'applied', 'interview', 'offer', 'rejected', 'archived')),
    notes      text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (job_id, profile_id)
);
CREATE INDEX applications_profile_status ON applications (profile_id, status);

CREATE TABLE application_events (
    id             bigserial PRIMARY KEY,
    application_id bigint      NOT NULL REFERENCES applications (id),
    from_status    text,       -- NULL for the first event (from "discovered")
    to_status      text        NOT NULL,
    note           text,
    forced         boolean     NOT NULL DEFAULT false,
    occurred_at    timestamptz NOT NULL
);
CREATE INDEX application_events_application ON application_events (application_id, occurred_at);

-- +goose Down
DROP TABLE application_events;
DROP TABLE applications;
