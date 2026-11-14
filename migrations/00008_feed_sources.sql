-- Feed sources (ADR 0013): Jobicy, Remote OK, Remotive and Dev Global Jobs become source kinds.

-- +goose Up
ALTER TABLE sources DROP CONSTRAINT sources_kind_check;
ALTER TABLE sources ADD CONSTRAINT sources_kind_check CHECK (kind IN (
    'greenhouse', 'lever', 'ashby', 'himalayas', 'jobicy', 'remoteok', 'remotive', 'devglobaljobs'));

-- +goose Down
DELETE FROM sources WHERE kind IN ('jobicy', 'remoteok', 'remotive', 'devglobaljobs');
ALTER TABLE sources DROP CONSTRAINT sources_kind_check;
ALTER TABLE sources ADD CONSTRAINT sources_kind_check CHECK (kind IN ('greenhouse', 'lever', 'ashby', 'himalayas'));
