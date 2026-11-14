-- name: ChampionVersionID :one
-- The champion the backend works with (ADR 0010); 0 when there is none yet.
SELECT coalesce((SELECT model_version_id FROM model_activations ORDER BY id DESC LIMIT 1), 0)::bigint;

-- name: SearchJobs :many
-- One page of open jobs with each job's best available match for the profile and its
-- application status (ADR 0011). Fresh = current revision, champion, current content; otherwise
-- the newest result, marked stale. Sort keys (k1, k2, id) are all descending, so one keyset
-- condition serves every sort.
WITH page AS (
    SELECT j.id, j.title, j.company_name, j.location_text, j.remote_policy, j.employment_type,
           j.salary_min, j.salary_max, j.salary_currency, j.salary_period, j.posted_at,
           mr.score, mr.band, mr.confidence, mr.completeness, mr.hard_constraints,
           mr.model_version, mr.stale,
           coalesce(a.status, 'discovered')::text AS application_status,
           (CASE WHEN sqlc.arg('sort')::text = 'score' THEN coalesce(mr.score, -1) ELSE 0 END)::float8 AS k1,
           extract(epoch FROM CASE WHEN sqlc.arg('sort')::text = 'updated' THEN j.updated_at
                                   ELSE coalesce(j.posted_at, j.created_at) END)::float8 AS k2
    FROM jobs j
    LEFT JOIN LATERAL (
        SELECT m.score, m.band, m.confidence, m.completeness, m.hard_constraints,
               v.version AS model_version,
               NOT (m.profile_revision_id = sqlc.arg('revision_id') AND m.model_version_id = sqlc.arg('champion_id')
                    AND m.job_content_hash = j.content_hash) AS stale
        FROM match_results m JOIN model_versions v ON v.id = m.model_version_id
        WHERE m.job_id = j.id
          AND m.profile_revision_id IN (SELECT r.id FROM profile_revisions r WHERE r.profile_id = sqlc.arg('profile_id'))
        ORDER BY (m.profile_revision_id = sqlc.arg('revision_id') AND m.model_version_id = sqlc.arg('champion_id')
                  AND m.job_content_hash = j.content_hash) DESC, m.created_at DESC
        LIMIT 1
    ) mr ON true
    LEFT JOIN applications a ON a.job_id = j.id AND a.profile_id = sqlc.arg('profile_id')
    WHERE j.closed_at IS NULL
      AND (sqlc.narg('q')::text IS NULL OR j.search @@ websearch_to_tsquery('english', sqlc.narg('q')))
      AND (sqlc.narg('remote')::text IS NULL OR j.remote_policy = sqlc.narg('remote'))
      AND (sqlc.narg('posted_after')::timestamptz IS NULL OR j.posted_at >= sqlc.narg('posted_after'))
      AND (sqlc.narg('source')::text IS NULL OR EXISTS (
            SELECT 1 FROM source_postings p JOIN sources s ON s.id = p.source_id
            WHERE p.job_id = j.id AND s.kind = sqlc.narg('source')))
)
SELECT id, title, company_name, location_text, remote_policy, employment_type, salary_min,
       salary_max, salary_currency, salary_period, posted_at, application_status, k1, k2,
       -- NULL when the job has no result yet (pending); one object otherwise.
       CASE WHEN band IS NULL THEN NULL ELSE jsonb_build_object(
           'score', score, 'band', band, 'confidence', confidence, 'completeness', completeness,
           'hard_constraints', hard_constraints, 'model_version', model_version, 'stale', stale)
       END::jsonb AS match
FROM page
WHERE (cardinality(sqlc.arg('bands')::text[]) = 0 AND band IS DISTINCT FROM 'excluded'
       OR band = ANY(sqlc.arg('bands')::text[]))
  AND (sqlc.narg('min_score')::int IS NULL OR score >= sqlc.narg('min_score'))
  AND (sqlc.narg('status')::text IS NULL OR application_status = sqlc.narg('status'))
  AND (NOT sqlc.arg('after')::bool
       OR (k1, k2, id) < (sqlc.arg('after_k1')::float8, sqlc.arg('after_k2')::float8, sqlc.arg('after_id')::bigint))
ORDER BY k1 DESC, k2 DESC, id DESC
LIMIT sqlc.arg('lim');

-- name: JobSources :many
-- Where a job is listed, for links and attribution.
SELECT p.job_id, s.kind, p.url, (p.closed_at IS NOT NULL)::bool AS closed
FROM source_postings p JOIN sources s ON s.id = p.source_id
WHERE p.job_id = ANY(sqlc.arg('job_ids')::bigint[])
ORDER BY p.job_id, p.id;

-- name: GetJob :one
SELECT id, title, company_name, description_text, description_html, location_text, remote_policy, countries,
       employment_type, salary_min, salary_max, salary_currency, salary_period, posted_at,
       closed_at, content_hash, created_at, updated_at
FROM jobs WHERE id = $1;

-- name: JobMatch :one
-- The best available match of one job for a profile, with its full stored details.
SELECT m.score, m.band, m.confidence, m.completeness, m.hard_constraints, m.details, m.created_at,
       v.version AS model_version,
       coalesce(NOT (m.profile_revision_id = sqlc.arg('revision_id') AND m.model_version_id = sqlc.arg('champion_id')
            AND m.job_content_hash = j.content_hash), true)::bool AS stale
FROM match_results m
JOIN model_versions v ON v.id = m.model_version_id
JOIN jobs j ON j.id = m.job_id
WHERE m.job_id = sqlc.arg('job_id')
  AND m.profile_revision_id IN (SELECT r.id FROM profile_revisions r WHERE r.profile_id = sqlc.arg('profile_id'))
ORDER BY (m.profile_revision_id = sqlc.arg('revision_id') AND m.model_version_id = sqlc.arg('champion_id')
          AND m.job_content_hash = j.content_hash) DESC, m.created_at DESC
LIMIT 1;
