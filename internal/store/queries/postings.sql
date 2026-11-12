-- name: GetPosting :one
SELECT id, job_id, payload_hash FROM source_postings
WHERE source_id = $1 AND external_id = $2;

-- name: FindOpenJobByDedupKey :one
SELECT id FROM jobs WHERE dedup_key = $1 AND closed_at IS NULL ORDER BY id LIMIT 1;

-- name: InsertJob :one
INSERT INTO jobs (title, company_name, company_key, description_text, description_html,
                  location_text, remote_policy, countries, employment_type, salary_min,
                  salary_max, salary_currency, salary_period, posted_at, content_hash, dedup_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING id;

-- name: UpdateJob :exec
UPDATE jobs
SET title = $2, company_name = $3, company_key = $4, description_text = $5,
    description_html = $6, location_text = $7, remote_policy = $8, countries = $9,
    employment_type = $10, salary_min = $11, salary_max = $12, salary_currency = $13,
    salary_period = $14, posted_at = $15, content_hash = $16, dedup_key = $17,
    closed_at = NULL, updated_at = now()
WHERE id = $1;

-- name: ReopenJob :exec
UPDATE jobs SET closed_at = NULL, updated_at = now() WHERE id = $1 AND closed_at IS NOT NULL;

-- name: InsertPosting :exec
INSERT INTO source_postings (source_id, external_id, job_id, url, raw_payload, payload_hash, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: UpdatePosting :exec
UPDATE source_postings
SET url = $2, raw_payload = $3, payload_hash = $4, expires_at = $5, last_seen_at = now(),
    closed_at = NULL
WHERE id = $1;

-- name: TouchPosting :exec
UPDATE source_postings SET last_seen_at = now(), closed_at = NULL WHERE id = $1;

-- name: ClosePostingsExpired :many
-- Postings past their source-given expiry, whether or not the latest fetch still listed them.
UPDATE source_postings
SET closed_at = now()
WHERE source_id = $1 AND closed_at IS NULL AND expires_at <= now()
RETURNING job_id;

-- name: ClosePostingsNotSeenSince :many
-- Postings of this source missing from the latest full fetch.
UPDATE source_postings
SET closed_at = now()
WHERE source_id = $1 AND closed_at IS NULL AND last_seen_at < $2
RETURNING job_id;

-- name: CloseJobsWithoutOpenPostings :execrows
-- A job closes once every posting that points to it is closed.
UPDATE jobs j SET closed_at = now(), updated_at = now()
WHERE j.id = ANY(sqlc.arg('job_ids')::bigint[]) AND j.closed_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM source_postings p WHERE p.job_id = j.id AND p.closed_at IS NULL);
