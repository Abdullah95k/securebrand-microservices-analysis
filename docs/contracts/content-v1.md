# Canonical content contract (securebrand.content.v1) and analysis results (securebrand.analysis.v1)

Every normalisation service (Instagram, TikTok, X, Telegram, YouTube, and later Facebook itself) publishes the same small contract. The analysis service ([design](../design/analysis-service.md)) reads it and does not care which platform an item came from.
- **Proto files:** `api/proto/securebrand/content/v1/content.proto` and `api/proto/securebrand/analysis/v1/analysis.proto`.
- **Generated Go:** `gen/go/securebrand/content/v1` and `gen/go/securebrand/analysis/v1`.
- **Wire format:** as for fb.v1 ([kafka-schemas.md](kafka-schemas.md)), plain protobuf with the message name in the `fbs-type` header, registered with the Schema Registry as `BACKWARD_TRANSITIVE`.

| Topic | Key | Value | Producer |
|---|---|---|---|
| `content.item.v1` | thread id (the root post's content id; a post's own id for posts) | `ContentItem` | normalisation services |
| `content.media.ready.v1` | media id | `MediaReady` | normalisation services (after downloading the file) |
| `content.media.processed.v1` | media id | `MediaProcessed` | sb-analysis (acknowledgement) |
| `content.erasure.v1` | request id | `Erasure` | normalisation services, on a data-subject request |
| `analysis.result.v1` | content id | `ItemAnalysis` | sb-analysis |
| `analysis.media.v1` | media id | `MediaAnalysis` | sb-analysis |
| `analysis.author.v1` | hex author key | `AuthorFlag` | sb-analysis (an author's bot level changed) |
| `analysis.topic.v1` | topic id | `TopicUpdate` | sb-analysis (the topic list changed) |

Today the Facebook collector does not publish `content.*`. sb-analysis's built-in Facebook adapter maps `fb.post.v1`, `fb.comment.v1`, `fb.media.ready.v1` and `fb.erasure.v1` to the same model, and acks on `fb.media.processed.v1` (`analysis.inputs: [fb]`).

## ContentItem: rules for producers

- **`content_id`** is `<platform>:<kind>:<native id>` (`ig:comment:1789...`), unique and stable. Platforms: `fb`, `ig`, `tt`, `x`, `tg`, `yt`.
- **`source_id`** is `<platform>:<source kind>:<native id>` (`tg:channel:123`, `yt:channel:UC...`): the page, account or channel the item belongs to.
- **`thread_id` and `parent_id`** are content ids: the post an item belongs to, and its direct parent. Both are empty for posts.
- **Key:** publish on `content.item.v1` keyed by **thread id**, so a post and its comments land in one partition, in order. The analysis service groups a thread's comments with the post as context.
- **Authors:** never send names or platform ids.
  - `author_key` is the HMAC of the platform author id, with the same key as fbs (`privacy.author_hmac_key`), so erasure by author works across services;
  - `author_type` is person or organisation;
  - `author_is_source` is true when the page or channel replies itself.
- **Mentions:** for tagged private persons, send only `type: user` with offset and length, never a name or id. The analysis service uses them to skip comments that only tag friends.
- **Text:** send the text as published (no translation, no cleaning). For videos, put the caption in `text` and the file in `media`.
- **Every observation** may be published. The analysis service computes its own hash of text, kind and media ids:
  - **unchanged re-scrape:** costs nothing;
  - **edit:** re-analysed;
  - **`status: DELETED`:** marks the item deleted (the analysis is kept);
  - **`status: REDACTED`:** removes the text and its analysis.
- **Fields:** only add fields, never renumber. `buf breaking` and the Schema Registry enforce it.

## MediaReady and MediaProcessed

The same lifecycle as Facebook's [media handoff](media-handoff.md):
1. The producer stores the file in S3-compatible storage and publishes `MediaReady` with `bucket`, `key` and `delete_after`.
2. sb-analysis analyses it and answers `MediaProcessed` with `consumer: "analysis"`: `PROCESSED`, `FAILED_PERMANENT`, or `FAILED_RETRYABLE` (republish the file).
3. The producer deletes the file once every required consumer acknowledged, or at `delete_after`.

## Erasure

`Erasure{subject: AUTHOR, author_key}` or `{subject: SOURCE, source_id}`. sb-analysis then:
- deletes the subject's text, analyses, entity mentions and media analyses;
- records the subject, so late items are dropped;
- acknowledges pending media.

## ItemAnalysis: rules for consumers

- **One record per analysis version.** Keep the highest `provenance.analysis_version` per `content_id`; in ClickHouse, use `ReplacingMergeTree(analysis_version)`. A re-analysis (edit, model change, escalation) publishes a higher version.
- **Delivery is at least once.** `event_id` is `<content_id>:<version>`.
- **Provenance** says which `provider`, `model`, `prompt_version`, `taxonomy_version` and `schema_version` produced the record. Use it to filter, compare or mark series breaks. `fallback: true` marks results from a route's fallback provider.
- **`entities`:**
  - per-entity sentiment and aspect;
  - `entity_id` is the registry id, empty when the entity is not linked yet;
  - private persons are never included; only `private_person_mentions` counts them.
- **`author_key`** is included so stores can apply erasure. Treat records as personal data.
- **`topics`** holds topic ids of the dynamic list and `@<entity_id>` for every linked brand, organisation, public figure, position, place or event the item is about. The list changes: on `analysis.topic.v1`, a `merged` topic's id now means `merged_into`, and a `rejected` id should be dropped. `TopicUpdate` records are keyed by topic id, so the latest one per key is the current state.
- **`ad_type`** (`own`, `paid`, `hidden`, `sale`) and **`legal`** (possible breaches) are leads for review, not findings.
- **`author_bot`** is the author's bot level when the item was analysed (`suspected`, `likely`, `confirmed`). The current level arrives on `analysis.author.v1`: apply it to every record with that `author_key`, also earlier ones. An empty `bot` clears the flag.
