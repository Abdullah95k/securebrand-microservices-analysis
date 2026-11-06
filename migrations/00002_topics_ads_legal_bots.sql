-- +goose Up
-- Prompt p3 / schema s3: ads, possible legal or policy breaches, a dynamic
-- topic list, and bot detection (docs/design/analysis-service.md sections
-- 6, 6.1 and 6.2).

-- Generic aliases ("رئيس الوزراء", "وزارة الصحة") are hints for the model,
-- never a link or an urgent match on their own.
ALTER TABLE analysis.entity_aliases ADD COLUMN ambiguous boolean NOT NULL DEFAULT false;
-- Who resolved a candidate entity (the curation model or the owner), when.
ALTER TABLE analysis.entity_candidates ADD COLUMN resolved_by text, ADD COLUMN resolved_at timestamptz;

-- Ads and possible breaches; the author on the analysis row, for bot flags
-- without a join through the text table.
ALTER TABLE analysis.analyses
    ADD COLUMN ad_type    text,     -- own | paid | hidden | sale
    ADD COLUMN legal      text[],   -- possible breaches, for human review
    ADD COLUMN author_key bytea;
ALTER TABLE analysis.media_analyses
    ADD COLUMN ad_type text,
    ADD COLUMN legal   text[];
CREATE INDEX analyses_author_idx ON analysis.analyses (author_key) WHERE author_key IS NOT NULL;
CREATE INDEX analyses_legal_idx ON analysis.analyses (created_at) WHERE is_current AND legal IS NOT NULL;
-- Topic merges and renames re-point topic arrays.
CREATE INDEX analyses_topics_idx ON analysis.analyses USING gin (topics);
CREATE INDEX entity_mentions_topics_idx ON analysis.entity_mentions USING gin (topics);

-- topics_replace replaces one topic id by another in a topic list (or
-- removes it when the replacement is NULL), keeping the first occurrence of
-- each id and the order.
-- +goose StatementBegin
CREATE FUNCTION analysis.topics_replace(arr text[], old text, new text) RETURNS text[]
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
  SELECT coalesce(array_agg(x ORDER BY first), '{}'::text[]) FROM (
    SELECT x, min(ord) AS first
    FROM unnest(array_replace(coalesce(arr, '{}'::text[]), old, new)) WITH ORDINALITY AS u(x, ord)
    WHERE x IS NOT NULL GROUP BY x) s
$$;
-- +goose StatementEnd

-- Topic list --------------------------------------------------------------------
-- Seeded from configs/analysis/taxonomy.v1.yaml; the model proposes missing
-- topics (candidates), which join the list once used often enough and
-- checked for duplicates. Two levels: a new topic sits under a top-level one.
CREATE TABLE analysis.topics (
    topic_id     text PRIMARY KEY,
    parent_id    text,
    name_en      text NOT NULL,
    name_ar      text,
    status       text NOT NULL,     -- active | candidate | merged | rejected
    merged_into  text REFERENCES analysis.topics (topic_id),
    source       text NOT NULL,     -- seed | model | owner
    ord          integer NOT NULL,  -- display order
    seen         integer NOT NULL DEFAULT 0,   -- items that used it as a candidate
    first_seen   timestamptz NOT NULL DEFAULT now(),
    last_seen    timestamptz NOT NULL DEFAULT now(),
    approved_at  timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX topics_candidates_idx ON analysis.topics (seen DESC) WHERE status = 'candidate';
-- Every name of a topic, as a duplicate key (textnorm.TopicKey: normalised,
-- function words and clitics dropped, words sorted), English and Arabic.
CREATE TABLE analysis.topic_aliases (
    alias_key  text PRIMARY KEY,
    topic_id   text NOT NULL REFERENCES analysis.topics (topic_id) ON DELETE CASCADE,
    alias      text NOT NULL
);
CREATE INDEX topic_aliases_topic_idx ON analysis.topic_aliases (topic_id);
-- Each change of the active list is a taxonomy version (results record it).
CREATE TABLE analysis.taxonomy_versions (
    version        text PRIMARY KEY,
    seq            integer NOT NULL UNIQUE,
    created_at     timestamptz NOT NULL DEFAULT now(),
    active_topics  integer NOT NULL,
    note           text
);

-- Authors (bot detection) ---------------------------------------------------------
-- Behaviour over the scoring window, the score and its reasons. bot is the
-- level in effect: the owner's override wins over the score.
CREATE TABLE analysis.authors (
    platform      text NOT NULL,
    author_key    bytea NOT NULL,
    items         integer NOT NULL DEFAULT 0,
    active_days   integer NOT NULL DEFAULT 0,
    per_day       real NOT NULL DEFAULT 0,
    max_per_hour  integer NOT NULL DEFAULT 0,
    sources       integer NOT NULL DEFAULT 0,
    active_hours  integer NOT NULL DEFAULT 0,
    repeated      real NOT NULL DEFAULT 0,   -- shares of the author's items
    coordinated   real NOT NULL DEFAULT 0,
    fast          real NOT NULL DEFAULT 0,
    spammy        real NOT NULL DEFAULT 0,
    score         integer NOT NULL DEFAULT 0,
    level         text NOT NULL DEFAULT '',  -- '' | suspected | likely
    reasons       text[] NOT NULL DEFAULT '{}',
    override      text,                      -- bot | human
    bot           text GENERATED ALWAYS AS (CASE WHEN override = 'human' THEN NULL WHEN override = 'bot' THEN 'confirmed'
                                                 WHEN level <> '' THEN level END) STORED,
    first_seen    timestamptz,
    last_seen     timestamptz,
    scored_at     timestamptz,
    PRIMARY KEY (platform, author_key)
);
CREATE INDEX authors_bot_idx ON analysis.authors (bot) WHERE bot IS NOT NULL;
-- Every manual decision on an author ("not a bot", "is a bot", back to
-- the score): who, when, why, and the level before and after.
CREATE TABLE analysis.author_overrides (
    id          bigserial PRIMARY KEY,
    platform    text NOT NULL,
    author_key  bytea NOT NULL,
    override    text,            -- bot | human | NULL (back to the score)
    bot_before  text,
    bot_after   text,
    decided_by  text NOT NULL,
    note        text,
    decided_at  timestamptz NOT NULL DEFAULT now()
);
-- Erasure and the log look an author up by key alone.
CREATE INDEX author_overrides_author_idx ON analysis.author_overrides (author_key, decided_at);

-- Rollups -------------------------------------------------------------------------
ALTER TABLE analysis.rollup_source_daily
    ADD COLUMN ads       bigint NOT NULL DEFAULT 0,
    ADD COLUMN legal     bigint NOT NULL DEFAULT 0,
    ADD COLUMN bot_items bigint NOT NULL DEFAULT 0;
ALTER TABLE analysis.rollup_entity_daily
    ADD COLUMN bot_mentions bigint NOT NULL DEFAULT 0;
-- Items per day by ad type (ad_own, ad_paid, ad_hidden, ad_sale) and
-- possible breach (incitement, fraud_scam, ...).
CREATE TABLE analysis.rollup_flag_daily (
    day        date NOT NULL,
    platform   text NOT NULL,
    source_id  text NOT NULL,
    flag       text NOT NULL,
    items      bigint NOT NULL,
    bot_items  bigint NOT NULL,
    PRIMARY KEY (day, platform, source_id, flag)
);

-- Agent views: the new fields, and author_bot on every item, mention and
-- text (the author's level now, so earlier items are flagged too). The
-- author key itself is never shown.
CREATE OR REPLACE VIEW agent.mentions WITH (security_barrier) AS
SELECT m.day, m.created_at, m.platform, m.source_id, m.kind, m.content_id,
       m.entity_id, m.name AS entity_name, m.entity_type, m.sentiment, m.score,
       m.aspect, m.origin, m.topics, m.lang, m.intent, au.bot AS author_bot
FROM analysis.entity_mentions m
LEFT JOIN analysis.authors au ON au.platform = m.platform AND au.author_key = m.author_key
WHERE EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_entities ce ON ce.client_id = r.client_id
                 WHERE r.role_name = current_user AND ce.entity_id = m.entity_id) OR EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_sources cs ON cs.client_id = r.client_id
                 WHERE r.role_name = current_user AND cs.source_id = m.source_id);

CREATE OR REPLACE VIEW agent.items WITH (security_barrier) AS
SELECT (a.created_at AT TIME ZONE 'Asia/Baghdad')::date AS day, a.created_at, a.platform,
       a.source_id, a.kind, a.content_id, a.lang, a.dialect, a.sentiment, a.sentiment_score,
       a.intent, a.emotion, a.toxicity, a.spam, a.sarcasm, a.urgent, a.topics, a.keywords,
       a.model, a.ad_type, a.legal, au.bot AS author_bot
FROM analysis.analyses a
LEFT JOIN analysis.authors au ON au.platform = a.platform AND au.author_key = a.author_key
WHERE a.is_current AND (EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_sources cs ON cs.client_id = r.client_id
                 WHERE r.role_name = current_user AND cs.source_id = a.source_id) OR EXISTS (
      SELECT 1 FROM analysis.entity_mentions m
      WHERE m.content_id = a.content_id AND m.created_at = a.created_at
        AND EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_entities ce ON ce.client_id = r.client_id
                 WHERE r.role_name = current_user AND ce.entity_id = m.entity_id)));

CREATE OR REPLACE VIEW agent.texts WITH (security_barrier) AS
SELECT c.content_id, (c.created_at AT TIME ZONE 'Asia/Baghdad')::date AS day, c.created_at,
       c.platform, c.source_id, c.kind, analysis.mask_pii(c.text) AS text,
       analysis.ar_normalize(c.text) AS search_norm, au.bot AS author_bot
FROM analysis.content_items c
LEFT JOIN analysis.authors au ON au.platform = c.platform AND au.author_key = c.author_key
WHERE c.status <> 'redacted' AND c.text IS NOT NULL
  AND (EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_sources cs ON cs.client_id = r.client_id
                 WHERE r.role_name = current_user AND cs.source_id = c.source_id) OR EXISTS (
       SELECT 1 FROM analysis.entity_mentions m
       WHERE m.content_id = c.content_id AND m.created_at = c.created_at
         AND EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_entities ce ON ce.client_id = r.client_id
                 WHERE r.role_name = current_user AND ce.entity_id = m.entity_id)));

CREATE OR REPLACE VIEW agent.rollup_entity_daily WITH (security_barrier) AS
SELECT * FROM analysis.rollup_entity_daily x WHERE EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_entities ce ON ce.client_id = r.client_id
                 WHERE r.role_name = current_user AND ce.entity_id = x.entity_id);
CREATE OR REPLACE VIEW agent.rollup_source_daily WITH (security_barrier) AS
SELECT * FROM analysis.rollup_source_daily x WHERE EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_sources cs ON cs.client_id = r.client_id
                 WHERE r.role_name = current_user AND cs.source_id = x.source_id);
CREATE VIEW agent.rollup_flag_daily WITH (security_barrier) AS
SELECT * FROM analysis.rollup_flag_daily x WHERE EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_sources cs ON cs.client_id = r.client_id
                 WHERE r.role_name = current_user AND cs.source_id = x.source_id);
-- The topic list is not client data: every agent role may read it, with
-- every name of each topic, to turn a question in any language into topic
-- ids. A topic's sub-topics are the ids that start with its id and a dot:
--   WHERE topics && ARRAY(SELECT topic_id FROM agent.topics
--                          WHERE topic_id = 'services.electricity' OR topic_id LIKE 'services.electricity.%')
CREATE VIEW agent.topics AS
SELECT topic_id, parent_id, name_en, name_ar, status, merged_into FROM analysis.topics WHERE status IN ('active', 'candidate', 'merged');
CREATE VIEW agent.topic_aliases AS
SELECT a.topic_id, a.alias FROM analysis.topic_aliases a JOIN analysis.topics t ON t.topic_id = a.topic_id
WHERE t.status IN ('active', 'candidate');

-- Agent roles created before this migration: grant the new views.
-- +goose StatementBegin
DO $$
DECLARE r text;
BEGIN
  FOR r IN SELECT role_name FROM analysis.client_roles JOIN pg_roles ON rolname = role_name LOOP
    EXECUTE format('GRANT SELECT ON agent.rollup_flag_daily, agent.topics, agent.topic_aliases TO %I', r);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP VIEW agent.topic_aliases;
DROP VIEW agent.topics;
DROP VIEW agent.rollup_flag_daily;
DROP VIEW agent.mentions;
DROP VIEW agent.items;
DROP VIEW agent.texts;
DROP VIEW agent.rollup_entity_daily;
DROP VIEW agent.rollup_source_daily;
DROP TABLE analysis.rollup_flag_daily;
ALTER TABLE analysis.rollup_entity_daily DROP COLUMN bot_mentions;
ALTER TABLE analysis.rollup_source_daily DROP COLUMN ads, DROP COLUMN legal, DROP COLUMN bot_items;
DROP TABLE analysis.author_overrides;
DROP TABLE analysis.authors;
DROP TABLE analysis.taxonomy_versions;
DROP TABLE analysis.topic_aliases;
DROP TABLE analysis.topics;
DROP FUNCTION analysis.topics_replace(text[], text, text);
DROP INDEX analysis.entity_mentions_topics_idx;
DROP INDEX analysis.analyses_topics_idx;
ALTER TABLE analysis.media_analyses DROP COLUMN ad_type, DROP COLUMN legal;
ALTER TABLE analysis.analyses DROP COLUMN ad_type, DROP COLUMN legal, DROP COLUMN author_key;
ALTER TABLE analysis.entity_aliases DROP COLUMN ambiguous;
ALTER TABLE analysis.entity_candidates DROP COLUMN resolved_by, DROP COLUMN resolved_at;
-- The views as 00001 created them (re-run agent-role to grant them again).
CREATE VIEW agent.mentions WITH (security_barrier) AS
SELECT m.day, m.created_at, m.platform, m.source_id, m.kind, m.content_id,
       m.entity_id, m.name AS entity_name, m.entity_type, m.sentiment, m.score,
       m.aspect, m.origin, m.topics, m.lang, m.intent
FROM analysis.entity_mentions m
WHERE EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_entities ce ON ce.client_id = r.client_id
                 WHERE r.role_name = current_user AND ce.entity_id = m.entity_id) OR EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_sources cs ON cs.client_id = r.client_id
                 WHERE r.role_name = current_user AND cs.source_id = m.source_id);

CREATE VIEW agent.items WITH (security_barrier) AS
SELECT (a.created_at AT TIME ZONE 'Asia/Baghdad')::date AS day, a.created_at, a.platform,
       a.source_id, a.kind, a.content_id, a.lang, a.dialect, a.sentiment, a.sentiment_score,
       a.intent, a.emotion, a.toxicity, a.spam, a.sarcasm, a.urgent, a.topics, a.keywords,
       a.model
FROM analysis.analyses a
WHERE a.is_current AND (EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_sources cs ON cs.client_id = r.client_id
                 WHERE r.role_name = current_user AND cs.source_id = a.source_id) OR EXISTS (
      SELECT 1 FROM analysis.entity_mentions m
      WHERE m.content_id = a.content_id AND m.created_at = a.created_at
        AND EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_entities ce ON ce.client_id = r.client_id
                 WHERE r.role_name = current_user AND ce.entity_id = m.entity_id)));

CREATE VIEW agent.texts WITH (security_barrier) AS
SELECT c.content_id, (c.created_at AT TIME ZONE 'Asia/Baghdad')::date AS day, c.created_at,
       c.platform, c.source_id, c.kind, analysis.mask_pii(c.text) AS text,
       analysis.ar_normalize(c.text) AS search_norm
FROM analysis.content_items c
WHERE c.status <> 'redacted' AND c.text IS NOT NULL
  AND (EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_sources cs ON cs.client_id = r.client_id
                 WHERE r.role_name = current_user AND cs.source_id = c.source_id) OR EXISTS (
       SELECT 1 FROM analysis.entity_mentions m
       WHERE m.content_id = c.content_id AND m.created_at = c.created_at
         AND EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_entities ce ON ce.client_id = r.client_id
                 WHERE r.role_name = current_user AND ce.entity_id = m.entity_id)));

CREATE VIEW agent.rollup_entity_daily WITH (security_barrier) AS
SELECT * FROM analysis.rollup_entity_daily x WHERE EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_entities ce ON ce.client_id = r.client_id
                 WHERE r.role_name = current_user AND ce.entity_id = x.entity_id);

CREATE VIEW agent.rollup_source_daily WITH (security_barrier) AS
SELECT * FROM analysis.rollup_source_daily x WHERE EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_sources cs ON cs.client_id = r.client_id
                 WHERE r.role_name = current_user AND cs.source_id = x.source_id);
