-- More company boards (ADR 0014): SmartRecruiters, Workable, Recruitee, Personio, Breezy,
-- Teamtailor and Rippling become source kinds.

-- +goose Up
ALTER TABLE sources DROP CONSTRAINT sources_kind_check;
ALTER TABLE sources ADD CONSTRAINT sources_kind_check CHECK (kind IN (
    'greenhouse', 'lever', 'ashby', 'himalayas', 'jobicy', 'remoteok', 'remotive', 'devglobaljobs',
    'smartrecruiters', 'workable', 'recruitee', 'personio', 'breezy', 'teamtailor', 'rippling'));

-- +goose Down
DELETE FROM sources WHERE kind IN ('smartrecruiters', 'workable', 'recruitee', 'personio', 'breezy', 'teamtailor', 'rippling');
ALTER TABLE sources DROP CONSTRAINT sources_kind_check;
ALTER TABLE sources ADD CONSTRAINT sources_kind_check CHECK (kind IN (
    'greenhouse', 'lever', 'ashby', 'himalayas', 'jobicy', 'remoteok', 'remotive', 'devglobaljobs'));
