-- name: CreateProfile :one
INSERT INTO candidate_profiles (name) VALUES ($1) RETURNING *;

-- name: GetProfileByName :one
SELECT * FROM candidate_profiles WHERE name = $1;

-- name: LockProfile :one
-- Serializes revision numbering for one profile within a transaction.
SELECT id FROM candidate_profiles WHERE id = $1 FOR UPDATE;

-- name: ListProfiles :many
SELECT p.id, p.name, p.created_at,
       coalesce((SELECT max(r.revision) FROM profile_revisions r WHERE r.profile_id = p.id), 0)::int AS latest_revision
FROM candidate_profiles p ORDER BY p.id;

-- name: LatestRevisionCompare :one
-- The latest revision, and whether its data equals the given document (jsonb equality ignores
-- key order and whitespace).
SELECT revision, (data = sqlc.arg('data')::jsonb)::bool AS same
FROM profile_revisions WHERE profile_id = sqlc.arg('profile_id')
ORDER BY revision DESC LIMIT 1;

-- name: InsertRevision :one
INSERT INTO profile_revisions (profile_id, revision, schema_version, data)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: GetRevision :one
SELECT * FROM profile_revisions WHERE profile_id = $1 AND revision = $2;

-- name: GetLatestRevision :one
SELECT * FROM profile_revisions WHERE profile_id = $1 ORDER BY revision DESC LIMIT 1;
