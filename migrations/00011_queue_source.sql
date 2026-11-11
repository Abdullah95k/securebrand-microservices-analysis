-- +goose NO TRANSACTION
-- +goose Up
-- A batch round tops up each page it claimed short posts of with that
-- page's other waiting short posts (11 Nov, the master agreed), as it does
-- a thread's comments (00008): short posts go to the comment route and are
-- grouped by page, but a claim of the oldest items holds about one post a
-- page. The queue entry of a short post (a post on the comment route)
-- carries its page; nullable, no default, so adding it rewrites nothing.
ALTER TABLE analysis.analysis_queue ADD COLUMN IF NOT EXISTS source_id text;

-- Entries queued before this get their page from a throttled backfill
-- (10,000 entries a pass, in content-ID order, a pause between passes),
-- which keeps its place here, so a restart goes on where it stopped.
-- sb-analysis stats prints its progress.
CREATE TABLE IF NOT EXISTS analysis.backfills (
    name         text PRIMARY KEY,
    cursor       text NOT NULL DEFAULT '',
    scanned      bigint NOT NULL DEFAULT 0,
    updated      bigint NOT NULL DEFAULT 0,
    started_at   timestamptz NOT NULL DEFAULT now(),
    last_at      timestamptz,
    finished_at  timestamptz
);

-- The lookup by page. Built concurrently: the queue is live and holds
-- hundreds of thousands of rows. A concurrent build that fails leaves an
-- invalid index behind: dropping any first lets a retry build it again.
DROP INDEX CONCURRENTLY IF EXISTS analysis.analysis_queue_source_idx;
CREATE INDEX CONCURRENTLY analysis_queue_source_idx
    ON analysis.analysis_queue (source_id, route, priority, enqueued_at) WHERE source_id IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS analysis.analysis_queue_source_idx;
DROP TABLE IF EXISTS analysis.backfills;
ALTER TABLE analysis.analysis_queue DROP COLUMN IF EXISTS source_id;
