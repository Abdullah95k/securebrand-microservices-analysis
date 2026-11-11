-- +goose Up
-- A request's cost if its cached prefix (system prompt and schema) misses
-- the cache and is written to it: the most it can cost for the prefix.
-- est_cost_usd stays the expected cost (the prefix read from the cache),
-- which the daily budget counts while a request is in flight; stats prints
-- real cost plus every in-flight request at this worst case, the most the
-- day can overshoot by. Nullable, no default: adding it rewrites nothing.
-- Requests from before it count at est_cost_usd.
ALTER TABLE analysis.provider_requests ADD COLUMN IF NOT EXISTS est_max_usd numeric(14,8);

-- +goose Down
ALTER TABLE analysis.provider_requests DROP COLUMN IF EXISTS est_max_usd;
