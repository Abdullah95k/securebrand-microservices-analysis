-- +goose NO TRANSACTION
-- +goose Up
-- A batch round tops up each thread it claimed with that thread's other
-- waiting comments (11 Nov), so a request carries up to grouping.max_items
-- of them instead of the one or two a claim of the oldest items happens to
-- hold. The lookup is by thread; without this index each one would scan the
-- queue. Built concurrently: the queue is live and holds hundreds of
-- thousands of rows. With batch.top_up_items 0 nothing reads it; it costs
-- only its upkeep on queue writes.
-- A concurrent build that fails leaves an invalid index behind: dropping
-- any first lets a retry build it again (on a first run there is none).
DROP INDEX CONCURRENTLY IF EXISTS analysis.analysis_queue_thread_idx;
CREATE INDEX CONCURRENTLY analysis_queue_thread_idx
    ON analysis.analysis_queue (thread_id, route, priority, enqueued_at) WHERE thread_id IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS analysis.analysis_queue_thread_idx;
