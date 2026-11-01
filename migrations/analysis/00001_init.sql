-- +goose Up
CREATE SCHEMA IF NOT EXISTS analysis;

-- Arabic normalisation for full-text search: alef/yaa/taa-marbuta/hamza
-- forms unified, diacritics and tatweel removed, Arabic-Indic digits to
-- ASCII, Latin lower-cased. Queries apply the same function.
-- +goose StatementBegin
CREATE FUNCTION analysis.ar_normalize(t text) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE RETURNS NULL ON NULL INPUT AS $$
  SELECT regexp_replace(
    translate(lower(t), 'أإآٱىةؤئ٠١٢٣٤٥٦٧٨٩۰۱۲۳۴۵۶۷۸۹', 'اااايهوي01234567890123456789'),
    '[ً-ٰٟـ]', '', 'g')
$$;
-- +goose StatementEnd

-- Phone numbers and e-mail addresses masked, for texts shown to the
-- analytics agent (example retrieval).
-- +goose StatementBegin
CREATE FUNCTION analysis.mask_pii(t text) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE RETURNS NULL ON NULL INPUT AS $$
  SELECT regexp_replace(
    regexp_replace(t, '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}', '[email]', 'g'),
    '(\+|00)?[0-9٠-٩۰-۹][0-9٠-٩۰-۹ ()-]{7,}[0-9٠-٩۰-۹]', '[phone]', 'g')
$$;
-- +goose StatementEnd

-- Content ---------------------------------------------------------------------
-- content_index answers "have we seen this content, with which text?" for
-- idempotency; hash partitions keep each btree small.
CREATE TABLE analysis.content_index (
    content_id        text NOT NULL,
    created_at        timestamptz NOT NULL,
    text_hash         bytea NOT NULL,
    state             text NOT NULL,   -- queued | analyzed | skipped | failed | deleted | redacted
    analyzed_hash     bytea,           -- input hash of the current analysis
    analyzed_route    text,            -- route of the current analysis
    analysis_version  integer NOT NULL DEFAULT 0,
    visible           boolean NOT NULL DEFAULT true,  -- false once the platform deleted it
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (content_id)
) PARTITION BY HASH (content_id);
-- +goose StatementBegin
DO $$
BEGIN
  FOR i IN 0..15 LOOP
    EXECUTE format('CREATE TABLE analysis.content_index_p%s PARTITION OF analysis.content_index FOR VALUES WITH (MODULUS 16, REMAINDER %s)', i, i);
  END LOOP;
END $$;
-- +goose StatementEnd

-- Text kept indefinitely, with its analysis (owner's decision). Monthly
-- partitions on the item's creation time; created on demand.
CREATE TABLE analysis.content_items (
    created_at        timestamptz NOT NULL,
    content_id        text NOT NULL,
    platform          text NOT NULL,
    kind              text NOT NULL,   -- post | comment | reply
    source_id         text NOT NULL,
    source_name       text,
    thread_id         text,
    parent_id         text,
    author_key        bytea,
    author_is_source  boolean NOT NULL DEFAULT false,
    text              text,
    lang_hint         text,
    hashtags          text[],
    media_ids         text[],
    status            text NOT NULL DEFAULT 'visible',  -- visible | deleted | redacted
    skip_reason       text,
    edit_count        integer NOT NULL DEFAULT 0,
    first_seen_at     timestamptz NOT NULL,
    last_seen_at      timestamptz NOT NULL,
    PRIMARY KEY (created_at, content_id)
) PARTITION BY RANGE (created_at);
CREATE INDEX content_items_source_idx ON analysis.content_items (source_id, created_at DESC);
CREATE INDEX content_items_thread_idx ON analysis.content_items (thread_id) WHERE thread_id IS NOT NULL;
CREATE INDEX content_items_author_idx ON analysis.content_items (author_key) WHERE author_key IS NOT NULL;
CREATE INDEX content_items_fts_idx ON analysis.content_items
    USING gin (to_tsvector('simple', analysis.ar_normalize(text)));

-- Analyses: one row per analysis version (re-analyses and audits add rows);
-- is_current marks the version dashboards use.
CREATE TABLE analysis.analyses (
    created_at             timestamptz NOT NULL,   -- the item's creation time
    content_id             text NOT NULL,
    version                integer NOT NULL,
    is_current             boolean NOT NULL,
    purpose                text NOT NULL,          -- current | audit
    platform               text NOT NULL,
    source_id              text NOT NULL,
    kind                   text NOT NULL,
    route                  text NOT NULL,
    lane                   text NOT NULL,
    provider               text NOT NULL,
    model                  text NOT NULL,
    prompt_version         text NOT NULL,
    taxonomy_version       text NOT NULL,
    schema_version         text NOT NULL,
    input_hash             bytea NOT NULL,
    fallback               boolean NOT NULL DEFAULT false,
    analyzed_at            timestamptz NOT NULL,
    request_id             uuid,
    lang                   text,
    dialect                text,
    script                 text,
    code_switch            boolean,
    sentiment              text,
    sentiment_score        real,
    confidence             real,
    calibrated_confidence  real,
    intent                 text,
    emotion                text,
    toxicity               text,
    spam                   boolean,
    sarcasm                boolean,
    urgent                 boolean,
    topics                 text[],
    keywords               text[],
    private_person_mentions integer NOT NULL DEFAULT 0,
    refused                boolean NOT NULL DEFAULT false,
    result                 jsonb NOT NULL,
    cost_usd               numeric(14,8),
    PRIMARY KEY (created_at, content_id, version)
) PARTITION BY RANGE (created_at);
CREATE INDEX analyses_content_idx ON analysis.analyses (content_id);
CREATE INDEX analyses_current_idx ON analysis.analyses (source_id, created_at) WHERE is_current;
CREATE INDEX analyses_model_idx ON analysis.analyses (model, created_at) WHERE is_current;

-- One row per (current analysis, entity): per-entity sentiment with the
-- item's dimensions, so analytics need no joins.
CREATE TABLE analysis.entity_mentions (
    created_at   timestamptz NOT NULL,
    content_id   text NOT NULL,
    version      integer NOT NULL,
    ord          smallint NOT NULL,
    day          date NOT NULL,                -- Iraq-time day of created_at
    platform     text NOT NULL,
    source_id    text NOT NULL,
    kind         text NOT NULL,
    author_key   bytea,
    entity_id    text,                          -- NULL when not linked
    name         text NOT NULL,
    entity_type  text NOT NULL,
    sentiment    text NOT NULL,
    score        real,
    confidence   real,
    aspect       text,
    origin       text NOT NULL DEFAULT 'text',  -- text | image | video
    topics       text[],
    lang         text,
    intent       text,
    PRIMARY KEY (created_at, content_id, version, ord)
) PARTITION BY RANGE (created_at);
CREATE INDEX entity_mentions_entity_idx ON analysis.entity_mentions (entity_id, created_at) INCLUDE (sentiment, platform);
CREATE INDEX entity_mentions_source_idx ON analysis.entity_mentions (source_id, created_at);
CREATE INDEX entity_mentions_content_idx ON analysis.entity_mentions (content_id);
CREATE INDEX entity_mentions_author_idx ON analysis.entity_mentions (author_key) WHERE author_key IS NOT NULL;
-- Unlinked names, re-pointed when a review accepts or merges a candidate.
CREATE INDEX entity_mentions_unlinked_idx ON analysis.entity_mentions (analysis.ar_normalize(lower(name))) WHERE entity_id IS NULL;

-- Work queue ------------------------------------------------------------------
CREATE TABLE analysis.analysis_queue (
    content_id   text NOT NULL,
    route        text NOT NULL,
    created_at   timestamptz NOT NULL,
    thread_id    text,
    lane         text NOT NULL,       -- realtime | batch
    priority     smallint NOT NULL,   -- 0 urgent, 1 normal, 2 background
    reason       text NOT NULL,       -- new | edit | escalation | audit | reanalysis
    enqueued_at  timestamptz NOT NULL DEFAULT now(),
    not_before   timestamptz NOT NULL DEFAULT now(),
    attempts     integer NOT NULL DEFAULT 0,
    lease_until  timestamptz,
    request_id   uuid,
    last_error   text,
    PRIMARY KEY (content_id, route)
);
CREATE INDEX analysis_queue_claim_idx ON analysis.analysis_queue (lane, route, priority, enqueued_at);
CREATE INDEX analysis_queue_request_idx ON analysis.analysis_queue (request_id) WHERE request_id IS NOT NULL;

CREATE TABLE analysis.dead_letters (
    content_id  text NOT NULL,
    route       text NOT NULL,
    attempts    integer NOT NULL,
    error       text,
    failed_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (content_id, route)
);

CREATE TABLE analysis.provider_batches (
    batch_id      text PRIMARY KEY,
    provider      text NOT NULL,
    model         text NOT NULL,
    route         text NOT NULL,
    status        text NOT NULL,      -- in_progress | ended | failed
    requests      integer NOT NULL,
    submitted_at  timestamptz NOT NULL DEFAULT now(),
    ended_at      timestamptz,
    lease_until   timestamptz,
    last_error    text
);
CREATE INDEX provider_batches_open_idx ON analysis.provider_batches (submitted_at) WHERE status = 'in_progress';

CREATE TABLE analysis.provider_requests (
    request_id          uuid PRIMARY KEY,
    batch_id            text,
    route               text NOT NULL,
    lane                text NOT NULL,
    provider            text NOT NULL,
    model               text NOT NULL,
    items               integer NOT NULL,
    -- The request's items in prompt order (index i = position) and their
    -- input hashes when sent: results are matched back by both.
    content_ids         text[] NOT NULL DEFAULT '{}',
    input_hashes        bytea[] NOT NULL DEFAULT '{}',
    status              text NOT NULL,   -- submitted | done | failed
    fallback            boolean NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL DEFAULT now(),
    completed_at        timestamptz,
    est_cost_usd        numeric(14,8),
    input_tokens        integer,
    output_tokens       integer,
    cache_read_tokens   integer,
    cache_write_tokens  integer,
    cost_usd            numeric(14,8),
    latency_ms          integer,
    error               text
);
CREATE INDEX provider_requests_day_idx ON analysis.provider_requests (created_at);
CREATE INDEX provider_requests_batch_idx ON analysis.provider_requests (batch_id) WHERE batch_id IS NOT NULL;

-- Transactional outbox: results and media acks reach Kafka after commit.
CREATE TABLE analysis.outbox (
    id          bigserial PRIMARY KEY,
    topic       text NOT NULL,
    key         text NOT NULL,
    type        text NOT NULL,
    payload     bytea NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Media -----------------------------------------------------------------------
CREATE TABLE analysis.media_jobs (
    media_id          text PRIMARY KEY,
    owner_content_id  text,
    platform          text NOT NULL,
    source_id         text,
    kind              text NOT NULL,     -- photo | video | reel | comment_photo | ...
    bucket            text NOT NULL,
    key               text NOT NULL,
    size_bytes        bigint,
    content_type      text,
    duration_ms       bigint,
    alt_text          text,
    delete_after      timestamptz,
    ack_format        text NOT NULL,     -- fb | content
    state             text NOT NULL,     -- pending | done | failed | skipped
    tries             integer NOT NULL DEFAULT 0,
    not_before        timestamptz NOT NULL DEFAULT now(),
    lease_until       timestamptz,
    last_error        text,
    received_at       timestamptz NOT NULL DEFAULT now(),
    done_at           timestamptz
);
CREATE INDEX media_jobs_due_idx ON analysis.media_jobs (delete_after) WHERE state = 'pending';
CREATE INDEX media_jobs_owner_idx ON analysis.media_jobs (owner_content_id);

CREATE TABLE analysis.media_analyses (
    media_id          text PRIMARY KEY,
    owner_content_id  text,
    platform          text NOT NULL,
    source_id         text,
    kind              text NOT NULL,
    analyzed_at       timestamptz NOT NULL,
    provider          text NOT NULL,
    model             text NOT NULL,
    prompt_version    text NOT NULL,
    taxonomy_version  text NOT NULL,
    description       text,
    text_in_image     text,
    transcript        text,
    logos             jsonb,
    result            jsonb NOT NULL
);
CREATE INDEX media_analyses_owner_idx ON analysis.media_analyses (owner_content_id);

-- Entity registry ---------------------------------------------------------------
CREATE TABLE analysis.entities (
    entity_id      text PRIMARY KEY,
    name           text NOT NULL,
    name_ar        text,
    type           text NOT NULL,
    public_figure  boolean NOT NULL DEFAULT false,
    status         text NOT NULL DEFAULT 'active',   -- active | merged | rejected
    merged_into    text REFERENCES analysis.entities (entity_id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE analysis.entity_aliases (
    alias_norm  text NOT NULL,
    entity_id   text NOT NULL REFERENCES analysis.entities (entity_id) ON DELETE CASCADE,
    alias       text NOT NULL,
    source      text NOT NULL,   -- owner | seed | discovered
    PRIMARY KEY (alias_norm, entity_id)
);
CREATE INDEX entity_aliases_entity_idx ON analysis.entity_aliases (entity_id);

-- Names the model found that the registry does not know: the review queue.
CREATE TABLE analysis.entity_candidates (
    norm                text NOT NULL,
    type                text NOT NULL,
    surface             text NOT NULL,
    seen                integer NOT NULL DEFAULT 1,
    first_seen          timestamptz NOT NULL DEFAULT now(),
    last_seen           timestamptz NOT NULL DEFAULT now(),
    examples            text[] NOT NULL DEFAULT '{}',
    status              text NOT NULL DEFAULT 'pending',  -- pending | accepted | merged | rejected
    resolved_entity_id  text,
    PRIMARY KEY (norm, type)
);
CREATE INDEX entity_candidates_review_idx ON analysis.entity_candidates (seen DESC) WHERE status = 'pending';

-- Clients and what they may see (agent scoping) --------------------------------
CREATE TABLE analysis.clients (
    client_id   text PRIMARY KEY,
    name        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE analysis.client_entities (
    client_id  text NOT NULL REFERENCES analysis.clients (client_id) ON DELETE CASCADE,
    entity_id  text NOT NULL REFERENCES analysis.entities (entity_id) ON DELETE CASCADE,
    role       text NOT NULL DEFAULT 'own',   -- own | competitor | watch
    PRIMARY KEY (client_id, entity_id)
);
CREATE INDEX client_entities_entity_idx ON analysis.client_entities (entity_id);
CREATE TABLE analysis.client_sources (
    client_id  text NOT NULL REFERENCES analysis.clients (client_id) ON DELETE CASCADE,
    source_id  text NOT NULL,
    PRIMARY KEY (client_id, source_id)
);
-- One database login role per client; agent views filter on current_user.
CREATE TABLE analysis.client_roles (
    role_name  text PRIMARY KEY,
    client_id  text NOT NULL REFERENCES analysis.clients (client_id) ON DELETE CASCADE
);

-- Rollups (Iraq-time days), recomputed per dirty day ---------------------------
CREATE TABLE analysis.rollup_dirty (
    day        date PRIMARY KEY,
    marked_at  timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE analysis.rollup_entity_daily (
    day        date NOT NULL,
    entity_id  text NOT NULL,
    platform   text NOT NULL,
    source_id  text NOT NULL,
    sentiment  text NOT NULL,
    mentions   bigint NOT NULL,
    score_sum  double precision NOT NULL,
    PRIMARY KEY (day, entity_id, platform, source_id, sentiment)
);
CREATE INDEX rollup_entity_daily_entity_idx ON analysis.rollup_entity_daily (entity_id, day);
CREATE TABLE analysis.rollup_entity_topic_daily (
    day        date NOT NULL,
    entity_id  text NOT NULL,
    platform   text NOT NULL,
    topic      text NOT NULL,
    mentions   bigint NOT NULL,
    positive   bigint NOT NULL,
    negative   bigint NOT NULL,
    neutral    bigint NOT NULL,
    mixed      bigint NOT NULL,
    PRIMARY KEY (day, entity_id, platform, topic)
);
CREATE INDEX rollup_entity_topic_daily_entity_idx ON analysis.rollup_entity_topic_daily (entity_id, day);
CREATE TABLE analysis.rollup_topic_daily (
    day        date NOT NULL,
    platform   text NOT NULL,
    source_id  text NOT NULL,
    topic      text NOT NULL,
    items      bigint NOT NULL,
    positive   bigint NOT NULL,
    negative   bigint NOT NULL,
    neutral    bigint NOT NULL,
    mixed      bigint NOT NULL,
    PRIMARY KEY (day, platform, source_id, topic)
);
CREATE TABLE analysis.rollup_source_daily (
    day         date NOT NULL,
    platform    text NOT NULL,
    source_id   text NOT NULL,
    kind        text NOT NULL,
    items       bigint NOT NULL,
    positive    bigint NOT NULL,
    negative    bigint NOT NULL,
    neutral     bigint NOT NULL,
    mixed       bigint NOT NULL,
    complaints  bigint NOT NULL,
    urgent      bigint NOT NULL,
    score_sum   double precision NOT NULL,
    PRIMARY KEY (day, platform, source_id, kind)
);

-- Traceability, erasure ---------------------------------------------------------
CREATE TABLE analysis.model_changes (
    id           bigserial PRIMARY KEY,
    route        text NOT NULL,
    from_model   text,
    to_model     text NOT NULL,
    from_prompt  text,
    to_prompt    text NOT NULL,
    taxonomy     text NOT NULL,
    changed_at   timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE analysis.erasures (
    request_id   text PRIMARY KEY,
    subject      text NOT NULL,   -- author | source
    received_at  timestamptz NOT NULL DEFAULT now(),
    done_at      timestamptz,
    stats        jsonb
);
CREATE TABLE analysis.erased_subjects (
    subject_type  text NOT NULL,  -- author | source
    subject_key   text NOT NULL,  -- hex author key or source id
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (subject_type, subject_key)
);

-- Read-only views for the analytics agent ---------------------------------------
-- Each client connects with its own login role (analysis.client_roles); the
-- security-barrier views below show a client the mentions of the entities it
-- tracks, anywhere, and everything from the sources it monitors. Agent roles
-- get SELECT on these views only, never on the tables: a view reads the
-- tables with its owner's rights, while current_user stays the agent's
-- login role, which the agent's SQL cannot change.
CREATE SCHEMA IF NOT EXISTS agent;

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

-- Texts for search and example retrieval: phones and e-mails masked, no
-- author. search_norm matches the full-text index expression:
--   WHERE to_tsvector('simple', search_norm) @@ plainto_tsquery('simple', analysis.ar_normalize('...'))
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
CREATE VIEW agent.rollup_entity_topic_daily WITH (security_barrier) AS
SELECT * FROM analysis.rollup_entity_topic_daily x WHERE EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_entities ce ON ce.client_id = r.client_id
                 WHERE r.role_name = current_user AND ce.entity_id = x.entity_id);
CREATE VIEW agent.rollup_topic_daily WITH (security_barrier) AS
SELECT * FROM analysis.rollup_topic_daily x WHERE EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_sources cs ON cs.client_id = r.client_id
                 WHERE r.role_name = current_user AND cs.source_id = x.source_id);
CREATE VIEW agent.rollup_source_daily WITH (security_barrier) AS
SELECT * FROM analysis.rollup_source_daily x WHERE EXISTS (SELECT 1 FROM analysis.client_roles r JOIN analysis.client_sources cs ON cs.client_id = r.client_id
                 WHERE r.role_name = current_user AND cs.source_id = x.source_id);
CREATE VIEW agent.entities WITH (security_barrier) AS
SELECT e.entity_id, e.name, e.name_ar, e.type, ce.role
FROM analysis.entities e
JOIN analysis.client_entities ce ON ce.entity_id = e.entity_id
JOIN analysis.client_roles r ON r.client_id = ce.client_id AND r.role_name = current_user
WHERE e.status = 'active';
CREATE VIEW agent.model_changes AS SELECT * FROM analysis.model_changes;

-- +goose Down
DROP SCHEMA agent CASCADE;
DROP SCHEMA analysis CASCADE;
