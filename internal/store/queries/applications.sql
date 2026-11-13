-- name: GetApplicationForUpdate :one
SELECT * FROM applications WHERE job_id = $1 AND profile_id = $2 FOR UPDATE;

-- name: GetApplication :one
SELECT * FROM applications WHERE job_id = $1 AND profile_id = $2;

-- name: InsertApplication :one
INSERT INTO applications (job_id, profile_id, status, notes) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: UpdateApplication :one
UPDATE applications SET status = $2, notes = $3, updated_at = now() WHERE id = $1 RETURNING *;

-- name: InsertApplicationEvent :one
INSERT INTO application_events (application_id, from_status, to_status, note, forced, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;

-- name: ListApplications :many
SELECT a.*, j.title, j.company_name FROM applications a JOIN jobs j ON j.id = a.job_id
WHERE a.profile_id = sqlc.arg('profile_id')
  AND (sqlc.narg('status')::text IS NULL OR a.status = sqlc.narg('status'))
ORDER BY a.updated_at DESC, a.id;

-- name: ListApplicationEvents :many
SELECT * FROM application_events WHERE application_id = $1 ORDER BY occurred_at, id;

-- name: JobExists :one
SELECT EXISTS (SELECT 1 FROM jobs WHERE id = $1);

-- name: ProfileIDs :many
SELECT id FROM candidate_profiles ORDER BY id LIMIT 2;

-- name: LabelRows :many
-- One row per application: the job as the matcher reads it, its current outcome, and when the
-- latest event happened.
SELECT a.job_id, a.status, j.title, j.company_name, j.description_text, j.location_text,
       j.remote_policy, j.countries, j.employment_type, j.salary_min, j.salary_max,
       j.salary_currency, j.salary_period,
       (SELECT max(e.occurred_at) FROM application_events e WHERE e.application_id = a.id)::timestamptz AS occurred_at
FROM applications a JOIN jobs j ON j.id = a.job_id
WHERE a.profile_id = $1
ORDER BY a.job_id;
