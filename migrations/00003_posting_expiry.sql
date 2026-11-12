-- Postings a source says will expire (Himalayas expiryDate): closed once past, on every
-- sync, even when a truncated listing cannot prove they vanished (Himalayas terms §113 d).

-- Also: jobs.dedup_key becomes the SHA-256 hex of the plain key (see ingest.DedupKey), because
-- plain keys with long location lists exceeded the btree entry limit.

-- +goose Up
ALTER TABLE source_postings ADD COLUMN expires_at timestamptz;
-- Existing plain keys get hashed the same way Go now does, so dedup keeps matching them.
UPDATE jobs SET dedup_key = encode(sha256(convert_to(dedup_key, 'UTF8')), 'hex')
WHERE dedup_key !~ '^[0-9a-f]{64}$';

-- +goose Down
ALTER TABLE source_postings DROP COLUMN expires_at;
-- dedup_key stays hashed: a hash cannot be reversed, and the old code only needs keys to
-- be equal for the same job, which hashed keys still are for jobs already stored.
