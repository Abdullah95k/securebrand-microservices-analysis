# sb-analysis: notes on the content contract (Part A)

*For the master session. From the code at `Abdullah95k/securebrand-microservices-analysis`, branch `claude/jolly-bell-mb5sct`, commit `bf32dd9`, 5 November 2026 (Iraq time). All examples are synthetic.*

## 1. Who designed `securebrand.content.v1` in this repository, and when

- **Designed by:** the analysis build session (this one), as the input contract of sb-analysis. The owner did not design it, and no other session reviewed it.
- **When:** committed on 1 November 2026 at 03:56 Iraq time (`d02c925`, "Add the canonical content contract and analysis results contract"). It was later documented in `docs/contracts/content-v1.md`.
- **Why it exists:** the analysis needed one platform-neutral input before any normalization service existed. It was meant as a draft for the normalizers to publish, not as a finished platform contract.
- **What it is:**
  - one `ContentItem` for posts, comments and replies;
  - `MediaReady` and `MediaProcessed` for the media handoff;
  - `Erasure`;
  - enums `Platform`, `ContentKind`, `AuthorType`, `ContentStatus`, `MediaKind`, `ProcessStatus`;
  - topics `content.item.v1`, `content.media.ready.v1`, `content.media.processed.v1`, `content.erasure.v1`.

## 2. Who uses it today

| Question | Answer |
|---|---|
| Does any collector publish it? | **No.** fbs publishes only `fb.*`. In this repository, nothing produces `content.item.v1` or `content.media.ready.v1`. I can't see the igs, tks or yts repositories, so I can't confirm what they publish. 05-unified-schema-plan.md lists `content.item.v1` among igs's topics. If igs publishes its own message there, it is a **different message under the same topic name**, and it must not reach sb-analysis. |
| Does sb-analysis read it? | **Not by default.** `analysis.inputs` is `[fb]`, so sb-analysis reads `fb.post.v1`, `fb.comment.v1`, `fb.media.ready.v1` and `fb.erasure.v1` through its Facebook adapter (`internal/analysis/adapter`). The `content` input exists in code but is switched off. **It must stay off until the master publishes v1.** |
| What else uses it? | (1) `analysis.proto` imports its enums `Platform`, `ContentKind` and `MediaKind`. (2) sb-analysis's media acknowledgement code can answer on `content.media.processed.v1` (only for `content.*` input, which is off). (3) This repository's **copy** of fbs's topic and schema lists (`internal/topics/topics.go`, `schemas.go`, `configs/topics.yaml`) includes the `content.*` and `analysis.*` topics. That is the risk in section 3. |
| Is it registered in a Schema Registry? | **sb-analysis never registers schemas.** However, `fbctl schemas apply` built from this repository's copy would register `content.*` and `analysis.*` subjects. **The pilot's registry does hold them** (confirmed 6 Nov; see below). |

**Confirmed by the owner's check on 6 November (00:58 Iraq time).** The pilot's shared Schema Registry already holds 9 subjects from this repository's copy, almost certainly from a `make pilot-up` run from this copy around 1 November:
- `content.item.v1-value`, `content.media.ready.v1-value`, `content.media.processed.v1-value`, `content.erasure.v1-value`;
- the file subject `securebrand/content/v1/content.proto`;
- `analysis.result.v1-value`, `analysis.media.v1-value`, `analysis.author.v1-value`, `analysis.topic.v1-value`.

Nothing produces to those topics. This happened before the owner's standing instruction (5 Nov); nothing has been registered since.

**Why it matters:** the master's v1 is planned under the same package name, `securebrand.content.v1`. Registering it against these subjects in `BACKWARD_TRANSITIVE` mode would **fail**, because the drafts are incompatible.

**Before the master registers v1 on the pilot,** one of these is needed:
- delete these 9 subjects (they are unused); this needs the owner's approval at the time;
- or use subject or topic names that don't collide.

Until then, leave them as they are.

**The owner's check (read-only, on the pilot server, prints only subject names):**

```bash
curl -s http://127.0.0.1:18081/subjects
```

Any name starting with `content.` or `analysis.` came from this repository's copy. Leave the subjects as they are, and tell the master. Deleting subjects needs the owner's approval.

## 3. Risk: the same package name

Draft A (here) and draft B (igs) are both `securebrand.content.v1` and are incompatible. Until the master publishes v1:
- sb-analysis keeps `inputs: [fb]`, so it never decodes `content.*`;
- nobody should run `fbctl schemas apply` or `make pilot-up` from this repository. `make pilot-up` here refuses anyway, because `phase0/proxies.yaml` isn't in this checkout;
- once the master's v1 exists, this repository's `content.proto` is **retired, not evolved**. The master's v1 takes the name, and `analysis.proto` switches to the master's enums (section 5).

## 4. On 05-unified-schema-plan.md, section 2

### What I agree with

1. **Separate entity messages with one shared envelope** (account, account snapshot, post, comment, metrics, media, discovery candidate, coverage). This matches the owner's "posts and comments in separate tables" decision and the ClickHouse model. A single `ContentItem` mixes things that change at different rates.
2. **Keep the ID scheme** `<platform>:<kind>:<native id>`, with text native IDs and short platform codes `fb`, `ig`, `tt`, `yt`. `x` and `tg` stay reserved but unused.
3. **Comments keyed by their post**, so a post's comments stay in order. sb-analysis groups a thread's comments into one model request, so it depends on this.
4. **One contract repository** with a conformance kit, generated Go code and synthetic sample events.
5. **A migration period:** sb-analysis keeps working on `fb.*` until the new topics flow.

### What I would change

1. **Don't derive `ContentItem` as a shared topic.** Keep it only as sb-analysis's internal model (`core.Item`), filled by an adapter on sb-analysis's side that reads `content.post.v1`, `content.comment.v1`, `content.media.ready.v1` and `content.media.processed.v1` directly.
   - **Why:** a derived topic means publishing the same data twice (standard E1), and it needs a component to own it.
   - **This is the second option in section 2.3, and I recommend it.**
   - Retire `content.item.v1` and this repository's `content.proto`.
2. **The kind in an ID is the entity kind, never a subtype.**
   - Use `fb:post:…`, `fb:comment:…` (replies too), `fb:account:…`, `fb:media:…`; reel, reply and story go in a separate `kind` or `subtype` field.
   - sb-analysis already does this for replies (`fb:comment:` at every depth).
   - IDs then stay stable if a subtype is re-classified.
3. **One account kind for every platform:** `fb:account:<id>`, not `fb:page:<id>`. sb-analysis's Facebook adapter writes `fb:page:<id>` today. That's free to change now, because no production analysis data exists.
4. **One platform code set everywhere.** Use the IDs' codes (`fb`, `ig`, `tt`, `yt`) in the envelope too, not `facebook`/`instagram` in one place and `fb`/`ig` in another. sb-analysis uses both today (codes in IDs, names in its tables and enum).
5. **Fields sb-analysis needs that the plan's lists don't name yet:**
   - **On comments:**
     - **`author_key`** (the HMAC), on every comment that has an author;
     - **`is_by_owner`** (the post's owner or creator wrote it);
     - **`author_type`** (person or page/channel).

     The bot detection needs all three: it excludes the page's own replies and needs a stable key per author.
   - **`thread` and `parent`:** a reply needs both `post_id` and `parent_comment_id` (the plan has them).
   - **Mention spans with their type** (user, page, hashtag, URL, with offsets) on posts and comments. sb-analysis removes tagged users before deciding whether a comment has content of its own (a tag-only comment is skipped).
   - **Link titles on posts:** a Facebook link post's text is often empty and the link title carries the topic.
   - **On the media events:**
     - `media.ready`: `owner_id`, `kind`, the storage object (bucket, key, size, content type), `duration_ms`, `alt_text` and **`delete_after`** (the deadline);
     - an **acknowledgement topic** (`media.processed`), with a per-consumer status (processed, failed permanent, failed retryable). fbs's handoff works this way today (`docs/contracts/media-handoff.md`): keep it.
   - **Captions or subtitles,** where a platform provides them (YouTube captions, TikTok subtitles), as text on the post or media. They can replace paid speech-to-text for many videos.
6. **`observed_at` and `version` on every event,** as the envelope plans. sb-analysis keeps the latest observation of an item and re-analyses only when its text changes. It computes its own input hash, so `content_hash` is helpful but not required.
7. **Erasure:** the owner says nothing is deleted (R6), and Q6 recommends keeping the erasure mechanism dormant. sb-analysis already honours an erasure event (it deletes the subject's analysis data). Keep the event in v1, unused, so the dormant path still works.

### What it would cost sb-analysis

| Work | Effort |
|---|---|
| A new adapter: `content.post.v1` + `content.comment.v1` + `content.media.ready.v1` → `core.Item` / `core.MediaJob`; acknowledgements on the new processed topic | 1–2 days, with tests. The pipeline, store and prompts don't change: they work on `core.Item`. |
| `analysis.proto`: import the master's enums instead of this repository's `content.proto`, and change account IDs to `…:account:…` | Half a day. Nobody consumes `analysis.*` yet, so this is the cheapest moment. |
| Remove this repository's `content.proto`, its topics and the `content` input | Half a day |
| Keep the Facebook adapter until fbs publishes `content.*`, then remove it | Removal: an hour |
| Backfill or migration of stored data | None. sb-analysis has no production data. |
| **Total** | **About 3–4 working days**, plus an end-to-end run against the master's synthetic sample events |

## 5. Which input fields each analysis needs

**How the analyses run:** language, sentiment (overall and per entity), entities, topics, intent, toxicity, ads and legal flags all come from **one model call** per group of items in a thread. So their inputs are nearly the same. They differ in which context fields help.

The model sees only:
- each item's text and kind;
- the parent comment's text (for replies);
- the post's text (as context for its comments, up to 1,500 characters);
- the page's display name;
- entity-registry hints for names found in the text.

Everything else is used by sb-analysis's own code:
- grouping;
- duplicates;
- urgency;
- bot detection;
- storage and rollups.

**Key:** **N** = needed (the analysis is wrong or impossible without it); **H** = helpful (better quality or cost); **–** = not used today.

### Text analyses

| Input field (unified name) | Language | Sentiment (overall) | Sentiment (per entity) | Topics | Entities | Intent | Toxicity |
|---|---|---|---|---|---|---|---|
| **id** (`<platform>:<kind>:<native id>`) | N | N | N | N | N | N | N |
| **platform** | N | N | N | N | N | N | N |
| **kind** (post, comment, reply) | H | H | H | H | H | N | H |
| **text** (as received, full) | N | N | N | N | N | N | N |
| **account id** (the page or channel the item belongs to) | – | – | H | H | H | – | – |
| **account display name** | – | H | **N** (resolves "you", "your network" to the page's brand) | H | H | H | – |
| **post id** (for comments: the thread) | H | N (groups the thread; gives context) | N | N | H | N | H |
| **parent comment id** (replies) | – | H | H | H | – | N (answer or question) | H |
| **post text** (for comments, via the post's event) | – | H | H | **N** for short comments ("وين الكهرباء؟") | H | H (giveaway posts → `giveaway_entry`) | H |
| **parent comment text** (via its event) | – | H | H | H | – | H | H |
| **published_at** | – | – | – | – | – | – | – |
| **observed_at**, **version** | – | – | – | – | – | – | – |
| **status** (visible, deleted, edited) | – | – | – | – | – | – | – |
| **mentions with type and offsets** | – | – | H | – | H (tagged private users are dropped) | H (tag-only comments are `tagging` and skipped) | – |
| **hashtags** | – | – | – | H (stored; not sent to the model today: they're usually in the text) | H | – | – |
| **links** (URL, title, domain) | – | – | – | H (title, for link posts) | H | – | – |
| **language stated by the platform** | H (stored; not used by the model today) | – | – | – | – | – | – |
| **author_key** (HMAC) | – | – | – | – | – | – | – |
| **is_by_owner** | – | – | – | – | – | – | – |
| **metrics** (likes, views, shares) | – | – | – | – | – | – | – |
| **raw platform IDs, author name or picture** | – | – | – | – | – | – | – |

### Pipeline mechanics (sb-analysis's own code, all analyses)

| Input field (unified name) | Role |
|---|---|
| **id** | Needed. |
| **platform** | Needed. |
| **account id** | Needed: urgency (urgent sources) and rollups. |
| **post id** | Needed: grouping and duplicates. |
| **parent comment id** | Needed: duplicates and context. |
| **published_at** | Needed: storage partition, rollup day, and timing for bot detection. |
| **observed_at**, **version** | Needed: keeps the latest observation; edits are re-analysed. |
| **status** | Needed: deleted items stay out of new analysis; redacted items are dropped. |
| **mentions with type and offsets** | Helpful: the "has content of its own" check. |
| **hashtags** | Stored. |
| **author_key** | Stored with the result. |
| **is_by_owner** | Stored. |
| **media references** (IDs, in order) | Helpful: links an item to its media results. |

Every other field in the text-analysis table is not used by sb-analysis's own code.

### Image and video analysis

| Input field | Image | Video |
|---|---|---|
| **media id**, **owner id** (post or comment) | N | N |
| **kind** (photo, video, reel, comment photo or video, cover, sticker, GIF, thumbnail) | N (decides whether it is analysed) | N |
| **storage object** (bucket, key, size, content type) | N | N |
| **delete_after** (the file's deadline) | N (the queue works nearest-deadline first) | N |
| **duration_ms** | – | H (frame sampling, the budget estimate) |
| **width, height** | – | – |
| **alt_text** (the platform's automatic description) | H (added to the entity hints) | H |
| **owner's text** (the post or comment text) | H (context; up to 1,000 characters) | H |
| **captions or subtitles** (where the platform has them) | – | H (not consumed today; would replace speech-to-text) |
| **account id**, **platform** | N (rollups, logo mentions per account) | N |

### Other analyses sb-analysis runs

| Analysis | Needed | Helpful |
|---|---|---|
| **Bot and coordinated-account detection** (rules, no model) | `author_key` on every comment; exact `published_at` of the comment and of its post (replies within 60 seconds); `post id`; `account id`; `text`; **`is_by_owner`**; `platform` | author type (page or person) |
| **Ads** (posts mainly) and **legal or policy flags** | `text`, `kind` | account display name; link URLs and titles (affiliate links and codes); media (an ad image) |
| **Urgency** (real-time analysis of a tracked brand's mentions) | `text`, `account id` | – |
| **Duplicate collapsing** (identical comments of a thread analysed once) | `post id`, `parent comment id`, `text` | – |

### What collectors must provide for sb-analysis

| Category | Fields |
|---|---|
| **Must provide** | id; platform; kind; text in full; account id; post id on comments; parent comment id on replies; published_at; observed_at; version; status. For bots: `author_key` and `is_by_owner` on every comment with an author. For media: the media events above. |
| **Should provide** | account display name; mentions with type and offsets; links with titles; hashtags; the platform's stated language; alt text; captions or subtitles. |
| **Not needed by analysis** | metrics; reactions; raw author IDs, names and pictures; profile links. Other parts of the platform need these: dashboards and entity linking. |

## REQ-006: answers to the master session's questions (6 November 2026, Iraq time)

*Facts from the code at commit `6692739`. Where a fix is proposed, it is a proposal for the owner, not built.*

### 1. Overlap with the platform: what the tables are for

**Entities**

| Tables | What they are for today | Keep as sb-analysis working state? |
|---|---|---|
| `entities`, `entity_aliases` | The registry of things **mentioned in text** (brands, products, organisations, public figures, positions, places, events, outlets). Aliases are normalized spellings in Arabic, Kurdish, English and Arabizi, with an `ambiguous` flag for bare titles ("رئيس الوزراء"). It links model output to IDs, gives the model hints, and drives the `@<entity>` topics. | **Keep a synced copy.** Matching text needs the aliases and ambiguity flags locally, on every item. **Proposal:** the platform owns entity IDs, names and types (09 section 7). sb-analysis keeps the aliases as its own working data, under the platform's IDs. |
| `entity_candidates` | Names the model found that aren't in the registry yet, with counts and 2 masked example items. The AI curation (every 6 hours, the owner's decision 5) approves, merges or rejects them. | **Keep** (it's a work queue), but an approval should become a **proposal to the platform's entity layer** instead of sb-analysis creating the entity itself. That needs the owner's decision, because decision 5 asked for automatic approval by AI. |

**Clients**

| Tables | What they are for today | Keep as sb-analysis working state? |
|---|---|---|
| `clients`, `client_entities` | Client names, and which entities each client tracks (own, competitor, watch). **The pipeline uses this:** an entity in `client_entities` is "tracked", so a mention of it goes to the **real-time lane**, can be **escalated** to the stronger model, and is never collapsed with its duplicates. | **No:** the client app owns tenants and watchlists. sb-analysis needs only a **feed of tracked entity IDs and hot sources** (for example, a compacted Kafka topic or a read-only view), kept as a local cache. |
| `client_sources` | Sources (pages) a client monitors. Used only to scope the read-only agent views. | **No** (the client app's) |
| `client_roles` | Maps a Postgres login role to a client, for the read-only `agent.*` views with row filtering | **No.** It goes when ClickHouse row policies or the client app replace the agent views. |

**Authors and rollups**

| Tables | What they are for today | Keep as sb-analysis working state? |
|---|---|---|
| `authors` | Bot-detection state per pseudonymous author: behaviour features over 30 days, score, level, reasons, and a person's override. Not a profile: no names, no IDs. | **Keep** (bot detection's working state). Its output is `analysis.author.v1`. |
| `author_overrides` | The log of people's corrections (who, when, why, before and after) | **Keep** (the owner's decision 12), or move it to the client app if corrections are made there |
| `rollup_source_daily`, `rollup_entity_daily`, `rollup_entity_topic_daily`, `rollup_topic_daily`, `rollup_flag_daily`, `rollup_dirty` | Daily aggregates per Iraq day (items and sentiment per source, entity, topic and flag; bot items) for the pilot's dashboards and the read-only agent views | **No.** They become redundant once the platform's ClickHouse rollups exist. Keep them only for the pilot. |

### 2. Erasure: what publishes `fb.erasure.v1`

- **Publisher:** only fbs, from its eraser (`internal/gdpr/erasure.go`, running inside `fbs-scheduler`). It publishes one `ErasureEvent` per processed erasure request.
- **How a request is created** (fbs `main`, read on 6 Nov). All paths need fbs's **`admin` API scope**:
  1. `POST /erasure-requests` (an author or a page);
  2. `DELETE /pages/{id}?purge=true`;
  3. a cascade inside the eraser: an author erasure also erases, as pages, any profiles of that person that fbs's mention checker had added.
- **Nothing creates one automatically** from collected data.
- **What sb-analysis does with it:**
  - it deletes the subject's analysis data: texts, analyses, entity mentions, media analyses, bot state and the correction log;
  - it marks the queue entries;
  - it records the request in `analysis.erasures`.
- **A second, dependent path:** fbs later publishes matching comments as `redacted` (suppression of erased authors). sb-analysis then drops their text and results. That path also exists only after an erasure.
- **Gate before 6 November:** fbs's admin scope only.

**Built on 6 November, with the owner's approval:** a setting `analysis.erasure.mode`.

| Mode | What it does |
|---|---|
| **`record`** (default) | Stores the request with its subject; **deletes, redacts and suppresses nothing** (a later `redacted` comment keeps its text here) |
| `off` | Ignores the request |
| `apply` | Deletes, as before |

- **Applying a recorded request later:** only on the owner's order, with `sb-analysis erasures apply --request <id> --yes-delete`.
- **Listing requests:** `sb-analysis erasures list`.

### 3. Tagged names and the AI provider

**Before 6 November:** `analysis.privacy.mask_pii` masked only e-mails and phone numbers, so the names of **tagged users** reached the provider. Facebook writes them into the comment text and marks them in `text_ranges` as `User`. The owner's 1 November decision was about authors, so this was a gap, not an intended choice.

**Built on 6 November, with the owner's approval** (`analysis.privacy.mask_tagged_users`, default on):
- **Where names are hidden:** every tagged person's name is written as `@user` in everything sent to a provider:
  - the items;
  - the post shown as context;
  - a parent comment;
  - a media owner's text;
  - the entity-curation examples;
  - the evaluation gold set.
- **What stays readable:** tagged names that are unambiguous registry entities (brands, public figures).
- **Storage:** the stored text is unchanged (contract rule 1). The tag positions are stored beside it (`content_items.tag_spans`, migration `00003`).
- **A bug fixed on the way:** Facebook counts tag positions in **UTF-16 units**, but the analysis read them as characters, so after an emoji a span would have landed in the wrong place. Positions are now converted exactly, and a position that misses the tagged name falls back to the name itself. **The v1 contract should state that mention offsets are UTF-16 units.**
- **What it can't do:** names people type without tagging can't be found reliably, and are still sent.

### 4. Raw author fields in v1 (the owner's decision D-003)

**What sb-analysis uses today:** only `author_key` (the HMAC fbs computes with `privacy.author_hmac_key`), for bot detection, `author_bot` on results, and erasure matching. It never receives or sends author names or raw IDs.

**What changes if v1 carries raw author IDs and names:**
1. **The adapter reads `author_id` and never forwards the name anywhere.** Names are not stored, not sent to a provider, and not logged. Raw IDs stay out of provider requests too (they aren't part of the model's input today, and that stays).
2. **sb-analysis computes its own pseudonymous key:** `HMAC(own secret, platform + ":" + author_id)`, with a secret of its own (for example `SB_ANALYSIS__AUTHOR_KEY_SECRET`).
   - **The shared HMAC key with fbs is then no longer needed.**
   - If v1 also carries the collector's `author_key`, sb-analysis can keep using that instead. Either works; one is enough.
3. **Joins on the platform side.** The platform will store raw author IDs, so `AuthorFlag` should carry the platform's author ID too, as an **added** field (`author_id`, restricted). `author_key` stays.
   - The same applies to the erasure path, if it is ever used.
   - Without this, the platform can't join bot flags to its comments unless it computes sb-analysis's key.
4. **Effort:** about 1 day (adapter, key derivation, the added `AuthorFlag` field, tests). No stored data has to change, because no production data exists.

### 5. What the alert engine will need

**Result fields** (`analysis.result.v1`; field details in analysis-outputs.md):

| Purpose | Fields |
|---|---|
| Identity and routing | `content_id`, `platform`, `kind`, `source_id`, `thread_id`, `created_at` |
| Sentiment drops | `sentiment.label`, `sentiment.score`, `sentiment.calibrated_confidence` |
| Brand and entity mentions | `entities[]` (`entity_id`, `type`, `sentiment`, `aspect`, `origin`) and the `@<entity_id>` entries in `topics` |
| Topic rules | `topics` (hierarchical IDs: a rule on `services.electricity` also matches its sub-topics by prefix) |
| Complaint, churn and purchase signals | `intent` (`complaint`, `churn_threat`, `purchase_intent`…), `emotion` |
| Risk | `toxicity`, `legal[]` (allegations for review), `spam`, `ad_type` |
| Attention | `urgent` (the **model's** judgement that an item needs attention fast, for example a safety complaint) |
| Trust | `author_bot` at analysis time (current level from `analysis.author.v1`), `refused` (skip such items) |
| Versions | `provenance.analysis_version` and `analyzed_at`. **An item can get a second result later** (an escalation to the stronger model, or an edit), so the engine must update or retract an alert by `content_id` and `analysis_version`. |

**Other topics:**
- `analysis.media.v1`: logos and entities in images;
- `analysis.author.v1`: an author becomes `likely` or `confirmed`;
- `analysis.topic.v1`: a newly approved topic (input for "new topic" alerts).

**Latency** (estimates, not measured; the collector's revisit interval comes on top):

| Lane | Latency after sb-analysis ingests the item |
|---|---|
| Real-time | ~30–60 seconds: a 20-second wait for a thread's siblings (`analysis.realtime.wait`), then one model call of a few seconds |
| Batch | submitted every 10 minutes; most provider batches finish within an hour; at most 24 hours |
| Escalation | a later second result: batch for normal items; real time when the original item was urgent |

**How urgent items are marked today:**

| Step | What happens |
|---|---|
| **Routing (sb-analysis's code)** | An item goes to the real-time lane when its text contains an alias of a **tracked** entity (`client_entities`) or its source is in `analysis.urgent.sources`. |
| **On the result** | This shows only as `provenance.lane = "realtime"`. The model's own judgement is the separate `urgent` boolean. |
| **What isn't on the result** | There is no field that says **why** an item went real time. A fallback to the backup provider can also be real time, marked `provenance.fallback = true`. |
| **Proposal** (additive, half a day) | A `priority` field on `ItemAnalysis` (`tracked_entity`, `urgent_source`, `normal`) and the matched `entity_id`s, so the alert engine needn't infer it |

**For alerts within 1–5 minutes on everything,** every item would need the real-time lane: about +$1,800 a month per 1,000 accounts (analysis-operations.md, section 13). Otherwise only watchlist (hot) items go in real time. The owner decides.
