# sb-analysis: what it produces (Part B)

*For the master session. From `api/proto/securebrand/analysis/v1/analysis.proto` and the code that fills it (`internal/store/results.go`, `media.go`, `authors.go`, `topics.go`), commit `bf32dd9`, 5 November 2026 (Iraq time). Machine-readable copy: `analysis-outputs.yaml`. Every example is synthetic.*

## 1. Overview

| Topic | Message | Key | When it is published | Partitions / retention (topic list) |
|---|---|---|---|---|
| `analysis.result.v1` | `ItemAnalysis` | `content_id` | Each new **current** text analysis of a post, comment or reply | 96 / 7 days |
| `analysis.media.v1` | `MediaAnalysis` | `media_id` | Each image or video analysis | 24 / 7 days |
| `analysis.author.v1` | `AuthorFlag` | the author key in hex | When an author's bot level **changes**: daily scoring, or a person's correction | 12 / 30 days |
| `analysis.topic.v1` | `TopicUpdate` | `topic_id` | When the topic list changes: approved, renamed, merged, rejected | 3 / 30 days |
| `fb.media.processed.v1` (fbs's topic) | fbs's `MediaProcessed` | `media_id` | The acknowledgement of each media file, so fbs can delete it | fbs's setting |

How the records are published:
- **Delivery:** at least once, through a transactional outbox in Postgres. A record is published only after its result is committed. **Consumers must ignore repeats by `event_id`.**
- **Headers:** every record carries a message-type header and a content-type header (protobuf).
- **Not published:** audit results (a 0.02% sample re-analysed by the strongest model to watch quality) are stored in sb-analysis's Postgres only.

**"Always"** means sb-analysis always fills the field. Proto3 can't tell an unset `false` or `0` from a real one, so a boolean that is "always" set can be `false` either way.

## 2. `ItemAnalysis` (`analysis.result.v1`)

| Field | Meaning | Type | Synthetic example | Always or sometimes |
|---|---|---|---|---|
| `event_id` | Unique per result: `<content_id>:<analysis_version>` | text | `fb:comment:900000000000001:2` | always |
| `content_id` | The item: `<platform>:<kind>:<native id>` (replies use `comment`) | text | `fb:comment:900000000000001` | always |
| `platform` | Platform of the item | enum `securebrand.content.v1.Platform`: FACEBOOK, INSTAGRAM, TIKTOK, X, TELEGRAM, YOUTUBE | `PLATFORM_FACEBOOK` | always |
| `kind` | Post, comment or reply | enum `ContentKind`: POST, COMMENT, REPLY | `CONTENT_KIND_COMMENT` | always |
| `source_id` | The page, account or channel the item belongs to | text | `fb:page:100000000000001` | always |
| `thread_id` | The post the comment belongs to | text | `fb:post:200000000000001` | sometimes (comments and replies; empty for posts) |
| `author_key` | Pseudonymous author key (HMAC from the collector) | bytes | `0x5f2c…` (32 bytes) | sometimes (empty for posts and for comments without an author) |
| `created_at` | When the item was published (UTC) | timestamp | `2026-11-04T09:12:00Z` | always |
| `lang` | Language | enum as text: `ar`, `ckb` (Sorani), `kmr` (Kurmanji), `en`, `tr`, `fa`, `other`, `none` | `ar` | always |
| `dialect` | Arabic dialect | enum as text: `iraqi`, `gulf`, `levantine`, `egyptian`, `msa`, `other`, `none` | `iraqi` | always (`none` when not Arabic) |
| `script` | Alphabet, computed by code (not the model) | enum as text: `arabic`, `latin`, `mixed`, `none` | `arabic` | always |
| `code_switch` | Mixes languages | bool | `false` | always |
| `sentiment` | Overall sentiment | `Sentiment` (section 6) | label `negative`, score `-0.6` | always |
| `entities` | Brands, organisations, public people, positions, places, events and outlets mentioned, with sentiment toward each | list of `EntityMention` (section 6) | one: `examplebrand`, brand, negative, aspect `network` | sometimes (often empty) |
| `topics` | Topic IDs from the topic list, hierarchical (`services.electricity` under `services`), plus `@<entity_id>` for each linked entity the item is about | list of text | `["services.electricity", "@examplebrand"]` | almost always (up to 10 list topics plus the `@` entity topics; empty when the model gave none that is valid) |
| `keywords` | Content words: computed by code for comments, by the model for posts | list of text | `["كهرباء", "قطع"]` | sometimes |
| `intent` | What the author is doing | enum as text: complaint, praise, question, request, suggestion, purchase_intent, churn_threat, recommendation, information, giveaway_entry, greeting_religious, tagging, spam_ad, other | `complaint` | always |
| `emotion` | Dominant emotion | enum as text: anger, joy, sadness, fear, surprise, disgust, trust, anticipation, none | `anger` | always |
| `toxicity` | Toxicity level and kinds | `Toxicity` (section 6) | level `none` | always |
| `spam` | Spam | bool | `false` | always |
| `sarcasm` | Sarcastic | bool | `false` | always |
| `urgent` | Needs attention fast (for example, a safety complaint) | bool | `false` | always |
| `private_person_mentions` | How many private persons are mentioned (they are never stored as entities) | int32 | `0` | always |
| `refused` | Every provider refused the item. The other fields are then neutral defaults: sentiment neutral, lang `other`, intent `other`. | bool | `false` | always (true is rare) |
| `provenance` | How the result was made | `Provenance` (section 6) | | always |
| `ad_type` | Advertising type | enum as text: `affiliate`, `own`, `paid`, `hidden`, `sale`; empty when not an ad | `paid` | sometimes |
| `legal` | Possible legal or policy breaches, **for human review (allegations, not findings)** | list of text: hate_speech, threat, incitement, terrorism, child_safety, sexual_content, graphic_violence, self_harm, drugs, weapons, gambling, regulated_goods, fraud_scam, unlicensed_finance, counterfeit, privacy, defamation, misinformation, extortion, religious_offense, other_illegal | `["fraud_scam"]` | sometimes (usually empty) |
| `author_bot` | The author's bot level **when the item was analysed**; later changes come on `analysis.author.v1` | enum as text: empty, `suspected`, `likely`, `confirmed` | `suspected` | sometimes |

## 3. `MediaAnalysis` (`analysis.media.v1`)

| Field | Meaning | Type | Synthetic example | Always or sometimes |
|---|---|---|---|---|
| `event_id` | Unique per result: `<media_id>:<analyzed_at, RFC 3339>` | text | `00000000-0000-0000-0000-000000000001:2026-11-04T09:15:02Z` | always |
| `media_id` | The collector's media ID (a UUID from fbs) | text | `00000000-0000-0000-0000-000000000001` | always |
| `owner_content_id` | The post or comment the media belongs to | text | `fb:post:200000000000001` | always |
| `platform` | Platform | enum `Platform` | `PLATFORM_FACEBOOK` | always |
| `source_id` | The page, account or channel | text | `fb:page:100000000000001` | always |
| `kind` | Image, video, GIF, sticker or audio | enum `securebrand.content.v1.MediaKind` | `MEDIA_KIND_IMAGE` | always |
| `logos` | Brand logos seen | list of `Logo` (section 6) | `examplebrand`, confidence `0.9` | sometimes |
| `text_in_image` | Text visible in the image (OCR by the model) | text | `عرض خاص` | sometimes |
| `description` | A short description of what is shown | text | `A shop front with a sale banner` | always |
| `labels` | Objects and scene labels | list of text | `["shop", "banner"]` | sometimes |
| `people_count` | People visible (never identified from faces) | int32 | `2` | always |
| `sentiment` | Sentiment of the image's message (no calibrated confidence for media) | `Sentiment` | label `positive`, score `0.5` | always |
| `entities` | Entities shown, written or spoken, with sentiment | list of `EntityMention` (origin `image` or `video`) | | sometimes |
| `topics` | Topic IDs | list of text | `["economy.prices"]` | always |
| `transcript` | Speech-to-text of a video's audio | text | `example transcript` | sometimes (videos, only when speech-to-text is configured; off by default) |
| `transcript_lang` | The transcript's language | text | — | **never set today** (a gap; see analysis-gaps.md) |
| `provenance` | How the result was made. **`analysis_version` is always 1 and `input_hash` is empty for media** | `Provenance` | | always |
| `ad_type` | As for items | text | `own` | sometimes |
| `legal` | As for items | list of text | `[]` | sometimes |

Which media is analysed (`analysis.media.kinds`): photos, videos, reels, comment photos, comment videos, covers and images. Stickers, GIFs, profile pictures and thumbnails are acknowledged without analysis.

## 4. `AuthorFlag` (`analysis.author.v1`)

| Field | Meaning | Type | Synthetic example | Always or sometimes |
|---|---|---|---|---|
| `event_id` | `<author key hex>:<unix milliseconds>` | text | `5f2c…e1:1762246800000` | always |
| `platform` | Platform | enum `Platform` | `PLATFORM_FACEBOOK` | always |
| `author_key` | The pseudonymous author key | bytes | `0x5f2c…` | always |
| `bot` | New level | enum as text: empty (no longer flagged), `suspected` (score ≥45), `likely` (≥70), `confirmed` (a person marked it) | `likely` | always (empty is a real value) |
| `score` | Behaviour score, 0–100 | int32 | `72` | always |
| `reasons` | The behaviour behind the score | list of text: burst, volume, repetition, coordinated, network, fast_replies, spam, breadth, no_sleep | `["coordinated", "repetition"]` | sometimes |
| `scored_at` | When it was decided (UTC) | timestamp | `2026-11-04T21:00:00Z` | always |

A person's correction ("not a bot") publishes a record with `bot` empty. The correction log (who, when, why) stays in sb-analysis's Postgres (`author_overrides`) and is not published.

## 5. `TopicUpdate` (`analysis.topic.v1`)

| Field | Meaning | Type | Synthetic example | Always or sometimes |
|---|---|---|---|---|
| `event_id` | `<topic_id>:<taxonomy_version>` | text | `services.electricity.outages:tax-v1.3` | always |
| `topic_id` | The topic | text | `services.electricity.outages` | always |
| `parent_id` | Its parent topic | text | `services.electricity` | sometimes (empty for a top-level topic) |
| `name_en` | English name | text | `Electricity cuts` | always |
| `name_ar` | Arabic name | text | `انقطاع الكهرباء` | sometimes |
| `status` | `active` (new or renamed), `merged`, `rejected` | text | `merged` | always |
| `merged_into` | For `merged`: the topic that replaces it | text | `services.electricity` | sometimes |
| `taxonomy_version` | The topic-list version this change created | text | `tax-v1.3` (the first list is `tax-v1`; each change adds `.N`) | always |
| `updated_at` | When (UTC) | timestamp | `2026-11-04T12:00:00Z` | always |

When a topic is **merged**, sb-analysis rewrites the topic arrays in its own Postgres. A consumer that keeps results should map `topic_id` to `merged_into` instead of rewriting history (section 8). A **rejected** topic should be dropped from views.

## 6. Nested messages

**`Sentiment`**

| Field | Meaning | Type | Example | Always or sometimes |
|---|---|---|---|---|
| `label` | positive, negative, neutral or mixed | text | `negative` | always |
| `score` | −1 (very negative) to 1 (very positive) | float | `-0.6` | always |
| `confidence` | The model's own confidence, 0–1 | float | `0.8` | always on items; **not set inside entity mentions** |
| `calibrated_confidence` | Mapped through the model's calibration, 0–1. It equals `confidence` until a calibration file exists. | float | `0.8` | items only |

**`EntityMention`**

| Field | Meaning | Type | Example | Always or sometimes |
|---|---|---|---|---|
| `entity_id` | Registry ID | text | `examplebrand` | sometimes (empty when not in the registry yet) |
| `name` | Registry name, or the text's form when unlinked | text | `Example Brand` | always |
| `type` | brand, product, organization, person_public, position, place, event, media_outlet or other (never person_private) | text | `brand` | always |
| `sentiment` | Toward this entity: **label and score only** | `Sentiment` | `negative`, `-0.7` | always |
| `aspect` | What the sentiment is about | text: price, quality, network, internet, customer_service, delivery, billing, app, staff, availability, offers, reliability, safety, management, general, other | `network` | always |
| `origin` | text, image or video | text | `text` | always |

**`Toxicity`**

| Field | Type | Example | Always or sometimes |
|---|---|---|---|
| `level` (none, mild, severe) | text | `none` | always |
| `hate` | bool | `false` | always |
| `threat` | bool | `false` | always |
| `harassment` | bool | `false` | always |

**`Logo`**

| Field | Type | Example | Always or sometimes |
|---|---|---|---|
| `entity_id` | text | `examplebrand` | sometimes |
| `name` | text | `Example Brand` | always |
| `confidence` (0–1) | float | `0.9` | always |

**`Provenance`**

| Field | Meaning | Type | Example | Always or sometimes |
|---|---|---|---|---|
| `provider` | Provider name in sb-analysis's config | text | `anthropic` | always |
| `model` | Model ID | text | `claude-haiku-5-5` | always |
| `prompt_version` | Prompt version | text | `p5` | always |
| `taxonomy_version` | Topic-list version used | text | `tax-v1` | always |
| `schema_version` | Answer-format version | text | `s5` | always |
| `route` | comment, post, image, video, escalation (audit is not published) | text | `comment` | always |
| `lane` | batch (half price, minutes to 24 hours) or realtime | text | `batch` | always |
| `analyzed_at` | When (UTC) | timestamp | `2026-11-04T10:02:00Z` | always |
| `input_hash` | SHA-256 of the analysed input; equal hashes are never analysed twice | bytes | 32 bytes | items: always; **media: empty** |
| `analysis_version` | Counter per content ID; increases with each new current result | uint32 | `2` | items: always (≥1); **media: always 1** |
| `purpose` | `current` (audits are not published) | text | `current` | always |
| `fallback` | Came from the route's backup provider | bool | `false` | always |

## 7. Versioning and re-analysis

**What a result records:** every result records `model`, `prompt_version`, `schema_version` and `taxonomy_version`, so any result can be traced to exactly what produced it.

**When a text result is created** — each event below writes a **new record**:

| Event | `analysis_version` | `event_id` | Previous result |
|---|---|---|---|
| The item's first analysis | 1 | new | — |
| **An edit** (the text changes; the input hash differs) | +1 | new | kept in history, no longer current |
| **An escalation** (a tracked brand's mention that the first model was unsure of, sarcastic or mixed, re-analysed by a stronger model) | +1 | new | kept in history, no longer current |
| **A re-analysis** (`sb-analysis reanalyze`) with another model, prompt or topic-list version | +1 | new | kept in history, no longer current |
| The **same** input with the **same** route, model, prompt and topic list (for example, a batch result fetched twice) | unchanged | none | — (duplicate dropped) |
| A late cheaper result after an escalation | unchanged | none | the escalation stays current |

**Text results:**
- sb-analysis's Postgres keeps **every version** (`analysis.analyses`, with `is_current`).
- On Kafka each version is its own record. **The highest `analysis_version` per `content_id` is current.**
- `analysis_version` is a counter kept by sb-analysis's database. If that database were ever rebuilt, the counter would restart, so use (`analysis_version`, `analyzed_at`) as the version.

**Media results:**
- A re-analysis **overwrites** the row in sb-analysis's Postgres and publishes a new record with a new `event_id`.
- `analysis_version` stays 1, so **use `analyzed_at` as the version for media.** (This is a gap; see analysis-gaps.md.)

**Bot flags:** each change is a new record. The latest `scored_at` per author is current.

**The topic list:** each change creates a new `taxonomy_version`.

## 8. How to store them in ClickHouse

### Text results: one row per item and analysis version

sb-analysis gets every text field (language, sentiment, entities, topics, intent, toxicity, flags) from **one model call**, so one row per (item, `analysis_version`) carries one consistent provenance. I don't recommend splitting a result into one row per analysis type: the rows would share the same model, prompt and time anyway. If the master wants a narrow form, a view can unpivot the wide row.

| Table | Engine and key | Contents |
|---|---|---|
| `analysis_item_results` (history) | `ReplacingMergeTree(analyzed_at)`, `ORDER BY (platform, content_id, analysis_version)`, `PARTITION BY toYYYYMM(created_at)` | One row per (item, analysis version). Repeated deliveries collapse; distinct versions stay. Provenance flattened (`provider`, `model`, `prompt_version`, `schema_version`, `taxonomy_version`, `route`, `lane`, `fallback`, `analyzed_at`). Result flattened (`lang`, `dialect`, `script`, `sentiment_label`, `sentiment_score`, `confidence`, `calibrated_confidence`, `intent`, `emotion`, `toxicity_level`, the flags, `ad_type`). `topics`, `keywords` and `legal` as `Array(LowCardinality(String))`. `author_key`, `author_bot`. |
| `analysis_item_latest` | `ReplacingMergeTree(analysis_version)`, `ORDER BY (platform, content_id)` | The current result per item, fed by the same materialized view. Queries use `FINAL`, or `argMax` on the history table. |
| `analysis_entity_mentions` | `ReplacingMergeTree(analysis_version)`, `ORDER BY (entity_id, created_at, content_id, ord)` | One row per (item, entity) of the current version: entity, type, sentiment, score, aspect, origin, plus the item's platform, source, created_at, lang, topics and intent. Brand dashboards then need no join. Media logos and entities go here too, with origin `image` or `video`. |
| `analysis_media_results` (history) | `ReplacingMergeTree(analyzed_at)`, `ORDER BY (platform, media_id, analyzed_at)` | One row per media analysis |
| `analysis_media_latest` | `ReplacingMergeTree(analyzed_at)`, `ORDER BY (platform, media_id)` | The current media result |
| `analysis_author_flags` | `MergeTree`, `ORDER BY (platform, author_key, scored_at)` | Every change, plus a `ReplacingMergeTree(scored_at)` view of the current level. **An item's bot flag at query time is the author's current level** (`author_bot` on the result is only the level at analysis time). |
| `analysis_topics` | `ReplacingMergeTree(updated_at)`, `ORDER BY topic_id` | Every topic with its status and `merged_into`. A dictionary on it maps old topic IDs to current ones **at query time**, so history is never rewritten. |

**Rules:**
- Deduplicate by `event_id` or the table keys above. Kafka delivery is at least once.
- Join results to content by `content_id`. Results don't carry the text.
- Partition by the **item's** month (`created_at`), not the analysis time, so a re-analysis lands next to its item.
