-- name: CreateSource :one
INSERT INTO sources (kind, board_token, display_name, config)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetSource :one
SELECT * FROM sources WHERE id = $1;

-- name: ListSources :many
SELECT * FROM sources ORDER BY id;

-- name: UpdateSource :one
UPDATE sources
SET enabled      = coalesce(sqlc.narg('enabled'), enabled),
    display_name = coalesce(sqlc.narg('display_name'), display_name)
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: ListDueSources :many
-- Enabled sources with no successful run that started within the interval.
SELECT s.* FROM sources s
WHERE s.enabled
  AND NOT EXISTS (
    SELECT 1 FROM sync_runs r
    WHERE r.source_id = s.id AND r.status = 'succeeded'
      AND r.started_at > now() - make_interval(secs => sqlc.arg('interval_seconds')::float8)
  )
ORDER BY s.id;
