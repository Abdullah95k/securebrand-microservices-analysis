-- +goose NO TRANSACTION
-- +goose Up
-- A batch round shares its claim across the platforms waiting (11 Nov):
-- each gets an equal share of its oldest live items, then the rest goes
-- oldest first. Claiming only the oldest across all platforms left a new
-- collector's items (Instagram, from 11 Nov) behind about a million older
-- ones for days. The queue has no platform column: an item's platform is
-- the code before the first ':' of its content ID (fb, ig, tt, yt), so the
-- lookup is an index on that expression, and nothing needs a backfill.
-- Built concurrently: the queue is live and holds over a million rows. A
-- concurrent build that fails leaves an invalid index behind: dropping any
-- first lets a retry build it again.
DROP INDEX CONCURRENTLY IF EXISTS analysis.analysis_queue_platform_idx;
CREATE INDEX CONCURRENTLY analysis_queue_platform_idx
    ON analysis.analysis_queue (lane, route, (split_part(content_id, ':', 1)), priority, enqueued_at);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS analysis.analysis_queue_platform_idx;
