-- +goose Up
-- Where tagged accounts sit in each text (byte offsets: start, end, start,
-- end, ...), so their names can be hidden from AI providers while the
-- stored text stays exactly as received.
ALTER TABLE analysis.content_items ADD COLUMN tag_spans integer[];

-- analysis.erasure.mode: "record" stores an erasure request without
-- deleting anything; the subject is kept so the owner can apply it later
-- (sb-analysis erasures apply).
ALTER TABLE analysis.erasures ADD COLUMN subject_key text, ADD COLUMN mode text NOT NULL DEFAULT 'apply';

-- +goose Down
ALTER TABLE analysis.erasures DROP COLUMN subject_key, DROP COLUMN mode;
ALTER TABLE analysis.content_items DROP COLUMN tag_spans;
