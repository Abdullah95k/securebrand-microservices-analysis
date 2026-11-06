-- +goose Up
-- REQ-022: a post's caption track (content.post.v1 captions), used as its
-- video's transcript instead of speech-to-text.
ALTER TABLE analysis.content_items
    ADD COLUMN captions text,
    ADD COLUMN captions_lang text,
    ADD COLUMN captions_source text;

-- +goose Down
ALTER TABLE analysis.content_items
    DROP COLUMN captions_source,
    DROP COLUMN captions_lang,
    DROP COLUMN captions;
