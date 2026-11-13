-- +goose NO TRANSACTION
-- +goose Up
-- Newest files first (the owner's D-060; the master, 13 Nov): with
-- analysis.media.recent_first on, a media claim takes the pending files
-- announced within it, newest first, before the rest by deadline. The
-- deadline order has its own index (media_jobs_due_idx); this one is for
-- the newest, over pending files only. Built concurrently: the table is
-- live. A concurrent build that fails leaves an invalid index behind:
-- dropping any first lets a retry build it again.
DROP INDEX CONCURRENTLY IF EXISTS analysis.media_jobs_recent_idx;
CREATE INDEX CONCURRENTLY media_jobs_recent_idx
    ON analysis.media_jobs (received_at DESC) WHERE state = 'pending';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS analysis.media_jobs_recent_idx;
