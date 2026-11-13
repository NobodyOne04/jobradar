-- Job search (ADR 0011): full-text search over title, company and description, and indexes for
-- the list's default sort and its per-job match lookup.

-- +goose Up
ALTER TABLE jobs ADD COLUMN search tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(company_name, '')), 'B') ||
    setweight(to_tsvector('english', coalesce(description_text, '')), 'C')
) STORED;
CREATE INDEX jobs_search ON jobs USING GIN (search);
CREATE INDEX match_results_job_created ON match_results (job_id, created_at DESC);

-- +goose Down
DROP INDEX match_results_job_created;
DROP INDEX jobs_search;
ALTER TABLE jobs DROP COLUMN search;
