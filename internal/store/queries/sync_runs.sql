-- name: StartRun :one
INSERT INTO sync_runs (source_id) VALUES ($1) RETURNING *;

-- name: FinishRun :exec
UPDATE sync_runs
SET finished_at = now(), status = $2, fetched = $3, created = $4, updated = $5,
    unchanged = $6, closed = $7, error = $8
WHERE id = $1;

-- name: FailInterruptedRuns :execrows
-- At startup: runs left 'running' by a crash or kill can never finish.
UPDATE sync_runs SET status = 'failed', finished_at = now(), error = 'interrupted: process stopped'
WHERE status = 'running';

-- name: ListRuns :many
SELECT * FROM sync_runs WHERE source_id = $1 ORDER BY started_at DESC LIMIT $2;
