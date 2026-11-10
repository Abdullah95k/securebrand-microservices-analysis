-- +goose Up
-- A record the relay can't publish (its topic refused, for example) waits
-- with a backoff instead of holding back every record behind it. Nothing is
-- dropped: it stays until it is published.
ALTER TABLE analysis.outbox
    ADD COLUMN attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN not_before timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN last_error text;
CREATE INDEX outbox_ready ON analysis.outbox (not_before, id);

-- +goose Down
DROP INDEX analysis.outbox_ready;
ALTER TABLE analysis.outbox
    DROP COLUMN last_error,
    DROP COLUMN not_before,
    DROP COLUMN attempts;
