-- name: UpsertModelVersion :one
-- Tags change after registration (promotion writes them), so they are refreshed.
INSERT INTO model_versions (version, mlflow_run_id, matcher_version, metrics, tags)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (version) DO UPDATE SET tags = EXCLUDED.tags, metrics = EXCLUDED.metrics
RETURNING *;

-- name: LatestActivation :one
SELECT a.id, a.observed_at, v.id AS model_version_id, v.version
FROM model_activations a JOIN model_versions v ON v.id = a.model_version_id
ORDER BY a.id DESC LIMIT 1;

-- name: InsertActivation :one
INSERT INTO model_activations (model_version_id, previous_id, reason, gate)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: ListModelVersions :many
SELECT * FROM model_versions ORDER BY version::bigint;

-- name: ListActivations :many
SELECT a.id, a.observed_at, a.reason, a.gate, v.version, p.version AS previous_version
FROM model_activations a
JOIN model_versions v ON v.id = a.model_version_id
LEFT JOIN model_versions p ON p.id = a.previous_id
ORDER BY a.id DESC LIMIT $1;

-- name: LatestRevisions :many
-- The current revision of every profile.
SELECT DISTINCT ON (profile_id) id, profile_id, revision, data
FROM profile_revisions ORDER BY profile_id, revision DESC;

-- name: PendingJobs :many
-- Open jobs without a result for this revision, model version and the job's current content.
-- Jobs never matched for this revision come first (new jobs), then the backfill.
SELECT j.id, j.title, j.company_name, j.description_text, j.location_text, j.remote_policy,
       j.countries, j.employment_type, j.salary_min, j.salary_max, j.salary_currency,
       j.salary_period, j.content_hash
FROM jobs j
WHERE j.closed_at IS NULL
  AND j.id <> ALL(sqlc.arg('skip')::bigint[])
  AND NOT EXISTS (
    SELECT 1 FROM match_results m
    WHERE m.job_id = j.id AND m.profile_revision_id = sqlc.arg('revision_id')
      AND m.model_version_id = sqlc.arg('model_version_id') AND m.job_content_hash = j.content_hash)
ORDER BY EXISTS (SELECT 1 FROM match_results m2
                 WHERE m2.job_id = j.id AND m2.profile_revision_id = sqlc.arg('revision_id')),
         j.id DESC
LIMIT sqlc.arg('lim');

-- name: CountPending :one
SELECT count(*) FROM jobs j
WHERE j.closed_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM match_results m
    WHERE m.job_id = j.id AND m.profile_revision_id = $1 AND m.model_version_id = $2
      AND m.job_content_hash = j.content_hash);

-- name: InsertMatchResult :exec
INSERT INTO match_results (job_id, profile_revision_id, model_version_id, job_content_hash, score,
                           band, confidence, completeness, hard_constraints, details, latency_ms)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT DO NOTHING;
