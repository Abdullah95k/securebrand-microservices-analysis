# SecureBrand content analysis service (`sb-analysis`)

Status: proposed, first version built (see "What is built" at the end). Written 2026-11-01; cost revision the same day (section 16.1); ads, legal and policy flags, a dynamic topic list and bot detection added the same day (sections 6.1-6.3).
Decisions: [ADR 0004](../adr/0004-analysis-service.md) (one service, canonical contract, Postgres work queue),
[ADR 0005](../adr/0005-llm-provider-strategy.md) (models and providers),
[ADR 0006](../adr/0006-analysis-storage.md) (storage, analytics store, agent access),
[ADR 0007](../adr/0007-topics-flags-bots.md) (dynamic topics, ad and breach flags, bot detection).
Owner-facing summary: [docs/owner/analysis-decisions.md](../owner/analysis-decisions.md).

## 1. Summary

- **One analysis service**, platform-neutral. It reads a small canonical contract (`securebrand.content.v1`). A built-in Facebook adapter reads today's `fb.*` topics, so it runs now without new collectors.
- **One LLM call per group of items returns every field** (language, sentiment, per-entity sentiment, topics, entities, intent, emotion, toxicity, spam, sarcasm). Comments are grouped by post, with the post as context: one request carries one post and up to 40 of its comments.
- **Models are chosen per content type and set in config.** The starting point is Claude Haiku 5.5 through the Batch API for comments and posts, with a stronger model only where the evaluation shows Haiku fails. Two further uses of a stronger model: escalation of low-confidence items that mention a client's tracked brand, and a 0.02% audit sample for drift monitoring.
- **What is not paid for:** answers use a compact format (most of the cost is the model's output), identical comments of a thread are sent once, and the script and comment keywords are computed locally (section 16.1).
- **Also in the same call, at no extra cost when absent:** ad detection (own, paid, hidden, private sale) and possible legal or platform-policy breaches (15 categories, for human review; criticism of the government is never a breach).
- **The topic list grows by itself** (section 6.1): the model proposes missing topics, the service deduplicates them, and a daily check merges synonyms before they join the list. Brands, people, positions and places the item is about become topics too ("@asiacell").
- **Bot and fake-account detection** (section 6.2): a daily behaviour score per author; every item of a flagged author carries the flag.
- **Postgres is the work queue and the system of record.** Text, analyses, entity mentions and daily rollups live there, partitioned by month. Results are also published on `analysis.result.v1`, so ClickHouse can be added for the future chat/analytics agent without changing the service.
- **Expected cost** (list prices of 2026-11-01; token counts and the duplicate share are estimates until the evaluation and `make pilot-stats` measure them):
  - **lean mix** (Haiku 5.5 everywhere, Sonnet 5.5 for escalations): about **$3.9k/year at 100M** text items, **$9.7k at 300M** and **$28k at 1B**. Above ~150M items a year, speech-to-text runs on an own Whisper server instead of a per-minute API (if it passes the dialect test).
  - **heavier mix** (Sonnet 5.5 for posts, images and videos): about $43k/year at 300M. It is needed only if the evaluation shows Haiku failing on those routes.
  - The first estimate was $15.1k (lean) and $48.5k (heavier) at 300M; section 16.1 explains the difference.

## 2. Critique of the brief

The brief's direction is right. These points change the design or the numbers.

| # | Brief says | Finding | Consequence |
|---|---|---|---|
| 1 | ~80 input + ~40 output tokens per comment | Output tokens are underestimated, and input is wrong in both directions. A one-call result with every field is ~60-90 output tokens in compact JSON, not 40. Per-comment input is lower than 80 *only if* the 2-3k-token instruction prefix is shared by many comments (grouping) and cached; sent per comment, the prefix alone is ~2,500 tokens, which costs 30x the comment. Arabic also tokenizes worse than English, and Haiku 5.5's tokenizer produces ~30% more tokens than Haiku 4.5's. | Group 30-40 comments per request and cache the prefix. A compact answer format (section 7.3) brings output to ~45 tokens. The cost model uses 60 uncached + 85 cached input and 45 output tokens per comment; the harness measures the real numbers. |
| 2 | Haiku 5.5 batch ~$14 per 1M comments | Haiku 5.5 **thinks by default** (adaptive thinking, effort `medium`), and thinking tokens are billed as output. Left on, a classification costs 3-10x the estimate. Gemini 3.x Flash models also bill "response and reasoning" output. | Thinking is **off** for comment routes (allowed on Haiku 5.5 at effort `low`-`high`), and set explicitly per route in config. With realistic tokens and the compact format, Haiku 5.5 batch is ~$15 per 1M comments. |
| 3 | Gemini 3.5 Flash-Lite / Flash are the Gemini candidates | Google's pricing page (2026-11-01) already lists **Gemini 3.6, 3.7 and 3.8 Flash** and **Gemini 4 Argon**. Gemini 3.1 Flash-Lite ($0.25/$1.50) is cheaper than 3.5 Flash-Lite ($0.30/$2.50). | The evaluation includes Gemini 3.1 and 3.5 Flash-Lite plus the newest Flash. The provider is configurable, so newer models are a config change. |
| 4 | Gemma 4 31B via Google API ~$27 | Gemma 4 **26B** is on Vertex at $0.15/$0.60 (official page). The 31B price ($0.14/$0.40) only appears on third-party trackers; ai.google.dev could not be reached from this session. The Gemini *API* free tier may use prompts to improve Google's products. | Use paid tiers only (Vertex or the paid Gemini API). The figure is marked unverified. |
| 5 | GPT-5.4 nano batch ~$33 | $0.20/$1.25 standard and $0.10/$0.625 batch per search results of OpenAI's model pages. openai.com could not be reached from this session, so the figure is unverified. One tracker marks GPT-5.4 nano as "deprecating". | Kept as a candidate. Verify on the pricing page before choosing it. |
| 6 | Escalate low-confidence comments to the strong model | LLM self-reported confidence is poorly calibrated, so escalating "everything below 0.6" escalates a random-looking set. Also, most of the money's value is in a small subset: comments that mention a **client's tracked brand**. | Escalate only when an item mentions a tracked entity (found by a free alias matcher before the LLM call) **and** its *calibrated* confidence is low, or it is sarcastic or mixed. Calibration maps are fitted on the gold set per model. |
| 7 | Prior estimate $30-55k/year for the mix | It holds only if posts, images and videos go to Sonnet. Those are ~3% of items but ~70% of the bill in that mix. Haiku for everything plus Sonnet escalation is ~$9-11k/year at 300M. | Start lean and promote a route to a stronger model only when the evaluation shows a measured gap on that route. |
| 8 | Self-hosting pays off at 1.5-3B items/year | Confirmed. Haiku 5.5 batch costs ~$15 per 1M comments, while 2x H100 cost ~$44k/year before staff. Break-even is ~3B comments/year. Self-hosting is also the answer if privacy rules forbid third-party processors. | Keep an OpenAI-compatible provider so a self-hosted vLLM (Gemma, or a distilled model) is a config change. |
| 9 | One call returns all fields; separate models per task multiply cost | Agreed for the LLM. Two **non-LLM** steps cost nothing and help: (a) skip items with no analyzable text (tags of friends only, sticker-only, empty), and (b) an alias matcher that finds tracked brands in the text. The alias matcher routes urgent items to the real-time lane and gives the LLM entity candidates, which improves linking and shortens output. | Both are built (`internal/analysis/content`, `internal/analysis/entities`). |
| 10 | Batch for bulk, real time for urgent | Agreed. "Urgent" needs a definition that works before analysis. | Urgent = matches a tracked-brand alias, or comes from a source marked priority. Everything else goes to the batch lane. |
| 11 | Fine-tuned Arabic encoder reaches ~83-84% macro-F1 (2026 shared task) | Not verified here, and sentiment-only. Aspect sentiment, linking and topics need a generative model or several encoders. Some Arabic encoders' licences are research-only. | Later option, trained on audited labels from the service. Check licences first. |
| 12 | Model retirements can create fake trends | Agreed, and the stored text makes it fixable. Every result records model, prompt, taxonomy and schema versions. A switch is gated on the gold set plus an overlap sample, and history can be re-analysed with the new model at batch prices (~$15 per 1M comments on Haiku 5.5). | See section 7.6. |
| 13 | Comments are personal data | Also *special-category* data. Sentiment toward politicians tied to a pseudonymous author is processing of political opinions (GDPR Art. 9 where GDPR applies). Pseudonymous data is still personal data. | Flagged for legal review (section 12). Author identity is never sent to LLMs. Phone numbers and e-mail addresses are masked before sending. Private persons are not stored as entities. |
| 14 | Keep text indefinitely | Done as decided. The pilot keeps comments in fbs's `comments` table, and the analysis store keeps its own copy. That makes **two indefinite copies** of every comment. | Flagged: keep one copy long term (recommend the analysis store). This is the owner's decision. |

## 3. Requirements and non-goals

**In scope:** analysis of posts, comments and replies, images and videos from any platform once normalised. Idempotency on content. Re-analysis of edits only. Traceability, erasure, cost control, evaluation, and storage ready for an analytics agent.

**Non-goals now:** the normalisation services for Instagram/TikTok/X/Telegram/YouTube, the chat agent itself, dashboards, ClickHouse deployment (designed, not deployed), self-hosted models.

**Volumes:**
- **Target:** 300M text items/year, ~95% short comments. That is ~10 items/s on average, bursty: backfills arrive as hours of thousands per second.
- **Media:** about 5M posts and 5M media items/year, assumed 80% images and 20% videos averaging 45 s.
- **Pilot:** 150 pages, 15,461 posts and 886,463 comments a week. That is ~1.5 comments/s in the steady state and ~46M comments/year.

## 4. Architecture

```
fbs collectors ──fb.post.v1, fb.comment.v1──┐
future normalisers ──content.item.v1────────┤
                                            ▼
                  sb-analysis ingest (Kafka → Postgres, commit after write)
                    │ adapter (fb → canonical) · text hash · skip rules · alias matcher
                    ▼
       content_items / content_index ──► analysis_queue (route, lane, priority)
                                            │
            ┌───────────── realtime lane ───┴──── batch lane (Message Batches, ≤24h) ──┐
            │ urgent items, workers                 submit every 10 min, poll every 1 min│
            ▼                                                                         ▼
     provider router: primary → breaker → fallback; JSON-schema output; budget check
            │
            ▼
     result writer (one transaction): analyses + entity_mentions + content_index state
            + rollup_dirty + outbox  ──► outbox relay ──► analysis.result.v1 (Kafka)
                                                     └──► fb.media.processed.v1 (acks)
fb.media.ready.v1 ──► media worker: S3 → resize / ffmpeg key frames + STT → vision model
                       → media_analyses (+ logo mentions) → ack via outbox
fb.erasure.v1, content.erasure.v1 ──► erasure handler: delete analysis data, suppress subject
rollup job (dirty days) ──► rollup_* tables            future: ClickHouse ◄── analysis.result.v1
```

Everything runs as one binary, `sb-analysis`, with roles chosen by flag (`--roles ingest,pipeline,media,erasure,relay,rollup`). The default runs every role. In Kubernetes, one Deployment per role group lets the media workers (which need ffmpeg) scale apart from the rest.

### Why one service, not one per platform

The platforms differ in *input shape*, not in the analysis. X is short text, TikTok and YouTube are videos with speech, Telegram has long posts and forwards, Instagram is image-heavy. The normalisation services absorb those differences, and the analysis service sees "a text item with optional context and media". What depends on the platform is routing:
- **text length:** short items go to the comment route, long items to the post route;
- **media type:** image or video;
- **context:** reply-to, forwarded-from, hashtags.

All three are fields of the canonical contract and keys of the routing config.

Per-platform services would each duplicate the provider integration, cost control, entity registry, taxonomy, evaluation, re-analysis and erasure. They would also drift apart, so "sentiment toward brand X across all platforms" would compare different models and prompts. That is exactly the comparison the product sells. A platform needing special treatment gets a route (`routes.tiktok_video`), not a service.

### Why Postgres is the work queue (not Kafka alone)

Batch API results take minutes to hours. Holding Kafka offsets that long blocks partitions and turns every restart into a re-submission. So ingest writes items to Postgres and commits at once. The queue (`analysis_queue`, claimed with `FOR UPDATE SKIP LOCKED` leases) then gives:
- retries with backoff;
- a dead-letter state;
- a budget pause, where items simply wait;
- selective re-analysis (insert queue rows);
- backpressure that is visible as queue depth and age.

At ~10 items/s with 40 items per request, the queue sees ~0.25 claims/s. That is trivial for Postgres.

## 5. Contracts

### 5.1 `securebrand.content.v1` (canonical input, `api/proto/securebrand/content/v1/content.proto`)

The minimal contract future normalisers emit. Topics:
- `content.item.v1`, keyed by thread id (a post and its comments stay in one partition, in order);
- `content.erasure.v1`, keyed by request id.

| Field | Meaning |
|---|---|
| `content_id` | Globally unique: `<platform>:<kind>:<native id>`, e.g. `fb:comment:123`. |
| `platform`, `kind` | `FACEBOOK`, `INSTAGRAM`, `TIKTOK`, `X`, `TELEGRAM`, `YOUTUBE`; `POST`, `COMMENT`, `REPLY`. |
| `source_id`, `source_name` | Page / channel / account the item belongs to (`fb:page:42`). |
| `thread_id`, `parent_id` | Content id of the root post, and of the direct parent (replies). |
| `author_key`, `author_type`, `author_is_source` | Pseudonymous HMAC key (never a name or id), `PERSON`/`ORGANIZATION`/`UNKNOWN`, and whether the author is the source itself (page replying). |
| `created_at`, `observed_at` | Times. |
| `text`, `lang_hint` | Text as published, and an optional language hint from the platform. |
| `hashtags`, `mentions`, `links` | Structured extras (mentions carry type and name of the tagged account, never a private person's id). |
| `media` | `MediaRef{media_id, kind, alt_text, duration_ms}`. |
| `forwarded_from`, `is_reshare` | Telegram forwards / reshares. |
| `engagement` | Reactions, replies, shares, views (optional; not used by analysis). |
| `status` | `VISIBLE`, `DELETED`, `REDACTED`. |
| `source_event_id` | The collector's event id, for tracing. |

The analysis service computes its own hash. The collector's `content_hash` changes when reaction counts change, so it cannot decide re-analysis. The service's `text_hash` is a SHA-256 over the analysis input: normalised text, kind, and media ids. An unchanged re-scrape therefore never triggers re-analysis, and an edit always does.

### 5.2 `securebrand.analysis.v1` (output, `api/proto/securebrand/analysis/v1/analysis.proto`)

- **`analysis.result.v1`** (key: content id): `ItemAnalysis` carries every field in section 6, plus `Provenance{provider, model, prompt_version, taxonomy_version, schema_version, route, analyzed_at, input_hash, analysis_version, purpose}` and the item's dimensions: platform, source, kind, created_at, thread and author key. Downstream stores can then build facts without joins.
- **`analysis.media.v1`** (key: media id): `MediaAnalysis` (logos, OCR text, description, labels, transcript, plus the same provenance).

Re-analysis publishes a higher `analysis_version` for the same content id. Consumers keep the highest, which maps directly to ClickHouse `ReplacingMergeTree(version)`.

### 5.3 Facebook adapter (runs today)

`internal/analysis/adapter/fb` maps:
- **`fb.post.v1`** → `POST`: `fb:post:<id>`, source `fb:page:<page_id>`, `author_is_source=true`;
- **`fb.comment.v1`** → `COMMENT` or `REPLY` (depth > 0). Thread `fb:post:<post_id>`, parent the parent comment or the post. `author_key` comes as is; `is_op` becomes `author_is_source`. Text ranges of type `User` become mentions *without* ids or names (private persons), and `Page` mentions keep their name. `COMMENT_STATUS_DELETED` → `DELETED`, `redacted` → `REDACTED` (text dropped);
- **`fb.media.ready.v1`** → a media job (`media_id`, owner content id, S3 object, `delete_after`); acks go to `fb.media.processed.v1` with consumer `analysis` (the value in `media.required_consumers`);
- **`fb.erasure.v1`** → canonical erasure: author key, or source `fb:page:<id>`.

When a Facebook normaliser exists, it publishes `content.item.v1`, and the adapter is switched off by config (`analysis.inputs: [content]`).

## 6. What is extracted

One result per item. The model answers in a compact JSON format enforced by JSON-schema structured output (`internal/analysis/prompt/schema.go`, schema `s2`); code validates it and expands it into the stored fields below. The model writes one language code (`ar-iq`, `ar-msa`, `ckb`, ...) for language and dialect, the yes/no signals as a list of `flags` (usually empty), and scores as whole numbers (−100..100, 0..100). The script and comment keywords are computed from the text, not asked of the model.

| Field | Values |
|---|---|
| `lang`, `dialect`, `script` | `ar`, `ckb` (Sorani), `kmr` (Kurmanji), `en`, `tr`, `fa`, `other`, `none`; dialect `iraqi`, `gulf`, `levantine`, `egyptian`, `msa`, `other`; script `arabic`, `latin` (Arabizi or English), `mixed`, `none` (computed from the letters: deterministic and free); plus `code_switch` (bool). |
| `sentiment` | label `positive`/`negative`/`neutral`/`mixed`, `score` −1..1, `confidence` 0..1 (raw; a calibrated value is stored next to it). |
| `entities[]` | `name` as written, `type` (`brand`, `product`, `organization`, `person_public`, `person_private`, `position`, `place`, `event`, `media_outlet`, `other`; a position is an office independent of its holder: "رئيس الوزراء" = Prime Minister of Iraq), `id` (a registry id from the hints, or empty), `sentiment` + `score` toward that entity (aspect-based), `aspect` (what about it: `price`, `network`, `customer_service`, ...). `person_private` entities are dropped before storage; only a count is kept. |
| `topics[]` | Every topic that applies (up to 10 from the model): ids from the topic list, new topics the model proposed (section 6.1), and `@<entity id>` for every linked brand, organisation, public figure, position, place or event the item is about. Also `keywords[]` (at most 5): the model's for long posts; for comments, their content words (function words, links, digits and masked contact details removed). A comment's keywords are its content words, and asking the model for them cost about a fifth of its answer. |
| `intent` | `complaint`, `praise`, `question`, `request`, `suggestion`, `purchase_intent`, `churn_threat`, `recommendation`, `information`, `giveaway_entry`, `greeting_religious`, `tagging`, `spam_ad`, `other`. |
| `emotion` | `anger`, `joy`, `sadness`, `fear`, `surprise`, `disgust`, `trust`, `anticipation`, `none`. |
| `toxicity` | `none`/`mild`/`severe`, plus flags `hate`, `threat`, `harassment`. |
| `spam`, `sarcasm`, `urgent` | Booleans. `urgent` means a safety, outage or fraud allegation (crisis signal). |
| `ad_type` | `own` (a brand or shop promoting itself), `paid` (disclosed paid promotion of another brand), `hidden` (looks like paid promotion but is not disclosed), `sale` (a private sale, classifieds); empty when not an ad. Mainly posts and images; comments use `spam`. |
| `legal[]` | Possible legal or platform-policy breaches, for human review (section 6.3): `incitement`, `terrorism`, `child_safety`, `sexual_content`, `graphic_violence`, `self_harm`, `drugs`, `weapons`, `gambling`, `regulated_goods`, `fraud_scam`, `unlicensed_finance`, `counterfeit`, `privacy`, `defamation`. |
| `author_bot` | The author's bot level: empty, `suspected`, `likely`, or `confirmed` by the owner (section 6.2). Not asked of the model. |

**Signals worth their cost** (they come free in the same call): intent (complaint/praise/question is what brand teams act on), emotion, toxicity, spam, sarcasm (an explicit flag lowers the weight of mis-read praise), and urgency.

**Bot-likeness is *not* asked of the LLM.** A single comment does not show it. It is computed from behaviour (section 6.2).

The yes/no signals, ad types and breaches share one `flags` list in the model's answer, usually empty, so they add no output tokens when absent.

**Images:**
- `logos[]` (brand + registry id + confidence) and `text_in_image` (OCR, capped at 500 characters);
- `description` (one sentence) and `labels[]` (scene/objects);
- `people_count` (no identification, never face recognition);
- `sentiment` of the image's message;
- `entities[]` and `topics[]` as for text.

**Video:** the same, from up to 8 key frames plus an Arabic transcript. The transcript is stored, capped at 20k characters.

**People policy (proposal):**
- **Public figures** are stored and linked like brands. They are recognised by the entity registry, or by the model's `person_public` with owner review before linking.
- **Private individuals** (tagged friends, names in comments) are never stored as entities, not even their surface text: the result keeps `private_person_mentions: n` only. This keeps the political opinions of identifiable private persons out of the store.
- **No face recognition.**

**Taxonomy v1** (`configs/analysis/taxonomy.v1.yaml`) was proposed from domain knowledge of Iraqi news and brand pages; the pilot data's content was not read. It has 20 top-level topics (including `other`) and 87 sub-topics: politics, security, economy, public services, telecom, banking & payments, retail, food, automotive, real estate, travel, health, education, religion & occasions, sports, entertainment, social issues, technology, and brand engagement. Each topic has an id, English and Arabic labels, and a one-line description that goes into the prompt.

The taxonomy is versioned. A change bumps `taxonomy_version` and makes items eligible for selective re-analysis (section 10). The owner replaces or edits the list.

### 6.1 Dynamic topic list

The taxonomy file (`configs/analysis/taxonomy.v1.yaml`) is only the starting list. The list lives in the database (`analysis.topics`, `topic_aliases`, `taxonomy_versions`) and grows from what the content is about:

1. **Proposals.** When an item is clearly about a theme no listed topic covers, the model adds it to `new_topics` (English name, Arabic name, top-level parent). It is told never to propose a synonym, translation or narrower wording of a listed topic, nor a name of a person, company, position or place.
2. **Deduplication, at once** (`store.ResolveTopics`):
   - a name that is a registry entity becomes that entity's topic (`@asiacell`);
   - a name whose duplicate key matches a known topic becomes that topic. The key is the name normalised, without function words and clitics, singular, words sorted, in English and in Arabic (`textnorm.TopicKey`): "Electricity cuts", "cuts of electricity" and "انقطاع الكهرباء / انقطاع كهرباء" share keys. A merged topic leads to the one it was merged into; a rejected topic is dropped;
   - anything else becomes a **candidate** topic under the proposed parent (`services.water_shortage`). The item keeps it, so nothing is lost while it waits.
3. **Curation, daily** (`internal/analysis/curate`, role `rollup`). Candidates used by at least `topics.approve_min_items` items (default 10) go to the curation model (Sonnet 5.5, a few cents a day). It merges synonyms the key cannot catch ("power outages" into "Electricity cuts"), rejects names, one-off details and vague topics, and approves the rest with clean names. Without a curation provider, candidates are approved on the count alone.
4. **A change is a version.** Each change of the active list records a taxonomy version (`tax-v1.1`, `tax-v1.2`...), which results carry. Merges and rejections re-point or remove the topic in stored analyses and mentions (GIN indexes on the topic arrays), rebuild their rollup days, and publish a `TopicUpdate` on `analysis.topic.v1` so downstream stores follow. Every replica reloads the list each minute. The system prompt changes only with the version, so the prompt cache breaks at most once per change.
5. **The owner decides last:** `sb-analysis topics list | approve | rename | merge | reject | curate | import`.

**Entity topics.** Every linked entity of an item becomes a topic `@<entity id>` at no cost: one spelling-independent topic per brand, organisation, public figure, position (`@pos-iraq-pm`), place or event. Linking a candidate entity later adds the topic to its earlier items; merging two entities re-points it.

**Limits:** up to 10 model topics per item, at most 3 proposals per item, 24 topics per item in all.

### 6.2 Bot and fake-account detection

The service has no profile data (author ids are pseudonymous HMACs, by design), so detection is behavioural. Once a day (role `rollup`, `bots.every`), `store.ScoreAuthors` computes, for every author with at least `bots.min_items` (10) items in the last `bots.window` (30 days), excluding pages replying on their own page:

| Signal | Points (weak / strong) |
|---|---|
| Burst: most items in one clock hour (≥15 / ≥30) | 10 / 25 |
| Volume: items per active day (≥20 / ≥50) | 10 / 20 |
| Repetition: share of items whose text (15+ characters) the author posted in 3+ threads (≥25% / ≥50%) | 10 / 25 |
| Coordination: share of items whose long text (30+ characters, 5+ words) at least `bots.coordination_authors` (5) authors posted (≥10% / ≥30%) | 15 / 35 |
| Fast replies: share of comments within 60 s of their post, with 10+ comments (≥30% / ≥50%) | 5 / 15 |
| Spam: share flagged spam, ad or scam by the analysis (≥25% / ≥50%) | 10 / 20 |
| Breadth: sources per active day (≥10 / ≥20) | 5 / 10 |
| No sleep: distinct hours of the day, with 10+ items a day (≥20 / ≥23) | 5 / 10 |

- **Levels:** score ≥ 45 `suspected`, ≥ 70 `likely` (`bots.suspected_score`, `likely_score`). The reasons are stored with the score.
- **Formulaic texts never count** as repetition or coordination: contest entries, prayers and tags (intents `giveaway_entry`, `greeting_religious`, `tagging`). Thousands of real people write the same "تم".
- **The owner decides last:** `sb-analysis authors list | mark --key … --as bot|human|none | score`. A `bot` override reads `confirmed`; `human` clears the flag whatever the score.
- **Every item of a flagged author is flagged,** also retroactively:
  - the agent views show `author_bot` from the author's current level;
  - results carry the level at analysis time, and `AuthorFlag` events on `analysis.author.v1` announce every change;
  - rollups count `bot_items` / `bot_mentions`, and a level change rebuilds the author's days.
- **Limits, stated plainly:** behaviour alone produces false positives (super-fans, giveaway hunters, page admins posting from personal accounts) and misses careful bots. Paid human "electronic armies" look the same and are flagged the same; "bot" here means inauthentic behaviour, not proven automation. The thresholds are starting values: `make pilot-stats` prints the activity distribution (counts only), and the owner reviews a sample of flagged authors before the flags are shown to clients.
- **Scale:** the scoring query scans 30 days of items once a day, minutes at pilot scale. At hundreds of millions of items a year it moves to ClickHouse with the rest of the analytics (section 12.3).
- **Privacy:** this is automated profiling of (pseudonymous) people. It is flagged for the lawyer. The profile row is deleted with the author's other data on erasure.

### 6.3 Legal and platform-policy flags

The model flags possible breaches in the same call, for **human review, never as a legal finding**. Categories follow Iraqi law where it is clear and common platform policies (Meta's Community Standards): incitement to violence or hatred, terrorism and armed-extremist propaganda, child safety, explicit sexual content, graphic violence, self-harm, drugs, weapons, gambling, regulated goods (alcohol, prescription medicines), fraud and scams (including impersonating brand or official pages), unlicensed finance, counterfeit goods, privacy violations (doxxing), and defamation of companies and private persons.

**Deliberately not breaches** (the prompt says so): criticism of the government, officials, institutions, companies or religion; political opinions; complaints; insults (that is toxicity); news reporting about crimes; discussion of sexuality or identity. Iraqi law has been used against government critics and against LGBT content, so flagging those would turn a monitoring tool into a reporting tool against users. Changing this boundary is the owner's decision, with their lawyer.

Breaches are stored per item (`legal[]`), counted per day and source (`rollup_flag_daily`), and measured in the evaluation (the labeller marks them too).

## 7. Model strategy

### 7.1 Routes (config: `analysis.routes`)

| Route | Default model | Lane | Thinking | Why |
|---|---|---|---|---|
| `comment` (comments and replies; posts under 280 characters) | `claude-haiku-5-5` | batch; real time if urgent | off | 95% of volume; cheapest at list price. |
| `post` | `claude-haiku-5-5` | batch; real time if urgent | off | Promote to `claude-sonnet-5-5` if the evaluation shows a gap on long MSA news posts (+~$7k/yr at 300M). |
| `image` | `claude-haiku-5-5` | real time | off | Promote to Sonnet if logo recall is short (+~$16k/yr real time, ~$8k with a batch media lane). |
| `video` | `claude-haiku-5-5` + STT | real time | off | Key frames + transcript. |
| `escalation` | `claude-sonnet-5-5` | batch | off | Tracked-entity items with low calibrated confidence, or sarcastic/mixed. Expected ≤2% of comments. |
| `audit` | `claude-opus-5-5` | batch | adaptive | 0.02% deterministic sample (~165 a day at 300M items a year), stored with `purpose=audit`, never shown. It measures agreement and drift. |

**Fallback per route** (`fallback:`): another provider, e.g. Gemini 3.1 Flash-Lite through the OpenAI-compatible endpoint. It is used only by the real-time lane:
- when the primary's circuit breaker is open;
- when the primary refuses after isolation (section 8.4).

The batch lane keeps retrying the primary's batch API (every submission round, without counting attempts) for `batch.fallback_after` (default 12h), because mixing models in the bulk data is itself a consistency risk (section 7.6). After that, the backlog goes to real time: to the primary if its real-time API still works, else to the fallback. A successful batch submission ends the outage. Every result records the model that produced it, and `fbs_analysis_fallback_total` counts fallback use.

### 7.2 Grouping and prompt caching

- **Grouping:** a request = one thread context (the post text, truncated to 1,500 characters) + up to `grouping.max_items` (40) items of that thread, capped by an estimated token size. Items carry an index; the response is an array keyed by index. Kafka keys comments by post, so a thread's comments arrive together. The real-time lane waits `grouping.realtime_wait` (20 s) for siblings, and the batch lane groups within each 10-minute window.
- **Static prefix** (system prompt: instructions, taxonomy, output rules; ~2.5k tokens) is byte-identical across requests and marked `cache_control`. Cache reads cost 0.1x on Haiku 5.5 and 0.05x on Sonnet/Opus 5.5, and stack with the batch discount. In the Batch API, cache hits are best-effort. The minimum cacheable prefix on the 5.5 models is 512 tokens.
- **Identical items are sent once** (`internal/analysis/pipeline/dedup.go`). Contest posts ("علق تم للمشاركة"), condolence and prayer threads collect hundreds of identical comments. Within a claim, items of one thread with the same parent and the same input hash (text, kind, media) collapse onto the first: it alone is shown to the model, and the others ride in the same request as followers. They get its answer and their share of the cost, and they fail and retry with it. The model's input for them would be identical, so its answer would be too. Followers count toward neither group limit, so a thread of 3,000 "تم" costs one prompt slot per claim. The batch lane recomputes the mapping from the request's stored input hashes when results arrive. Posts and audits are never collapsed. `fbs_analysis_items_deduplicated_total` counts followers, and `make pilot-stats` measures the share (assumed 10%).
- **Entity hints:** per item, the alias matcher's candidates (`id`, canonical name). They are not the whole brand list (hundreds of brands × aliases would bloat every prompt).
- **PII masking:** phone numbers (Western and Arabic-Indic digits) and e-mail addresses are replaced with `[phone]` / `[email]` before the text leaves the service.

### 7.3 Structured output and validation

- **Anthropic:** `output_config.format` with a JSON schema (constrained decoding) is used; forced tool use is not, because forced `tool_choice` is rejected on several current models.
- **OpenAI-compatible providers:** `response_format: json_schema, strict: true`.
- **Compact answers.** Output tokens are ~75% of a comment's cost, so the answer format is short. One language code replaces language and dialect, a list of `flags` (usually `[]`) replaces seven booleans, scores are whole numbers, and the script and comment keywords are computed locally. This is ~45 output tokens per comment instead of ~70-100, with the same information.
- **Field order is kept** in the schema sent to providers (Go maps would sort the keys alphabetically). Models write fields in schema order, so `i` comes first and every label before its score and confidence.

Code then validates every item:
- the index is in range and unique;
- enum values are valid and scores in range;
- `person_private` entities are dropped, and topics outside the taxonomy are removed (counted as `invalid_topic`).

If items are missing from a response, only those are re-queued (attempt + 1). An invalid or truncated answer re-queues all its items. Re-queued items wait with exponential backoff and are then retried alone, so one bad item cannot sink a group twice. After `max_attempts`, items move to `analysis.dead_letters` with the error; they are alerted on and replayable (`sb-analysis replay-failed`). Provider errors and refusals are what trigger the fallback (section 8.4).

### 7.4 Batch vs real time

| | Batch lane | Real-time lane |
|---|---|---|
| Price | 50% off (Anthropic, Google, OpenAI) | list |
| Latency | minutes, ≤24h | seconds |
| Used for | everything not urgent | tracked-brand alias hits, priority sources, media, fallbacks |

The batch lane submits every `batch.submit_every` (10 min), with at most `batch.max_requests` (5,000) requests per batch. It polls every minute and resumes after restarts, because batch ids are stored. Errored or expired results re-queue their items.

### 7.5 Calibration, escalation, audit

- **Calibration:** the evaluation harness fits a per-model, per-route isotonic map from raw confidence to observed accuracy on the gold set. It writes the map to `configs/analysis/calibration/<model>.yaml`, the service applies it, and both values are stored.
- **Escalation rule:** `tracked_entity_hit && (calibrated_conf < 0.6 || sarcasm || sentiment == mixed)`. The escalated result becomes current; both are kept.
- **Audit:** a deterministic hash of the content id selects 0.02% of items for Opus 5.5 (~1,600 a month even at 100M items a year, enough to see a drift of a few points month by month). A daily job computes agreement (sentiment label, top entity sentiment, topics Jaccard) per route and model, and alerts when it drops below the baseline measured at launch.

### 7.6 Consistency over five years

1. **Pin models.** Use fixed model ids, never "latest" aliases, plus prompt, taxonomy and schema versions per route. Every result stores them.
2. **Gate switches.** A new model or prompt must:
   - beat or match the current one on the gold set (macro-F1 within the bootstrap confidence interval);
   - run on an *overlap sample* (20k recent items, stratified by source and platform) next to the current model.
   The harness reports the per-label shift, e.g. "negative +3.1 points".
3. **Mark series breaks.** Each switch writes a row in `model_changes`. Dashboards draw it, and the agent's tools return it with any time series that crosses it.
4. **Re-analyse when it matters.** Text is kept, so history can be re-run on the new model (selective and budgeted, section 10), e.g. the last 12 months of a client's tracked entities. Re-running all of 1.5B items on Haiku 5.5 batch would cost ~$22k.
5. **Anchor set.** The gold set plus a frozen 5k sample is re-run monthly on the production config. Rising disagreement with the stored labels means provider-side drift even without a model change.

## 8. Pipeline and reliability

### 8.1 Ingest

- **Consuming:** a batch consumer (like the persister) reads the input topics, applies the adapter, and upserts `content_index` / `content_items` in one transaction per batch. It commits offsets only after the commit. An undecodable record is logged with topic and offset and counted, never its payload.
- **Upsert rules:**
  - **new item:** stored, then queued, or marked `skipped` if there is no analyzable text;
  - **same `text_hash`:** nothing is written;
  - **new `text_hash`:** text updated, `edit_count + 1`, re-queued with reason `edit`;
  - **`DELETED`:** status set; the text and analysis are kept, and dashboards may exclude the item;
  - **`REDACTED` or erased subject:** text removed and analysis deleted (section 11).
- **Skip rules:** after removing mentions, URLs, punctuation and whitespace, an item with no letters, digits or emoji is skipped (`skip_reason=no_text`). Emoji-only items *are* analysed: they are cheap and carry sentiment.

### 8.2 Queue

`analysis_queue(content_id, route, lane, priority, reason, not_before, attempts, lease_until, request_id)`:
- **Claiming:** workers claim `ORDER BY priority, enqueued_at` with `SKIP LOCKED` leases. Real-time leases last 2 minutes, batch leases 26 hours (longer than the 24h batch window).
- **Re-submission:** a lease that expires (crash) makes items claimable again. A duplicate submission is harmless: results are written only if the item's current `text_hash` still equals the request's `input_hash` and no result exists for that hash.

### 8.3 Results and outbox

One transaction per request group:
- insert `analyses` (version + 1) and set the previous row `is_current=false`;
- replace the item's current `entity_mentions`;
- update `content_index` state;
- delete queue rows;
- insert `rollup_dirty(day)` and the outbox rows.

The relay publishes outbox rows to Kafka (idempotent producer) and deletes them. Delivery is at least once, and `event_id = content_id:analysis_version` lets consumers deduplicate.

### 8.4 Errors, refusals, rate limits, budget

- **Provider errors:** SDK retries on 429/5xx, then a circuit breaker per provider (`internal/platform/breaker`) and backoff on the queue.
- **Refusals** (`stop_reason: refusal`; Haiku 5.5 has safety classifiers): a refused group is split and retried as single items to isolate the refusing item. A single refusal goes to the fallback provider. A second refusal stores a minimal result with `refused=true`. Toxic and violent comments are exactly what this service must classify, so the refusal rate on Iraqi political content is an evaluation metric.
- **Rate limits:** about 0.25 requests/s on average and a few per second in bursts, far below API limits. A token-bucket limiter per provider (`providers.<name>.rps`) protects shared keys.
- **Budget:** `budget.daily_usd` (global) and `routes.<r>.daily_usd`, measured from real usage in `provider_requests` per Iraq day (Asia/Baghdad). When a request would exceed the cap:
  - batch and re-analysis work pause (items wait);
  - urgent real-time work continues if `budget.urgent_exempt`;
  - `sb_analysis_budget_paused` is set and alerts fire.

### 8.5 Scaling

Ingest scales on Kafka lag (KEDA). Pipeline workers are I/O-bound: one or two replicas handle the target volume. Media workers scale on `media_jobs` depth (KEDA PostgreSQL scaler).

## 9. Media

- **Input:** `fb.media.ready.v1` → `media_jobs` (media id, owner content id, kind, S3 object, `delete_after`). The offset is committed after insert.
- **Policy** (`analysis.media.kinds`):
  - **analysed:** photos, videos, reels, comment photos, comment videos, covers;
  - **acknowledged without analysis** (`PROCESSED`, `skip_reason=policy`): stickers, GIFs, profile pictures, and thumbnails of videos that are analysed themselves.
- **Images:** read from S3 (max 20 MB), decode (JPEG/PNG/GIF/WebP), resize to 1024 px on the long side, re-encode JPEG q85 (~600-900 image tokens). One real-time call with the post text as context. Logos are linked through the registry and stored as entity mentions with `origin=image`.
- **Video:** download to a temporary file and run ffmpeg:
  - key frames: scene-change selection, at most 8, 512 px;
  - audio: 16 kHz mono.
  Speech-to-text runs through the provider's transcription endpoint (OpenAI-compatible `/audio/transcriptions`: OpenAI `gpt-4o-mini-transcribe`, or an own Whisper server speaking the same API, provider `whisper` in the config), then one vision call with frames + transcript. The temporary files are deleted in all cases.
- **Speech costs count toward the budget.** The budget is checked (vision estimate + audio minutes × the transcription model's `per_minute` price) before transcribing, and each transcription is logged as a request of its own, so the day's spend includes it.
- **Deadlines:** jobs are processed nearest `delete_after` first. The `TTL-at-risk` gauge counts jobs within 6h of `delete_after`.
- **Ack:** written to the outbox in the same transaction as the analysis: `PROCESSED`, `FAILED_PERMANENT` (undecodable, too large, policy) or `FAILED_RETRYABLE` (transient; fbs re-announces). Acks therefore survive crashes, and the janitor deletes the file.

**STT options for Iraqi Arabic speech:**

| Option | Cost |
|---|---|
| Gemini 3.5 Transcribe (Vertex) | **$0.0051** a minute (official) |
| OpenAI `gpt-4o-mini-transcribe` | ~$0.003 a minute (third-party figure, unverified) |
| `gpt-4o-transcribe` | ~$0.006 a minute (unverified) |
| Own Whisper large-v3-turbo server (faster-whisper, int8, OpenAI-compatible API, e.g. [`deepdml/faster-whisper-large-v3-turbo-ct2`](https://hf.co/deepdml/faster-whisper-large-v3-turbo-ct2)) on one 8-CPU machine | a fixed ~$2k a year (estimate) |

- **Volume:** at 300M items a year there are ~1M videos and ~770k audio minutes. That is ~$3.9k a year on Gemini 3.5 Transcribe, and ~$13k at 1B items.
- **Own server:** faster-whisper runs large-v2 at ~7-8x real time with int8 on 8 CPU threads (its published benchmark). The turbo model has 4 decoder layers instead of 32 and is faster still. One 8-CPU machine should therefore keep up with the ~43k audio hours a year of 1B items; this is an estimate, and a load test confirms it before use.
- **Break-even:** ~400k audio minutes a year, i.e. ~150M text items. Below that the API is cheaper and needs no server.
- **Dialect quality is unknown for both,** and Whisper-class models are weaker on dialects. The evaluation therefore transcribes 20-30 clips with reference transcripts (word error rate) before choosing. Public Iraqi fine-tunes of Whisper exist on Hugging Face but are unproven (a handful of downloads).
- **If the transcriber fails,** the video job is retried (up to `media.max_tries`, then acknowledged `FAILED_RETRYABLE` so the producer re-announces the file). An outage of the own server therefore delays videos within the file's lifetime; it does not lose them.

## 10. Re-analysis (selective, budgeted)

`sb-analysis reanalyze` queues items matching a filter, with `--route`, `--reason`, a dry-run estimate of items and cost, and `--max-usd`. Filters:
- `--since/--until`;
- `--platform`, `--source`;
- `--entity` (items mentioning it);
- `--taxonomy-below N`;
- `--model` (items analysed by a retiring model);
- `--sample 0.1`.

Re-analysis uses priority `low` and the batch lane, and stops at the budget. Typical uses:
- **taxonomy change:** items whose topics fall under a changed branch;
- **model switch:** overlap sample, then a client's history;
- **registry merge:** no re-analysis needed (mentions are re-pointed by SQL);
- **new tracked brand:** search the alias over historic text, then queue only the matches.

## 11. Entity registry

- **Tables:**
  - `entities`: id, canonical name, type, `public_figure`, status `active`/`candidate`/`merged`/`rejected`, `merged_into`;
  - `entity_aliases`: normalised alias → entity, with language/script and source `owner`/`discovered`/`model`;
  - `client_entities`: client → entity, role `own`/`competitor`/`watch`;
  - `entity_candidates`: the review queue.
- **Seed:** `configs/analysis/entities.seed.yaml` holds public Iraqi brands with Arabic, English and Arabizi aliases (e.g. آسياسيل / Asiacell / asiasell). The owner's brand list replaces or extends it (`sb-analysis entities import`).
- **Normalisation for matching:**
  - remove diacritics and tatweel;
  - unify alef forms (أ إ آ → ا), ى→ي, ة→ه, ؤ→و, ئ→ي;
  - map Arabic-Indic digits to ASCII; lower-case Latin; collapse repeated letters ("حلوووو" → "حلو");
  - strip the clitics و، ب، ل، ال، بال، وال at word starts.
  The matcher is a token n-gram dictionary over normalised text, rebuilt on registry change.
- **Linking:** the model returns `id` when an entity matches a hint; otherwise a name and type. The writer links in order:
  1. a valid hint id;
  2. an exact normalised alias match;
  3. otherwise an `entity_candidates` row (surface, type, count, first/last seen, up to 5 example content ids).
- **Review queue:** candidates seen ≥ N times (default 20) appear in `sb-analysis entities review`, which can accept as new, merge into an existing entity, or reject (adds a stop-alias). A merge re-points mentions in SQL and marks rollups dirty. `person_public` candidates always need review; private persons never become candidates.

## 12. Storage

Full rationale in [ADR 0006](../adr/0006-analysis-storage.md).

### 12.1 Postgres (system of record; built)

Own migrations (`migrations/analysis`, goose table `analysis_goose_db_version`) and own DSN (`analysis.postgres.dsn`, default: the fbs database). The service can move to its own database without code changes.

| Table | Partitioning | Key / main indexes |
|---|---|---|
| `content_index` | hash (content_id) × 16 | PK content_id → created_at, text_hash, state, current analysis version |
| `content_items` | monthly on `created_at` | PK (created_at, content_id); (source_id, created_at); (thread_id); author_key; GIN full-text index on normalised text |
| `analyses` | monthly | PK (created_at, content_id, version); `is_current` partial indexes on (sentiment, created_at) |
| `entity_mentions` | monthly | (entity_id, created_at) incl. sentiment; (source_id, created_at); content_id; author_key |
| `media_jobs`, `media_analyses` | – / monthly | media id; owner content id |
| `analysis_queue`, `provider_batches`, `provider_requests`, `analysis_dead_letters`, `outbox`, `rollup_dirty`, `model_changes`, `erasures` | – | operational |
| `rollup_entity_daily`, `rollup_entity_topic_daily`, `rollup_topic_daily`, `rollup_source_daily` | – | PK on all dimensions |
| `entities`, `entity_aliases`, `entity_candidates`, `clients`, `client_entities`, `client_sources`, `client_roles` | – | registry and scoping |
| `topics`, `topic_aliases`, `taxonomy_versions` | – | the dynamic topic list (section 6.1) |
| `authors` | – | PK (platform, author_key): behaviour, bot score, level, owner override (section 6.2) |

**Size:** roughly 1.1 KB per item all-in (text ~150 B, analysis ~400 B, mentions, indexes), so ~330 GB/year at 300M items. Partitions are monthly, there is no retention drop, and old partitions can move to a cheaper tablespace.

### 12.2 Daily rollups (built)

Days are Iraq days (Asia/Baghdad).

| Rollup | Columns | Answers |
|---|---|---|
| `rollup_entity_daily` | day, entity, platform, source, sentiment → mentions, score sum | Sentiment toward a brand over time, per platform or page. |
| `rollup_entity_topic_daily` | day, entity, platform, topic → mentions, positive, negative, neutral, mixed | "Most common topics when people mention brand X". |
| `rollup_topic_daily` | day, platform, source, topic → items and sentiment counts | Topic trends. |
| `rollup_source_daily` | day, platform, source, kind → items and sentiment counts, complaints, urgent, ads, possible breaches, bot items | Per-page health. |
| `rollup_flag_daily` | day, platform, source, flag (`ad_own`, `ad_paid`, `ad_hidden`, `ad_sale`, or a breach category) → items, bot items | Ads and possible breaches over time. |

`rollup_entity_daily` also counts `bot_mentions`, so "how much of the negativity toward brand X comes from suspected bots" is one query.

**Correctness under re-analysis, merges and erasure:**
- writers only mark `rollup_dirty(day)`;
- a job recomputes each dirty day from current analyses (delete + insert for that day);
- a day is ~1M mentions at 300M items/year, a few seconds to scan.

### 12.3 Storage for the future chat/analytics agent

The agent (Claude with tools) answers questions such as "most common topics when people mention brand X in the last year, across all platforms". It uses read-only SQL, text search, example retrieval and charts, and never touches the primary store directly.

**Entity-mention table.** `entity_mentions` links each item to each entity, carrying:
- per-entity sentiment and aspect;
- the item's topics (array), platform, source, kind and `created_at` (and its Iraq-time day);
- language, intent and the analysis version.

So "topics when people mention X" is a single-table query with no joins. Its mirror in ClickHouse is `mentions` (DDL below).

**Rollups:** as in 12.2. They answer the common questions in milliseconds at any scale.

**Analytics store: ClickHouse vs PostgreSQL + rollups**

| | PostgreSQL + rollups | ClickHouse |
|---|---|---|
| Questions that match a rollup | ms | ms |
| Ad-hoc group-by over raw mentions, one brand (~1-10M rows) | 1-10 s with indexes | < 1 s |
| Ad-hoc group-by over all mentions, one year (~300M+ rows) | minutes; risks hurting the primary | 1-5 s on one 16-vCPU node |
| Storage per mention | ~250 B with indexes | ~20-40 B compressed |
| Safety for agent-written SQL | `statement_timeout` (user-changeable), RLS, no result-size cap | settings profiles with read-only constraints, `max_execution_time`, `max_result_rows`, `max_memory_usage`, quotas, row policies: all server-enforced |
| GDPR deletes | normal `DELETE` | lightweight `DELETE` / mutations; fine for occasional erasures |
| Operations | already running | one more stateful system: one node self-hosted (~$150-300/month) or ClickHouse Cloud (similar at this size) |
| Feeding it | – | Kafka engine table on `analysis.result.v1` + materialized views; no service changes |

**Recommendation:**
- **Now:** Postgres + rollups (built). It is enough for dashboards and for the pilot's ~46M items/year.
- **Before the agent goes live, or past ~200M mentions:** add ClickHouse fed from `analysis.result.v1`. The agent queries **only ClickHouse** (and Postgres full-text search through a narrow tool), never the primary.

```sql
-- ClickHouse (designed, not deployed)
CREATE TABLE mentions (
  content_id String, analysis_version UInt32, day Date, created_at DateTime,
  platform LowCardinality(String), source_id String, kind LowCardinality(String),
  entity_id String, entity_type LowCardinality(String), sentiment LowCardinality(String),
  score Float32, aspect LowCardinality(String), topics Array(LowCardinality(String)),
  intent LowCardinality(String), lang LowCardinality(String), author_key String,
  model LowCardinality(String), client_ids Array(String)
) ENGINE = ReplacingMergeTree(analysis_version)
PARTITION BY toYYYYMM(day) ORDER BY (entity_id, day, content_id);
-- items: one row per item (sentiment, topics, intent, toxicity...), ORDER BY (platform, source_id, day, content_id)
-- per-client scoping: CREATE ROW POLICY client_x ON mentions USING has(client_ids, 'x') OR source_id IN (...) TO agent_client_x;
-- limits: CREATE SETTINGS PROFILE agent SETTINGS readonly = 1, max_execution_time = 15,
--         max_result_rows = 10000, max_memory_usage = 4e9 ... ; CREATE QUOTA ... FOR INTERVAL 1 hour MAX queries = 500;
```

**Read-only role with query time and size limits (Postgres; built).** `sb-analysis agent-role --client <id> --role <name>` creates a login role:
- `NOINHERIT`, `CONNECTION LIMIT 5`, `default_transaction_read_only = on`;
- `statement_timeout = 15s`, `idle_in_transaction_session_timeout = 30s`, `work_mem = 32MB`, `temp_file_limit = 1GB`;
- `SELECT` only on the views of schema `agent`, never on base tables.

The password is read from a file, never from arguments or chat. Postgres lets a session change its own `statement_timeout`, so the agent's SQL tool must also:
1. accept one `SELECT` only (parsed with a Postgres parser);
2. wrap it as `SELECT * FROM (<query>) q LIMIT 10001`;
3. run it in a read-only transaction with its own client-side deadline that cancels the backend.

ClickHouse enforces all of this server-side, which is a reason to give the agent ClickHouse.

**Per-client access scoping (built).**
- **Model:** a client sees mentions of its tracked entities (`client_entities`) anywhere in the corpus, and every item from the sources it monitors (`client_sources`).
- **Postgres:** each client gets its own login role (`client_roles`). The `agent` views are security-barrier views filtered on `current_user`: the views read the tables with their owner's rights, while `current_user` stays the agent's login. The scope is tied to the login, so agent SQL cannot change it (a session variable could be reset by the SQL itself). A test connects as two client roles and checks that each sees only its own rows and no base table.
- **ClickHouse:** row policies per client user, as above.

Example retrieval returns at most 20 texts per call, with phones and e-mails masked and no author identity.

**Arabic full-text search (built for Postgres).**
- **Index:** an expression GIN index on `to_tsvector('simple', analysis.ar_normalize(text))`, where `ar_normalize` is an `IMMUTABLE` SQL function applying the alef/yaa/taa-marbuta/diacritic/tatweel/digit normalisation of section 11. Queries go through the same function.
- **Why `simple`:** the built-in `arabic` Snowball stemmer helps MSA news posts but mangles dialect and Arabizi, so it is not used for comments.
- **Limits:** clitics (و، ال) attached to brand names are covered by the alias matcher, not by full-text search. Substring search (`pg_trgm`) is not enabled by default because its index is 2-3x the text size. Enable it on posts only if needed.
- **At scale:** OpenSearch with the ICU and Arabic analysers, or ClickHouse's text index.
- **Index size:** ~30-50% of the text size.

**Meaning-based search (embeddings): evaluated, costed option (not built).**
- **What to embed:** all posts, plus comments that have ≥ 5 words and mention a tracked entity, plus a 10% sample of other comments (deduplicated). At 300M items/year that is ~5M posts + ~30M comments.

| Model | Price per 1M tokens | Notes |
|---|---|---|
| Gemini Embedding 2 (Vertex) | $0.20 online / **$0.10 batch** (official) | Multilingual, managed |
| OpenAI `text-embedding-3-small` / `-large` | ~$0.02 / ~$0.13 (unverified) | Weaker on dialect in public reports |
| Voyage multilingual (Anthropic's recommended embeddings partner) | ~$0.06-0.12 (unverified) | – |
| BGE-M3 (MIT licence, 1024-d dense + sparse, strong Arabic) | self-hosted: CPU (~50-100 texts/s on 16 cores) or one L4 GPU (~1,000/s) | No data leaves; ~$0 marginal |

- **API cost:** posts at ~400 tokens and comments at ~40 tokens make ~3.2B tokens/year, ~$320/year with Gemini batch. The API cost is negligible; **storage dominates**:
  - 35M vectors × 1024 dims × 2 bytes (`halfvec`) ≈ 70 GB/year plus an HNSW index of similar size;
  - Matryoshka truncation to 256 dims ≈ 18 GB/year.
- **Store:** start with pgvector on posts only (5M/year), and move to ClickHouse's vector index or Qdrant beyond ~50M vectors.
- **Evaluation before choosing:** build ~200 search queries (Arabic MSA, Iraqi dialect, Arabizi, English, and cross-lingual such as "network outage" → Arabic comments) with relevance judgements from the labeller. Measure recall@10 and nDCG@10 per model: Gemini Embedding 2, BGE-M3, multilingual-e5-large and Voyage. The harness has the place for it (`sb-eval retrieval`, planned).

## 13. Erasure and privacy

- **Erasure** (`fb.erasure.v1` via the adapter, `content.erasure.v1` natively), idempotent and logged in `erasures`:
  - **author:** delete the author's items' text, analyses, mentions, media analyses and behaviour profile (`authors`), then mark the affected rollup days dirty;
  - **source:** delete everything from that source (items, analyses, mentions, media), plus its rows in the per-source rollups;
  - **both:** record the subject in `erased_subjects`, so late items and in-flight batch results for it are dropped at write time; delete pending queue and media rows.
  - **ClickHouse (later):** consumes the same erasure topics and runs lightweight deletes.
- **Data minimisation:**
  - LLMs receive only text (with phones and e-mails masked), post context and media. They never receive author names, ids or keys;
  - private persons are not stored as entities;
  - logs never contain content or credentials.

**Legal points to flag (for the owner and counsel; not decided here):**
1. **Third-party processors:** sending comments to Anthropic, Google or OpenAI makes them processors. You need DPAs, the providers' API data-retention terms (zero-data-retention can be requested), paid tiers only (some free tiers use data for training), and a check of transfer rules for where the data subjects and clients are.
2. **Keeping text indefinitely:** storage limitation (GDPR Art. 5(1)(e)) needs a documented purpose and periodic review. "Indefinitely" is hard to justify for private individuals' comments, and there are now two copies (section 2, point 14).
3. **Special-category data:** sentiment toward politicians and religious topics, linked to a pseudonymous author, is political/religious opinion data (Art. 9). It needs a legal basis assessment and a DPIA.
4. **People mentioned in comments:** storing who is mentioned, even public figures, is processing about them. The policy keeps private persons out.
5. **Iraq:** there is no comprehensive data-protection law in force at the time of writing (a draft exists). Clients' jurisdictions and where SecureBrand is established decide which laws apply.
6. **Facebook terms** (already flagged in the repo) apply equally to analysing the data.
7. **Bot scores are automated profiling** of (pseudonymous) people (GDPR Art. 4(4), and Art. 22 if a decision with effects is based on them). A false "bot" label shown to a client is an accusation about a real person. Keep the label internal or reviewed, show reasons, and give a way to correct it (the owner override exists).
8. **Legal and policy flags are allegations.** Treat them as leads for review, never publish them as findings, and do not use them to report users to authorities without legal advice. The boundary (criticism of the government is never flagged) is a deliberate choice (section 6.3).

## 14. Observability

Metrics (`fbs_analysis_*`, Prometheus):
- items ingested, skipped and analysed, by platform, kind and route;
- queue depth and oldest age, by route and lane;
- provider requests, by provider, model, lane and outcome;
- latency;
- tokens (input, output, cache read, cache write) and **cost in USD**, by model and route;
- spend today vs budget, and `budget_paused`;
- fallback share; invalid outputs, refusals, missing items and dead letters;
- items answered by an identical item (`items_deduplicated_total`);
- batch age;
- media processed and acked by status, and **TTL at risk**;
- erasures processed;
- outbox backlog;
- rollup lag.

Alerts:
- queue age > 2h on the real-time lane, > 30h on the batch lane;
- TTL at risk > 0;
- dead letters rising;
- invalid or refusal rate > 2%;
- fallback share > 5%;
- spend > 80% of budget;
- audit agreement below baseline − 5 points.

**Quality drift:**
- **audit sample:** Opus 5.5 on 0.02% of items, agreement tracked daily;
- **label distribution shift:** PSI per route per week;
- **human check:** the anchor set, re-labelled quarterly by the labeller (~200 items).

## 15. Evaluation plan

The harness is `cmd/sb-eval`, with guide [docs/analysis/evaluation.md](../analysis/evaluation.md).
- **Same code as production:** it uses the service's own prompt, schema, providers and validation, so it measures what is deployed.
- **Gold set:** ~1,000 comments (stratified by page, length, script and post type; oversampled for tracked-brand mentions and Kurdish/Arabizi), ~200 posts and ~100 images, labelled by an Iraqi native speaker with [the labelling guide](../analysis/labelling-guide.md) in the built-in web tool (`sb-eval label`).
  - The set lives on the owner's server only (`/srv/securebrand/gold/`), outside the repository. `.gitignore` blocks `*.gold.jsonl`, and CI fails on any `testdata/**/gold*`.
  - The repository has only synthetic examples.
- **Metrics:**
  - sentiment macro-F1 (4 and 3 classes), with 95% bootstrap intervals (1,000 resamples);
  - aspect sentiment: entity-sentiment pair F1, and accuracy on matched entities;
  - entity F1 by type, and linking accuracy;
  - topic micro and macro F1; intent and language accuracy;
  - **measured cost** per 1,000 items from real token usage, at the configured prices, batch-adjusted;
  - latency p50/p95; failure, refusal and missing-item rates.
- **With 1,000 items,** the macro-F1 interval is about ±3 points. Models closer than that are tied, and cost decides.
- **Grid:** Haiku 5.5, Sonnet 5.5 and Opus 5.5; Gemini 3.1/3.5 Flash-Lite and the newest Flash; GPT-5.4 nano/mini; Gemma 4 (Vertex) via the OpenAI-compatible provider. Also group size (10/25/40) and thinking off vs low.
- **Acceptance bar (proposed; the owner may change it):** for the comment route, sentiment macro-F1 ≥ 0.75 and aspect pair F1 ≥ 0.65 on tracked brands, with refusal rate < 1%. The final choice is the cheapest model per route within the confidence interval of the best.

## 16. Cost model

**Assumptions** (replace with measured numbers from `sb-eval run` and `make pilot-stats`):
- **Volume:** posts = items / 58.3 (pilot ratio); media = 1 per post, 80% images and 20% videos of 45 s.
- **Tokens per unit** (uncached in / cached in / out), compact format (prompt `p3`): comment 60 / 110 / 50; post 700 / 600 / 150; image 1,100 / 550 / 180; video 3,250 / 550 / 220. The cached prefix includes the flag definitions; ads and breaches cost no output when absent.
- **Duplicates:** 10% of comments share an identical comment's answer (same thread, parent and text); `make pilot-stats` measures it.
- **Other shares:** 2% of analysed comments escalated to Sonnet 5.5 batch; 0.02% audit on Opus 5.5.
- **Speech-to-text:** Gemini 3.5 Transcribe at $0.0051/min, or an own Whisper server at a fixed ~$2k/year (mix `lean+whisper`).
- **Prices:** list prices of 2026-11-01. Batch is 50% off where offered. Media uses the real-time lane.

`sb-eval cost` (`make eval-cost`) prints these tables from code (`internal/analysis/eval/cost.go`); `--measured results.jsonl` replaces the token assumptions with measured ones.

**Per 1M comments:**

| Model | Batch | Real time |
|---|---|---|
| Claude Haiku 5.5 | **$16** | $32 |
| GPT-5.4 nano (unverified) | $38 | $77 |
| Gemma 4 26B (Vertex, no batch) | $41 | $41 |
| Gemini 3.1 Flash-Lite | $46 | $93 |
| Gemini 3.5 Flash-Lite | $73 | $146 |
| GPT-5.4 mini (unverified) | $139 | $278 |
| Gemini 3.5 Flash | $278 | $557 |
| Claude Sonnet 5.5 | $316 | $631 |
| Claude Opus 5.5 | $631 | $1,262 |

**Yearly, by mix:**

| Mix | 100M items | 300M items | 1B items |
|---|---|---|---|
| **Lean** (Haiku 5.5 everywhere + Sonnet escalation + audit; speech on Gemini 3.5 Transcribe) | **$3.9k** | $11.7k | $38.9k |
| **Lean + own Whisper** (the same; speech on an own server) | $4.6k | **$9.7k** | **$27.8k** |
| Heavier (Haiku comments; Sonnet posts, images, videos) | $14.4k | $43.3k | $144k |
| All Sonnet 5.5 | $40.4k | $121k | $404k |
| All Gemini 3.5 Flash-Lite (+ Sonnet escalation) | $10.5k | $31.6k | $105k |
| All GPT-5.4 nano (+ Sonnet escalation; unverified) | $6.5k | $19.6k | $65.4k |

**By line** (lean | lean + own Whisper, where they differ):

| Line | 100M | 300M | 1B |
|---|---|---|---|
| Comments | $1.4k | $4.3k | $14.2k |
| Escalation | $0.6k | $1.7k | $5.6k |
| Posts | $0.1k | $0.4k | $1.3k |
| Images | $0.3k | $0.8k | $2.8k |
| Videos (vision) | $0.2k | $0.5k | $1.5k |
| Speech-to-text | $1.3k \| $2.0k | $3.9k \| $2.0k | $13.1k \| $2.0k |
| Audit | $38 | $0.1k | $0.4k |
| Topic curation, bot scoring | ~$20 | ~$20 | ~$20 |

**Infrastructure** (not in the model): Postgres storage ~330 GB/year at 300M items; ClickHouse later ~$150-300/month; one small node for workers; the Whisper server above, if chosen.

**What could break the model:**
- thinking left on (×3-10);
- groups of 1 (×2-3 on input);
- Haiku failing the bar on comments (then GPT-5.4 nano or Gemini 3.1 Flash-Lite at ~2.5-3x Haiku, or Sonnet at 20x);
- real-time lane overuse (×2);
- fewer duplicates than assumed (at 0%, comments cost 11% more);
- prompts over 100k tokens on Haiku 5.5, which double the price (impossible with these group sizes).

### 16.1 Cost revision: what changed and why (2026-11-01)

The first estimate (lean $5.0k / $15.1k / $50.2k at 100M / 300M / 1B items; heavier $16.2k / $48.5k / $162k) was too high for the owner. Every saving below keeps accuracy, speed and reliability; the levers that would cost accuracy were rejected or left to the owner.

| Change | Saving | Effect on accuracy, speed, reliability |
|---|---|---|
| **Compact answers** (prompt `p2`, schema `s2`; section 7.3): one language code, a `flags` list, whole-number scores, schema field order kept. ~45 output tokens per comment instead of ~70. | ~−30% on every model call (output is ~75% of a comment's cost) | Same information. The schema now puts the label before its confidence, which can only help. The gold-set run measures it, with the same code. |
| **Script and comment keywords computed locally** (`textnorm.Script`, `textnorm.Keywords`) | part of the above (~a fifth of a comment's answer) | Script detection is deterministic and exact. A comment's keywords are its content words; posts keep the model's keywords. |
| **Identical comments of a thread sent once** (section 7.2) | −10% of comment and escalation calls (assumed; contest threads can be far higher) | No change: the model's input would be identical. Failures and retries stay grouped. |
| **Audit sample 0.1% → 0.02%** | −80% of the audit line | ~165 audits a day at 300M items, ~1,600 a month at 100M: still enough for monthly drift tracking. Statistical power depends on the count, not the share. |
| **Own Whisper server for speech above ~150M items** (optional, `lean+whisper`) | speech $13.1k → ~$2k at 1B | Only if it passes the word-error-rate test on Iraqi clips; otherwise the API stays. One more service to run; an outage delays videos, it does not lose them. |
| **Speech cost counted in the budget** | none (a fix) | The daily cap now really caps spending: speech-to-text was not counted before. |

Result: lean **$3.7k / $9.2k / $25.9k** at 100M / 300M / 1B (taking the cheaper speech option at each volume), **−26% / −39% / −48%**. The heavier mix falls to $14.0k / $42.0k / $140k.

**The features added afterwards** (ads, legal and policy flags, new-topic proposals, positions; prompt `p3`) add ~5 output tokens per item (mostly the usually empty `new_topics`) and ~25 cached prefix tokens: **$3.9k / $9.7k / $27.8k**, +5-7%. Topic curation (a Sonnet call a day) and bot scoring (SQL) cost almost nothing.

**Considered and not done** (each would save money but risk accuracy, or needs the owner):
- **A positional answer format** (one string of codes per item, ~25 tokens): another ~−30% on comments. Models make more field-position errors in such formats; worth a gold-set comparison, not a blind switch.
- **Smaller images** (1024 → 768 px, ~−25% of the image line, ~$0.7k at 1B): small Arabic text in images (OCR) suffers. Measure on the gold images first.
- **A batch lane for media** (−50% of images and video vision): the file's `delete_after` deadline and batch latency (up to 24h) conflict. It needs a deadline guard; worth it only if images move to Sonnet.
- **Haiku with thinking instead of Sonnet for escalations** (~−70% of the escalation line, estimated with ~300 thinking tokens): unknown accuracy on exactly the items that matter most (unsure mentions of client brands). The evaluation can compare them (`--thinking adaptive`).
- **Sampling very large threads** (analyse every brand mention, and a share of the rest, weighted in the rollups): large savings on viral posts, but per-item completeness is lost. A product decision for the owner, not taken.
- **A cheaper model:** none is cheaper than Haiku 5.5 at these prices (GPT-5.4 nano is ~2.4x, Gemini 3.1 Flash-Lite ~2.9x per comment).
- **Self-hosting or distillation** (section 17): pays only above ~3B comments a year, or if processors are forbidden.

## 17. Self-hosting and distillation

- **Break-even:** 2x H100 cost ~$44k/year on demand, before an engineer's time. Against Haiku 5.5 batch (~$15 per 1M comments) that is ~3B comments/year; against Sonnet-tier it is ~150M/year.
- **When it pays:** at 300M/year, APIs win unless a privacy decision forbids processors.
- **Path when it pays:**
  1. collect API labels with provenance and the audit agreement;
  2. fine-tune a small open model (Gemma 4 E4B/12B; check licence terms) on audited labels, so one model still returns every field;
  3. serve it with vLLM behind the existing OpenAI-compatible provider;
  4. gate it with the same harness and overlap procedure (section 7.6).
- **A sentiment-only encoder** (MARBERT/CAMeLBERT) is cheaper to run but covers one field. Licences of some Arabic encoders restrict commercial use.

## 18. Rollout

1. **Merge this work.** Run `scripts/analysis/pilot-stats.sql` on the pilot (counts only) to measure the script mix (Kurdish, Arabizi), lengths, duplicates and tag-only share. Adjust skip rules and token assumptions.
2. **Label the gold set** (owner's labeller, ~3-4 working days for 1,000 comments + 200 posts + 100 images). Run the harness with the owner's API keys and pick a model per route.
3. **Turn on the service in the pilot** with the chosen config and a daily budget. Watch cost per item and audit agreement for a week.
4. **Before the agent:** add ClickHouse fed from Kafka, create the agent roles, and build the retrieval evaluation.

## What is built (this change)

- **Contracts:** `securebrand.content.v1` (`ContentItem`, `MediaReady`/`MediaProcessed`, `Erasure`) and `securebrand.analysis.v1` (`ItemAnalysis`, `MediaAnalysis`), with topics, the Schema Registry mapping and `buf lint`/`buf breaking` clean. Documented in [content-v1.md](../contracts/content-v1.md).
- **`cmd/sb-analysis`** (roles `ingest`, `pipeline`, `media`, `relay`, `rollup`):
  - **inputs:** the Facebook adapter and the canonical input, skip rules, PII masking, and the alias matcher with clitic handling;
  - **pipeline:** the Postgres work queue with leases, backoff and dead letters; real-time and Message Batches lanes; identical items of a thread sent once; the compact answer format;
  - **providers:** Anthropic on the official Go SDK v2 (structured output, cached system prompt, per-model thinking control, batches); OpenAI-compatible (chat with strict JSON schema, transcription); a deterministic fake;
  - **reliability:** circuit breakers and rate limits, fallback, refusal isolation, budget caps, escalation and audit sampling, calibration maps;
  - **output:** the transactional outbox and relay;
  - **media:** images resized; videos as ffmpeg key frames plus speech-to-text (its cost logged and budgeted); acks through the outbox;
  - **data management:** erasure with suppression; the entity registry with review, merge and tracking; daily rollups;
  - **agent access:** views with per-client scoping, and read-only agent roles with limits;
  - **search and metrics:** Arabic full-text search; Prometheus metrics and alert rules ([runbook](../runbooks/analysis.md));
  - **commands:** `migrate`, `entities`, `sources`, `reanalyze`, `replay-failed`, `agent-role`, `rollups`.
- **`cmd/sb-eval`** ([guide](../analysis/evaluation.md)): `sample`, `label` (web tool), `agreement`, `run`, `report`, `calibrate`, `cost`. Plus the [labelling guide](../analysis/labelling-guide.md) and a synthetic gold example.
- **Deployment:**
  - **Compose:** local stack with the fake provider. On the pilot the service sits behind the `analysis` profile, so nothing is sent before the owner decides; `make pilot-stats` gives counts only.
  - **Helm:** `analysis` and `analysis-media` components, a migrate hook, the ffmpeg image target; `make chart-check` and `make chart-smoke` pass.
  - **Retention:** fbs comment retention is now indefinite.
- **Tests:**
  - unit tests;
  - integration tests against Postgres and Redpanda: store, agent scoping, pipeline lanes and failure modes, media with real ffmpeg, Facebook topics to `analysis.result.v1` end to end, the sampler;
  - the existing fbs suites still pass;
  - a mutation check of the new tests: 16 of 16 deliberate faults caught, and 11 of 11 for the cost revision (compact format, local script and keywords, duplicate collapsing).

- **Ads, legal and policy flags, dynamic topics, bot detection** (sections 6.1-6.3): prompt `p3` / schema `s3`; migration `00002`; `store.ResolveTopics`, `ApplyTopicDecisions`, `ScoreAuthors`; `internal/analysis/curate`, `internal/analysis/bots`; `sb-analysis topics` and `authors`; `AuthorFlag` and `TopicUpdate` events; agent views with `ad_type`, `legal`, `author_bot`, `agent.topics`, `agent.rollup_flag_daily`; ad and breach labels in the labelling tool and the report; positions in the brand list (14 Iraqi offices seeded).

**Not built:** ClickHouse, embeddings and `sb-eval retrieval`, the agent itself, the speech-to-text evaluation, and the own Whisper server's deployment (added only if it passes that evaluation).

**Not measured yet:** accuracy and real token counts. They need the owner's gold set and API keys (section 15).
