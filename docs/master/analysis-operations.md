# sb-analysis: operations and integration (Parts C and D)

*For the master session, in the layout of 07-operations-questionnaire.md. From the code at `Abdullah95k/securebrand-microservices-analysis`, branch `claude/jolly-bell-mb5sct`, commit `bf32dd9`, 5 November 2026 (Iraq time).*

**Nothing in this file is measured on real data.** sb-analysis has never run on real content, and the evaluation hasn't been run. Every number marked "estimate" comes from the cost model (`internal/analysis/eval/cost.go`, `docs/design/analysis-service.md` section 16), from list prices checked on 1 November 2026, or from the pilot ratios fbs reported.

## 1. Components

**One binary,** `sb-analysis run`, with **roles** that can run together or as separate Deployments.

| Role | What it does | Copies | Scales out? | State |
|---|---|---|---|---|
| `ingest` | Reads the collector topics into Postgres: stores the item, de-duplicates by text hash, and queues new or changed items. Offsets are committed after the database write. | 1+ | Yes (Kafka consumer group `sb-analysis`; at most one copy per partition) | Postgres |
| `pipeline` | Claims queued items (`FOR UPDATE SKIP LOCKED`) and groups them per thread. Real-time lane: one model call per group. Batch lane: provider batches submitted every 10 minutes and polled every minute. Validates the answers, stores the results and queues escalations and audits. | 1+ | Yes (queue leases) | Postgres |
| `media` | Image and video analysis. Reads the file from S3; ffmpeg cuts key frames and audio; optional speech-to-text; one vision call; writes the acknowledgement. | 1+ | Yes (job leases, nearest deadline first) | Postgres; temporary files deleted after each job |
| `relay` | Publishes the outbox (results, flags, topic updates, media acknowledgements) to Kafka after commit | 1+ | Yes (`SKIP LOCKED`) | Postgres |
| `rollup` | Daily rollups every 5 minutes. Also topic curation and entity curation (one Sonnet call every 6 hours each) and bot scoring (SQL, daily). | **1** recommended | Safe with more (advisory locks), but curation calls would be repeated | Postgres |

**Jobs and commands of the same binary:**
- `migrate`;
- `kafka-topics` (creates only `content.*` and `analysis.*`);
- `entities …`, `topics …`, `authors …` (owner review and corrections);
- `reanalyze` (selective, budgeted re-analysis);
- `replay-failed` (dead letters);
- `rollups recompute`;
- `agent-role` (a read-only database role per client).

**`sb-eval`** is the evaluation tool. It's a job, run by hand on the pilot server against a gold file kept outside git: `sample`, `silver`, `label`, `agreement`, `run`, `report`, `calibrate`, `cost`.

## 2. Packaging and deployment

### Images

| Image | Built by | Status |
|---|---|---|
| `sb-analysis` (sb-analysis and sb-eval only, distroless, non-root) | Dockerfile target `sb-analysis`, added 2 Nov | **Not built in CI and not published.** On the pilot it is built with `make analysis-image` (tag `sb-analysis:dev`). |
| The `fbs` image | This repository's release workflow | Built from this repository's **copy** of fbs, so it also contains sb-analysis. No release tag has ever been pushed in this repository, so no such image exists. |
| `fbs:<version>-media` (ffmpeg, for video) | This repository's release workflow | Only on a tag; never published. |

**Change needed:** an `sb-analysis:<version>` image (and `-media`) from this repository's own release, never under the `fbs` name.

### Deployment

| Where | What it is | Tested |
|---|---|---|
| **Pilot:** `deploy/analysis/compose.yml` | Its own Compose project (`sb-analysis`), on fbs's network `fbs_default`. Reads Redpanda; keeps schema `analysis` in fbs's Postgres. | Locally, 2 Nov: started, migrated, created its topics, answered commands, stopped without touching fbs's containers |
| **Kubernetes:** components `analysis` and `analysis-media` in **fbs's** Helm chart (`deploy/helm/fbs`, this repository's copy) | A migrate hook, KEDA autoscaling on `fb.comment.v1` lag, alerts in fbs's PrometheusRule | `make chart-check` and `make chart-smoke` in CI |

The analysis needs a chart of its own (analysis-gaps.md, A3).

### Start-up

1. `sb-analysis run --migrate` (the default in Compose) applies the analysis migrations (goose, table `analysis_goose_db_version`, apart from fbs's). In Kubernetes a hook runs `sb-analysis migrate` and the Deployments start with `--migrate=false`.
2. `sb-analysis kafka-topics` creates its 8 topics.
3. **Schemas are not registered** (see section 4).

### Where it runs today

- **Not running anywhere.** It has never analysed real content and has never called an AI provider with real data.
- **The code** is on GitHub, in the **public** repository `Abdullah95k/securebrand-microservices-analysis`. It's only on the branch `claude/jolly-bell-mb5sct`, which is not merged (the repository has no `main`). *(09-architecture-v1.md says it is "only on the pilot server and not yet on GitHub": that is out of date.)*
- **On the pilot server (201.79.36.209), not confirmed.** On 2 November the owner was given steps to clone it into `~/securebrand-analysis`, run the counts-only statistics, and build `sb-analysis:dev`. I have no confirmation of those steps, of any API key on the server, or that it was ever started.
- **On the pilot's Schema Registry (confirmed 6 Nov, 00:58 Iraq time):** 9 subjects from this repository's copy: the 4 `content.*` value subjects, `securebrand/content/v1/content.proto`, and the 4 `analysis.*` value subjects. They must be deleted (with the owner's approval) or avoided before the master registers its v1 (analysis-contract-notes.md, section 2).
- **Probably on the pilot's Kafka:** the matching `content.*` and `analysis.*` topics (09 says they exist). They are empty, since no producer exists.

**Read-only checks for the owner on the pilot server** (they print only names and states):

```bash
docker ps -a --filter name=sb-analysis --format '{{.Names}} {{.Status}}'
```

```bash
docker exec fbs-redpanda-1 rpk topic list
```

```bash
curl -s http://127.0.0.1:18081/subjects
```

## 3. Dependencies

| Dependency | Version | Used for | Shared with another service? | Size today, and growth |
|---|---|---|---|---|
| Postgres | 18 on the pilot (any 15+) | Everything (system of record): content copy, queue, results, entity registry, topic list, bot scores, correction log, outbox, rollups, read-only agent views | **Yes, on the pilot:** schema `analysis` in fbs's database, as user `fbs`. Production: its own database and user. | 0 today. **Estimate ~1.1 KB per item** with indexes: ~330 GB a year at 300M items; **~100 GB a month per 1,000 Facebook-sized accounts** (section 13). |
| Kafka / Redpanda | Redpanda 24.2.7 on the pilot | Input `fb.*` (later `content.*`); output `analysis.*`; media acknowledgements on `fb.media.processed.v1` | Yes (fbs's cluster) | Own topics: results ~1 KB × items, 7-day retention |
| Schema Registry | — | **Not used by sb-analysis** | — | — |
| Valkey / Redis | — | **Not used** | — | — |
| Object storage (S3) | Any S3 | **Reads** media files from the collector's bucket (media role only). Writes nothing. | Yes (fbs's media bucket) | None of its own |
| ClickHouse | — | Not used yet (planned consumer of `analysis.*`) | — | — |
| Browser | — | **None** | — | — |
| ffmpeg | System package in the `analysis-media` image | Video key frames and audio | — | — |
| AI APIs | Anthropic SDK for Go v2.4.0; OpenAI-compatible HTTP for Gemini, OpenAI and an own Whisper | Every text and media analysis; curation | — | Pay per token or minute |
| GPU | — | **Not needed** (APIs only) | — | — |

## 4. Kafka

| Topic | Produce or consume | Key | Value (message type) | Partitions | Retention | Consumer group |
|---|---|---|---|---|---|---|
| `fb.post.v1` | consume | fbs's | `securebrand.fb.v1.Post` | fbs's | fbs's | `sb-analysis` |
| `fb.comment.v1` | consume | fbs's (post ID) | `securebrand.fb.v1.Comment` | fbs's | fbs's | `sb-analysis` |
| `fb.erasure.v1` | consume | fbs's | `securebrand.fb.v1.ErasureEvent` | fbs's | fbs's | `sb-analysis` |
| `fb.media.ready.v1` | consume (media role) | fbs's | `securebrand.fb.v1.MediaReady` | fbs's | fbs's | `sb-analysis` |
| `fb.media.processed.v1` | **produce** (acknowledgements, consumer `analysis`) | media ID | `securebrand.fb.v1.MediaProcessed` | fbs's | fbs's | — |
| `analysis.result.v1` | produce | content ID | `securebrand.analysis.v1.ItemAnalysis` | 96 (pilot: 3) | 7 days | — |
| `analysis.media.v1` | produce | media ID | `MediaAnalysis` | 24 (pilot: 3) | 7 days | — |
| `analysis.author.v1` | produce | author key, hex | `AuthorFlag` | 12 (pilot: 3) | 30 days | — |
| `analysis.topic.v1` | produce | topic ID | `TopicUpdate` | 3 | 30 days | — |
| `content.item.v1`, `content.media.ready.v1`, `content.erasure.v1` | consume, **switched off** (`analysis.inputs: [fb]`) | content or media ID | this repository's draft `securebrand.content.v1` | 96 / 24 / 6 (pilot: 3) | 7 / 7 / 30 days | `sb-analysis` |
| `content.media.processed.v1` | produce, only with `content.*` input (off) | media ID | `securebrand.content.v1.MediaProcessed` | 24 | 7 days | — |

**Schema Registry:**
- sb-analysis registers nothing, and won't until the master publishes v1 (standing instruction).
- Caution: `analysis.proto` imports this repository's `content.proto`. Registering `analysis.*` today would also register `securebrand.content.v1`. That's why it must wait for the master's contract (analysis-contract-notes.md, section 4).
- The intended mode is `BACKWARD_TRANSITIVE`, as fbs uses.

**Message rate per 1,000 accounts** (estimate, from fbs's pilot ratio of about 3,000 items per page per day):

| Direction | Rate |
|---|---|
| **In** | about 3 million items a day (~35 a second on average; bursts during backfills); about 50,000 media files a day if fbs downloads media (the pilot doesn't) |
| **Out** | about 2.5–3 million `analysis.result.v1` records a day (skipped and duplicate items publish nothing new); about 50,000 `analysis.media.v1`; author flags and topic updates in the hundreds |

**Idempotency:**
- **Ingest** is idempotent: a re-delivered record with the same text is a no-op, and a changed text is a new version.
- **Results:** an input hash prevents analysing the same input twice with the same model, prompt and topic list.
- **Outputs** are at least once. Consumers de-duplicate by `event_id`.

## 5. Storage

### Postgres tables (schema `analysis`)

All tables are empty today.

| Tables | Purpose | Growth |
|---|---|---|
| `content_index`, `content_items` (monthly partitions) | Each item's state, text hash, and **a copy of its text** (kept forever; the owner's decision 7: "keep both copies for now") | 1 row per item |
| `analyses` (monthly partitions), `entity_mentions` | Every analysis version, and one row per (item, entity) | ~1 row per item; ~0.3 per item |
| `analysis_queue`, `dead_letters` | Work queue; items that failed 5 times | Transient |
| `provider_batches`, `provider_requests` | Every model request, its tokens and cost (the budget and the cost report read these) | ~1 per 40 items |
| `outbox` | Records waiting to be published | Transient |
| `media_jobs`, `media_analyses` | Media work and its latest result | 1 per media file |
| `entities`, `entity_aliases`, `entity_candidates`, `clients`, `client_entities`, `client_sources`, `client_roles` | Entity registry and its growth, client tracking, read-only agent roles | Small |
| `topics`, `topic_aliases`, `taxonomy_versions` | The growing topic list and its versions | Small |
| `authors`, `author_overrides` | Bot scores, and the log of people's corrections | ~1 per active author |
| `rollup_*` (source, entity, entity-topic, topic, flags; per Iraq day), `rollup_dirty` | Daily aggregates for dashboards | Small |
| `model_changes`, `erasures`, `erased_subjects` | Model-change log; the dormant erasure path | Small |

There are also read-only `agent.*` views filtered per client (row-level, by login role).

**Cleanup jobs:** none for content or results (nothing is deleted, R6). Queue, outbox and media rows clear themselves as work completes.

### Valkey, object storage and backups

- **Valkey:** not used.
- **Object storage:** reads only, from the collector's media bucket. sb-analysis acknowledges each file, and the collector deletes it.
- **What can be rebuilt:**
  - the queue, outbox and rollups (recomputed);
  - results, from the content copy, **by paying for re-analysis**.
- **What must be backed up:**
  - the entity registry and its curation decisions;
  - the topic list and its versions;
  - the bot-correction log;
  - client tracking and roles;
  - the results themselves, which are expensive to redo.

## 6. Network

- **Inbound:** `:9090`, with `/metrics`, `/healthz` and `/readyz` (`/readyz` checks Postgres only). No API port: owner commands run as one-off containers or jobs.
- **Outbound:**

| Host | What for | When |
|---|---|---|
| `api.anthropic.com` | Main provider (Messages and Batches APIs) | Always |
| `generativelanguage.googleapis.com` | Backup provider (Gemini) | Only once its key is set |
| `api.openai.com` | Optional speech-to-text or comparison runs | Only if configured |
| `whisper:8000` | An own speech-to-text server | Only if deployed |
| Postgres, Kafka and S3 endpoints | — | Always |

  **No social-media platform or CDN is ever called.**
- **Proxies:** none.

## 7. Configuration and secrets

**Configuration:**
- **Where it's read from:**
  - built-in defaults: `configs/default.yaml`, section `analysis`, inside fbs's config schema;
  - then a file: `deploy/analysis/pilot.yaml` on the pilot;
  - then environment variables with the prefix **`FBS_`** (shared with fbs; analysis-gaps.md).
- **Validated at start-up:** routes, providers, budget, media, topics, entities, bots. sb-analysis refuses to start on an invalid config and says why.
- **Important settings and their defaults:**
  - `analysis.inputs: [fb]`;
  - the routes:

    | Route | Model | Lane |
    |---|---|---|
    | comment, post | Claude Haiku 5.5 | batch |
    | image, video | Claude Haiku 5.5 | real time |
    | escalation | Claude Sonnet 5.5 | batch, when calibrated confidence is under 0.6, or sarcasm or mixed sentiment, on a tracked entity |
    | audit | Claude Opus 5.5 | 0.02% sample |

  - `analysis.budget.daily_usd` (0 = no cap; **5** on the pilot);
  - `analysis.urgent.tracked_entities: true`;
  - `analysis.privacy.mask_pii: true` (phone numbers and e-mails are masked before text leaves);
  - `analysis.privacy.mask_tagged_users: true` (tagged people's names become `@user` before text leaves; added 6 Nov);
  - `analysis.erasure.mode: record` (an erasure request is stored and deletes nothing until the owner applies it; added 6 Nov);
  - `media.kinds`;
  - `media.video.stt_provider: ""` (no speech-to-text by default);
  - `topics.dynamic: true`; `entities.auto: true`; `bots.min_items: 5`.

**Secrets** (names only; environment variables; in Compose, from `deploy/analysis/analysis.env`, mode 600, never committed):

| Secret | When needed |
|---|---|
| `FBS_ANALYSIS__PROVIDERS__ANTHROPIC__API_KEY` | Always |
| `FBS_ANALYSIS__PROVIDERS__GEMINI__API_KEY` | Optional backup |
| `FBS_ANALYSIS__PROVIDERS__OPENAI__API_KEY` | Optional |
| `FBS_POSTGRES__DSN` (or `FBS_ANALYSIS__POSTGRES__DSN`) | Always |
| `FBS_KAFKA__SASL__USERNAME` / `PASSWORD` | When the cluster needs them |
| `FBS_S3__ACCESS_KEY` / `SECRET_KEY` | Media role |
| One password file per read-only client role | `sb-analysis agent-role --password-file` |

**Logging of secrets:** none are logged. A keyless hosted provider is skipped, with its name logged, never a key.

## 8. Resources

**Not measured: nothing has run under real load.**

| Component | Set today (Helm values) | Estimate |
|---|---|---|
| `analysis` (ingest, pipeline, relay, rollup) | request 250m CPU, 256 MiB; limit 1 GiB | I/O-bound. The runbook's estimate is ~10 items a second per copy with batching; one copy covers ~1,000 accounts. |
| `analysis-media` | request 500m CPU, 512 MiB; limit 2 GiB | Images are decoded and resized in memory (files up to 500 MiB are refused above that). Videos use ffmpeg with temporary files up to the video's size. |
| Local test, 2 Nov (fake provider, idle) | — | Started and stayed up. Memory was not recorded. |

- **Disk:** Postgres only (section 3). The media role uses temporary disk up to the size of the largest video in flight.
- **Network:** results are a few KB per item to the AI APIs and back; media is the file size from S3 (images resized to 1,024 px before upload; videos as 8 frames of 512 px plus audio).
- **Soak test:** none for sb-analysis (analysis-gaps.md, G3).

## 9. Observability

- **Health:** `/healthz` always answers; `/readyz` checks Postgres.
- **Metrics:**
  - **Prefix:** `fbs_analysis_` (shared with fbs; analysis-gaps.md).
  - **Families:** `items_ingested_total`, `items_analyzed_total`, `items_failed_total`, `items_deduplicated_total`, `records_undecodable_total`, `queue_depth`, `queue_oldest_seconds`, `provider_requests_total`, `provider_request_seconds`, `tokens_total`, `cost_usd_total`, `spent_today_usd`, `budget_daily_usd`, `budget_paused`, `refusals_total`, `fallback_total`, `output_repairs_total`, `follow_ups_total`, `media_total`, `media_ttl_at_risk`, `dead_letters`, `published_total`, `erasures_total`, `rollup_days_total`, `outbox_backlog`.
  - **Is it working:** `queue_oldest_seconds` by lane, `items_analyzed_total`, `spent_today_usd`, `budget_paused`, `dead_letters`, `media_ttl_at_risk`.
- **Logs:** JSON (slog). No text, keys or author data, with one exception: a failed entity-curation decision logs the candidate name (analysis-gaps.md, F2).
- **Alerts:**
  - in fbs's PrometheusRule (group `fbs.analysis`), each with a runbook link:
    - real-time lane stuck (oldest item over 2 hours);
    - batch lane over 30 hours;
    - media at risk of its deadline;
    - dead letters over 100;
    - refusals over 2%;
    - backup-provider share high;
    - the budget 80% spent;
    - an outbox backlog over 10,000;
  - **no Grafana dashboard.**
- **Owner health check** (counts only, in Iraq days):

```bash
make analysis-status
```

  **What good looks like:** most items `analyzed`; the queue small or draining; spend under the daily cap.

## 10. Reliability

- **Retries and dead letters:**
  - **Retries are bounded:** 5 attempts per item, then the dead-letter table. Replay with `sb-analysis replay-failed`.
  - **Provider calls:** retries with backoff, a circuit breaker per provider, and a route's backup provider when the main one is down or refuses.
  - **Batch outage:** after 12 hours of a provider's batches failing, items move to the backup in real time.
- **Stuck work and restarts:**
  - **Stuck work:** queue and media leases expire and are retried. Batch requests are leased for 26 hours (longer than the providers' 24-hour window).
  - **What survives a restart:** everything (all state is in Postgres; submitted batches are polled again after a restart).
  - **Graceful shutdown:** components stop on the signal. Uncommitted work is retried from its lease or offset.
- **Load and outages:**
  - **Backpressure:** none on ingest. Postgres is the buffer, so a slow provider grows the queue in Postgres rather than slowing ingest. The daily budget pauses non-urgent work when it's spent.
  - **Platform blocks:** not applicable (no platform access).
  - **Kafka down:** ingest waits; results queue up in the outbox and are published later.
  - **Postgres down:** everything stops and resumes from where it was.
  - **Valkey down:** not used.
  - **AI provider down:** the backup provider takes over, if its key is set; otherwise items wait in the queue.

## 11. Platform access and policy

- **Social-media platforms:** sb-analysis never accesses one, so logged in or out and bot checks are not applicable. It never hides automation, solves challenges or generates tokens.
- **AI providers:**
  - **Official APIs** (Anthropic Messages and Message Batches; Gemini's OpenAI-compatible endpoint; optionally OpenAI). They become processors of the comments (the owner said yes to Anthropic on 1 Nov; the lawyer was flagged to check the data-processing and retention terms).
  - **Rate limits:** 5 requests a second **per process** per provider (a guess, not measured; not fleet-wide), plus the providers' own limits.

## 12. Data contract

- **Publishes `content.*`?** No. It publishes `analysis.*` from `api/proto/securebrand/analysis/v1/analysis.proto` in **this** repository.
- **Platform topics:**
  - publishes `fb.media.processed.v1` (acknowledgements, in fbs's format);
  - consumes `fb.post.v1`, `fb.comment.v1`, `fb.erasure.v1`, `fb.media.ready.v1`;
  - **nobody consumes its outputs yet.**
- **To adopt the unified contract,** it needs from the master:
  - the v1 protos and generated Go code at a tag;
  - the decision to retire this repository's `content.v1`;
  - entity topics with the fields listed in analysis-contract-notes.md, section 5;
  - a media handoff with deadlines and acknowledgements.

  The work is 3–4 days (analysis-contract-notes.md, section 4).

## 13. Cost (per month, at 1,000 and at 10,000 accounts)

**All figures are estimates:**
- **Volume:** **3,000 items per account per day** (fbs's pilot ratio: about 450,000 items a day from about 150 Facebook pages). That gives about 91 million items a month per 1,000 accounts. Instagram, TikTok and YouTube accounts may be very different.
- **Media:** one media file per post, 80% images and 20% 45-second videos.
- **Prices:** the `lean` mix from the cost model, at list prices of 1 November 2026.

| Line | 1,000 accounts | 10,000 accounts |
|---|---|---|
| Comments (Haiku 5.5, batch) | ~$1,300 | ~$13,000 |
| Escalations (Sonnet 5.5, ~2%) | ~$510 | ~$5,100 |
| Posts | ~$120 | ~$1,200 |
| Audit (Opus 5.5, 0.02%) | ~$35 | ~$350 |
| Images (Haiku 5.5, real time) | ~$260 | ~$2,600 |
| Video vision (Haiku 5.5) | ~$140 | ~$1,400 |
| Speech-to-text (per-minute API, ~$0.0051 a minute) | ~$1,200 | ~$12,000 |
| **AI total** | **~$3,550** | **~$35,500** |
| AI total with an own Whisper server instead of the speech API (if it passes the dialect test) | ~$2,500 | ~$25,000 |
| **If every comment had to be analysed in real time** (for 1–5 minute alerts on everything; see the risks) | add ~$1,800 | add ~$18,000 |
| Servers for sb-analysis itself | 1–2 small pods (~0.5 vCPU, 1–2 GiB) | ~4–8 small pods, plus the media workers |
| Storage: sb-analysis's Postgres (its own text copy, results, mentions, indexes) | **~100 GB a month** | **~1 TB a month** |

The storage line is why sb-analysis's own text copy should end once ClickHouse holds content (analysis-gaps.md).

### Cost per provider option

All per unit, estimates at list prices. The OpenAI prices are unverified. Image token counts are Claude's: other providers count image tokens differently.

| Model | 1M comments, batch | 1M comments, real time | 1M posts, batch | 1,000 images | 1,000 videos (vision, up to 8 frames) |
|---|---|---|---|---|---|
| **Claude Haiku 5.5** (default) | **$16** | $32 | $76 | **$0.21** | **$0.44** |
| Gemini 3.1 Flash-Lite (pilot backup) | $46 | $93 | $208 | $0.56 | $1.16 |
| Gemini 3.5 Flash-Lite | $73 | $146 | $302 | $0.80 | $1.54 |
| GPT-5.4 nano (unverified) | $38 | $77 | $170 | $0.46 | $0.94 |
| GPT-5.4 mini (unverified) | $139 | $278 | $622 | $1.68 | $3.47 |
| Gemini 3.5 Flash | $278 | $557 | $1,245 | $3.35 | $6.94 |
| Claude Sonnet 5.5 | $316 | $631 | $1,480 | $4.05 | $8.76 |
| Claude Opus 5.5 | $631 | $1,262 | $2,960 | $8.11 | $17.51 |

**Speech-to-text, per 1,000 video minutes:**

| Option | Cost |
|---|---|
| Gemini 3.5 Transcribe | ~$5.10 (official price) |
| OpenAI `gpt-4o-mini-transcribe` | ~$3 (unverified) |
| An own Whisper large-v3-turbo server on one 8-CPU machine | a fixed ~$2,000 a year, enough for about 43,000 audio hours a year (estimate; needs a load test) |

## 14. Status and risks

### 1. Where it stands

**Built and tested** (unit and integration tests with race detection against real Postgres, Redpanda and Valkey; mutation-checked):
- the whole service: ingest, pipeline (batch and real time), media, outbox, rollups;
- the entity registry with automatic growth; the growing topic list; bot detection with a correction log; ads and legal flags;
- budget, escalation and audit;
- erasure (dormant);
- the evaluation harness;
- a separate pilot deployment.

**Not proven:**
- quality on real Iraqi comments (the evaluation hasn't been run);
- cost and throughput on real data;
- the media path on real files (the pilot downloads no media);
- any run in production.

### 2. Open issues

The gaps in analysis-gaps.md, chiefly:
- its own image, chart, metric prefix and env prefix;
- media results not versioned;
- `transcript_lang` not set;
- one log line that can carry a name;
- `/readyz` doesn't check Kafka;
- no dashboard;
- no soak test;
- no backups.

### 3. Top three risks

1. **Quality on Kurdish and Iraqi dialect is unknown.** It's measured only by the evaluation, which also uses the AI as the reference (no human labeller: the owner's decision 8–9). Sorani and Kurmanji are a known weak spot for most models.
2. **Latency against R4 (alerts within 1–5 minutes).** By default most comments go through the batch lane (half price, minutes to 24 hours; the owner's decision 3: "within hours is fine"). Only mentions of tracked brands and urgent sources go in real time (about a minute). Alerts on everything in 1–5 minutes would add ~$1,800 a month per 1,000 accounts. The master and the owner should decide which items are "hot" (watchlists already push tracked entities to real time).
3. **Storage and duplication.** sb-analysis keeps its own copy of every text in Postgres (~100 GB a month per 1,000 accounts). On a small shared server that copy would fill the disk. In the target architecture it should keep only working state, and ClickHouse should hold content and results.

### 4. What it needs from the owner

- The provider API keys, put on the server by the owner (never in chat).
- Approval to run the quality check on the pilot (~$3–5, light).
- Approval to switch the analysis on in the pilot (light: one small container).
- The daily budget (the owner's decision 2: "set after the test").
- The real-time scope (risk 2).
- Whether sb-analysis keeps its own copy of texts (the owner's decision 7, "for now").
- A base branch for the pull request in this repository.

## Additions requested for sb-analysis

### Models per analysis, and quality per language

| Analysis | Model or method | Where it runs | Iraqi Arabic | Sorani | Kurmanji | English |
|---|---|---|---|---|---|---|
| Language and dialect | Claude Haiku 5.5 (part of the one text call) | Anthropic API (batch; real time for urgent) | not measured | not measured | not measured | not measured |
| Script (Arabic, Latin, mixed) | Own code (`textnorm.Script`) | In process | exact by construction | exact | exact | exact |
| Sentiment (overall and per entity, with aspect) | Claude Haiku 5.5; escalation to Claude Sonnet 5.5 for unsure mentions of tracked brands | Anthropic API | not measured | not measured | not measured | not measured |
| Topics (growing list, sub-topics), entities (registry-linked), intent, emotion, toxicity, spam, sarcasm, urgency, ads, legal flags | Claude Haiku 5.5 (same call) | Anthropic API | not measured | not measured | not measured | not measured |
| Comment keywords | Own code | In process | — | — | — | — |
| Duplicate collapsing | Own code (thread, parent, text hash) | In process | — | — | — | — |
| Images (logos, text in the image, description, entities, topics, ads, flags) | Claude Haiku 5.5 vision | Anthropic API, real time | not measured | not measured | not measured | not measured |
| Video | ffmpeg key frames (up to 8, 512 px) and audio, then Claude Haiku 5.5 vision | In process + API | not measured | not measured | not measured | not measured |
| Speech-to-text | **Off by default.** Options: Gemini 3.5 Transcribe, OpenAI `gpt-4o-mini-transcribe`, or an own Whisper large-v3-turbo server | API or own CPU server | not measured (Whisper-class models are known to be weaker on dialect) | not measured | not measured | not measured |
| Topic and entity curation | Claude Sonnet 5.5, every 6 hours | Anthropic API | — | — | — | — |
| Bot and coordinated-account detection | Rules in SQL (no model) | Postgres | — | — | — | — |
| Quality audit | Claude Opus 5.5, 0.02% sample | Anthropic API | — | — | — | — |
| Backup (pilot) | Gemini 3.1 Flash-Lite (comments, posts); Gemini 3.5 Flash (escalation) | Google API | not measured | not measured | not measured | not measured |

**How quality will be measured:** `docs/analysis/evaluation.md`.
- **The sample:** 1,000 comments and 200 posts from the pilot, Kurdish and Latin-script comments oversampled to 10%.
- **The reference:** Opus labels them (`sb-eval silver`), and the production model is scored against those labels (`sb-eval run` and `report`).
- **Scores:** sentiment macro-F1 with confidence intervals, aspects, entities and linking, topics, intent, language, dialect; also cost and latency.
- **Gap:** the report doesn't yet break scores down **by language** (Sorani and Kurmanji separately). That's a small change, listed in the gaps.

### Throughput and latency (estimates; not measured)

**Text, end to end from ingest:**

| Lane | Latency |
|---|---|
| Real-time | ~30–60 seconds: it waits 20 seconds for a thread's siblings, then makes one model call of a few seconds |
| Batch | submitted every 10 minutes; the provider returns most batches within an hour, and at most within 24 hours |

**Media, per file:**

| Kind | Time |
|---|---|
| Image | one real-time call, ~3–8 seconds |
| Video | download, then ffmpeg (seconds per minute of video on one CPU), then speech-to-text if on, then one vision call; ~10–60 seconds for short videos |

**Throughput:** one small pod should keep up with ~1,000 Facebook-sized accounts. The work waits on the APIs, not on the CPU.

**Hardware:** any small CPU node. **No GPU is needed.**
- A GPU only matters for self-hosting:
  - an LLM pays off above ~3 billion comments a year (2× H100, ~$44,000 a year);
  - embeddings (one L4) and Whisper (CPU is enough) are the other cases.

### Media: what sb-analysis needs per platform

**What it uses today:**

| Item | Detail |
|---|---|
| **Files** | The original file (any size up to 500 MiB) from S3, announced on `*.media.ready` with: owner ID, kind, storage object, duration, alt text and a deadline (`delete_after`). |
| **Images** | Resized to 1,024 px on the long side before upload. A source of **at least ~512 px** on the long side keeps text readable. |
| **Videos** | Only the first **10 minutes** are analysed (`max_seconds: 600`). SD video is enough: frames are 512 px; audio is 16 kHz mono. |
| **Not analysed** (acknowledged straight away) | Stickers, GIFs, profile pictures, and thumbnails of videos that are analysed themselves |
| **Thumbnails** | **Not analysed at all today.** If a video isn't downloaded (fbs's backfill policy is `thumbnail`), nothing of it is analysed. Adding `thumbnail` to `media.kinds` would fix that (a one-line setting). |

**How long a file must exist before the acknowledgement:**
- **Normal:** images within minutes; videos within tens of minutes.
- **Under a backlog or a provider outage:** retries back off up to 30 minutes, 5 tries, so a few hours.
- **Recommendation:** keep files at least **24 hours**. fbs keeps them 72 hours, which is comfortable.
- An alert fires when a file is within 6 hours of its deadline unprocessed.

**Proposal per platform** (to settle with the master and the collectors; Q8):

| Platform | Download | Don't download | Use instead |
|---|---|---|---|
| Facebook | post photos; comment photos; videos and reels as SD | profile pictures, stickers, GIFs | the thumbnail when a video isn't downloaded |
| Instagram | post images (each image of a carousel); reels as SD | profile pictures | the cover for reels not downloaded |
| TikTok | videos as SD (or audio plus the cover) | — | the platform's **subtitles** as text where present, instead of speech-to-text |
| YouTube | thumbnail always; video SD up to 10 minutes, or audio only for long videos | — | **captions** as text where present, instead of speech-to-text |

Captions and subtitles aren't consumed today. Adding them needs a contract field (analysis-contract-notes.md) and a small change in sb-analysis.

### Embeddings: plan and status

- **Status: not built.** No embedding code exists.
- **My plan** (`docs/design/analysis-service.md` section 12.3, ADR 0006) was costed with **Gemini Embedding 2** (official price $0.10 per million tokens batch, $0.20 online) or **BGE-M3 self-hosted**:
  - BGE-M3 is MIT-licensed and strong on Arabic: ~1,000 texts a second on one L4 GPU, or 50–100 a second on 16 CPU cores;
  - candidates in the comparison: Gemini Embedding 2, BGE-M3, multilingual-e5-large and Voyage.
  - **What to embed:** all posts; comments of 5 or more words that mention a tracked entity; a 10% sample of other comments (de-duplicated).
- **How it compares with 09-architecture-v1.md section 9:**
  - **Where we agree:** selective embedding, shorter vectors (Matryoshka truncation), a recent window in the fast index, versioning by model, and a quality test before choosing.
  - **Where we differ:**
    - 09 costs OpenAI `text-embedding-3-small` and `-large`. Public reports rate those weaker on Arabic dialect, and I couldn't verify their prices.
    - My test plan adds a retrieval evaluation: ~200 queries in Arabic MSA, Iraqi dialect, Arabizi, English and cross-lingual, scored by recall@10 and nDCG@10 (`sb-eval retrieval`, planned, not built).
- **Recommendation:** run that test on Gemini Embedding 2, BGE-M3 and one OpenAI model, with Sorani and Kurmanji queries added. The API cost is small next to storage either way.

## Part D: the repository

### Which fbs code sb-analysis needs

| fbs part | What sb-analysis uses it for | Size |
|---|---|---|
| `internal/platform/kafka`, `pg`, `run`, `s3x`, `metrics`, `log`, `httpserver`, `breaker`, `config` | Generic building blocks: Kafka client and outbox producer, Postgres pool, component runner, S3 reads, Prometheus registry, logging, ops server, circuit breaker, config loading | ~2,100 lines, generic |
| `internal/config` | The **whole** fbs config struct. sb-analysis's settings are its `analysis` section, so sb-analysis loads and validates fbs's settings too. | ~960 lines |
| `configs` (embedded) | `default.yaml` (fbs and analysis defaults together), `topics.yaml`, `configs/analysis/*` | — |
| `internal/topics` | Topic names | ~100 lines |
| `internal/app` | **One function,** `KafkaClient`. Importing it links **the whole fbs crawler** into sb-analysis's binary (about 40 fbs packages: browser, parsers, proxies, scheduler…). | ~40 lines needed |
| `gen/go/securebrand/fb/v1` | fbs's public events, for the Facebook adapter, until fbs publishes `content.*` | generated |
| Build and deploy | Makefile, Dockerfile, CI and release workflows, fbs's Helm chart | — |

sb-analysis's own code is about 17,400 lines of Go (4,200 of them tests), plus its protos, migrations, configs and docs.

### Could it stand in a repository of its own?

**Yes.** Moving it takes about **2–3 working days:**
1. **The new repository** (for example `securebrand-analysis`, module `github.com/abdullah95k/securebrand-analysis`) gets:
   - `internal/analysis/…`, `cmd/sb-analysis`, `cmd/sb-eval`;
   - `api/proto/securebrand/analysis` (and this repository's `content` draft only until the master's v1);
   - `migrations/analysis`, `configs/analysis`, `deploy/analysis`, `scripts/analysis`, `testdata/analysis`;
   - the analysis docs (design, ADRs 0004–0007, runbook, evaluation, labelling guide, owner guide, content-v1).
2. **Copy the generic platform packages** (~2,100 lines) into it, until the shared platform library (Q12).
3. **Give it its own config package** with only the sections it uses: service, log, http, postgres, kafka, s3, privacy, analysis. Use the env prefix `SB_ANALYSIS_` (keeping `FBS_` accepted for a release).
4. **Depend on fbs's repository for `gen/go/securebrand/fb/v1`** at a tagged version, until fbs publishes `content.*`. Go allows importing it, because it isn't under `internal/`.
5. **Add its own** Dockerfile, CI (tests with race detection, lint, buf, the no-real-data guard), a release to `ghcr.io/abdullah95k/sb-analysis`, a Helm chart (moved out of fbs's chart, with its alerts), and the Compose file.
6. **Retire this repository** (or empty it), so fbs code exists in one place only.

### Is the fbs code in this copy newer, or different?

**fbs's own repository is the real one.** This copy is **older**:
- **Last synced:** fbs `main` `be2ec2f` (PR #42) on 2 November at 05:59 Iraq time.
- **Missing:** fbs has merged PRs #43–#49 since (`main` is `4816736`, 5 November).
- **Never deploy fbs from this repository.**

**Changes this copy made to fbs files:**

| Changes | Files |
|---|---|
| **Analysis wiring only** (move with sb-analysis, or drop) | `.gitignore`, `Makefile`, `README.md`, `configs/embed.go`, `deploy/compose/docker-compose.yml` (a local analysis service with the fake provider), `deploy/compose/local.yaml`, `deploy/docker/Dockerfile` (targets `analysis-media` and `sb-analysis`), fbs's Helm chart (components `analysis` and `analysis-media`, their values, alerts and migrate hook), `.github/workflows/ci.yml` (ffmpeg for tests; a guard against committed gold or labelled data), `.github/workflows/release.yml` (a `-media` image), `go.mod`/`go.sum` (the Anthropic SDK and `x/image`), `internal/config/analysis.go` and one line in `config.go`, `internal/platform/metrics/analysis.go`, `internal/topics/topics.go`, `schemas.go`, `configs/topics.yaml` (the `content.*` and `analysis.*` topics and schemas), `api/proto/embed.go`, one count in `internal/platform/schemareg/schemareg_test.go` |
| **A small refactor** (no behaviour change except one log message's text) | `internal/platform/run/run.go` gains `Every`; `internal/scheduler/loops.go` calls it. Not needed in fbs. |
| **A setting the master should know** | `configs/default.yaml`: this copy sets **`privacy.retention.comments_days: 0`** (keep comments forever, per R6). **fbs's own `main` still says `365`.** In fbs's code, that drops comment partitions older than a year and skips writing comments of older posts, which **contradicts R6**. That's fbs's to change. |

**There is no fbs bug fix in this copy that fbs lacks.**

**Risk:** `fbctl schemas apply` or `make pilot-up` from this copy would register `securebrand.content.v1` and `securebrand.analysis.v1` in the shared Schema Registry. Never run them from here (`make pilot-up` here refuses anyway: the proxies file isn't in this checkout).
