-- +goose Up
-- REQ-022, the contract's media hand-off (content.media.ready.v1): a media
-- job's role, the file's SHA-256 and the announcement's attempt; and, on
-- an analysis, the file's SHA-256 and the media whose result it reused
-- (identical files are analysed once).
ALTER TABLE analysis.media_jobs
    ADD COLUMN role text,
    ADD COLUMN sha256 text,
    ADD COLUMN attempt integer NOT NULL DEFAULT 0;
ALTER TABLE analysis.media_analyses
    ADD COLUMN sha256 text,
    ADD COLUMN reused_from text;
CREATE INDEX media_analyses_sha256_idx ON analysis.media_analyses (sha256) WHERE sha256 IS NOT NULL;

-- +goose Down
DROP INDEX analysis.media_analyses_sha256_idx;
ALTER TABLE analysis.media_analyses
    DROP COLUMN reused_from,
    DROP COLUMN sha256;
ALTER TABLE analysis.media_jobs
    DROP COLUMN attempt,
    DROP COLUMN sha256,
    DROP COLUMN role;
