-- +goose Up
-- REQ-022: what a result on the contract's analysis.result.v1 needs from
-- its item: the envelope.version that brought the analysed text, a
-- comment's depth, and the author's account ID (raw IDs are ordinary data,
-- owner's decision of 5 November). Zero and NULL for items from fbs's own
-- topics, whose results are not published on the contract's topics.
ALTER TABLE analysis.content_items
    ADD COLUMN content_version bigint NOT NULL DEFAULT 0,
    ADD COLUMN depth integer NOT NULL DEFAULT 0,
    ADD COLUMN author_id text;

-- +goose Down
ALTER TABLE analysis.content_items
    DROP COLUMN author_id,
    DROP COLUMN depth,
    DROP COLUMN content_version;
