-- +goose Up
-- The owner's D-053 (11 Nov 2026): only a fixed share of comments is
-- analysed (analysis.comments.sample_share), chosen by content ID. A comment
-- outside it isn't stored here (its raw text stays with the platform:
-- ClickHouse, fbs's Postgres). This counts, per Iraq day and platform, the
-- comment records received, those in the sample, and those kept outside it
-- because they take the real-time lane, so figures from the analysed share
-- can be scaled. A re-sent comment counts each time, in or out alike, so
-- sampled / received stays close to the share.
CREATE TABLE analysis.comment_sample_daily (
    day       date NOT NULL,
    platform  text NOT NULL,
    received  bigint NOT NULL DEFAULT 0,
    sampled   bigint NOT NULL DEFAULT 0,
    urgent    bigint NOT NULL DEFAULT 0,
    share     numeric(6,5) NOT NULL, -- the share in force at the day's last batch
    PRIMARY KEY (day, platform)
);

-- +goose Down
DROP TABLE analysis.comment_sample_daily;
