# Analysis service operations (sb-analysis)

The design is in [docs/design/analysis-service.md](../design/analysis-service.md) and the evaluation in [docs/analysis/evaluation.md](../analysis/evaluation.md). Commands run in the analysis container or pod:
- **Compose (next to the fbs pilot):** `make analysis-cmd ARGS="..."` (`deploy/analysis/compose.yml`);
- **Kubernetes:** `kubectl -n analysis exec deploy/<release>-analysis -- sb-analysis ...` (the chart: `deploy/helm/sb-analysis`).

All times in reports and budgets are Iraq time (Asia/Baghdad, UTC+3).

## Roles and scaling

`sb-analysis run --roles` takes any of:
- **ingest:** Kafka to Postgres;
- **pipeline:** the real-time and batch lanes;
- **media:** images and videos;
- **relay:** the outbox to Kafka;
- **rollup:** daily rollups, the daily topic curation and the daily bot scoring (each job takes a database lock, so replicas never overlap).

The Helm chart (`deploy/helm/sb-analysis`) runs `analysis` (all roles but media) and `analysis-media` (media, on the ffmpeg image `sb-analysis-media`), and `sb-analysis migrate` as a pre-install and pre-upgrade hook. The hook needs only the config and Postgres (no Kafka, Valkey, S3 or AI key), and its error line says what failed: `pg: ping:` for the connection or login, `permission denied` for missing rights.

**PostgreSQL in production** is reached through DigitalOcean's PgBouncer in transaction mode, with 50 connections for every service.
- sb-analysis keeps no prepared statements: pgx describes each statement once and caches only the description (`cache_describe`, set in `internal/platform/pg`).
- It keeps no session state: only transaction-level advisory locks, with no `SET`, temporary tables or `LISTEN`.
- It opens at most 4 connections per process, and closes idle ones after 30 seconds.
- `make test-pgbouncer` (and CI) run the tests through a PgBouncer set up the same way.
- Errors like `prepared statement … does not exist` mean something bypasses `pg.Open`.
- **A full database** is waited for, with `pg: server busy; retrying` and the step (`connect` or `migrate`) in the log:
  - what counts as full: PostgreSQL's `too many connections` (53300) or `starting up` (57P03), and PgBouncer's own `query_wait_timeout`, `server login has been failing … (server_login_retry)`, `no more connections allowed`, `client_login_timeout` and `pgbouncer cannot connect to server`;
  - a pod's start waits about two minutes, then fails and the pod restarts;
  - the migrate hook waits up to 30 minutes, then fails: a failed hook blocks the deployment. Each migration is its own transaction, so a migration cut off by a full pool runs again;
  - any other error stops at once.
- **It touches the database as little as it can while idle** (the master, 7 Nov: idle polls kept the shared database's connections busy):
  - an empty queue is polled by one worker for all of them, and less often each time it is still empty: the real-time lane from `realtime.poll` (1 s) up to `realtime.idle_poll` (15 s), the media queue from `media.poll` (2 s) up to `media.idle_poll` (30 s), and the outbox from 0.5 s up to 10 s. Work found sets the wait back and wakes every worker;
  - a real-time item is claimed within `idle_poll` after its 20 s `wait`, so still within about a minute;
  - `/readyz` pings PostgreSQL at most once a minute and answers from that ping in between, a failure included; `/healthz` (liveness) doesn't touch the database;
  - the queue and outbox gauges are refreshed once a minute while idle.
  An idle `analysis` pod sends about 20 queries a minute (before: about 12 a second); an idle `analysis-media` pod under 10 (before: about 2 a second).

Credentials come from the platform's secrets (`platform-credentials`, `ai-provider`); the chart creates no topics and registers no schemas. Scaling:
- **Ingest:** scales on Kafka lag (KEDA).
- **Pipeline:** I/O-bound; one replica handles ~10 items/s with batching. More replicas share the queue safely (`SKIP LOCKED`).
- **Media:** scale on `sb_analysis_media_ttl_at_risk` and the pending `media_jobs`.

## Lanes: real time or batch

- **Real-time lane (results within minutes):**
  - items that mention an entity a client tracks (`analysis.urgent.tracked_entities`, `sb-analysis entities track`);
  - items from hot-tier accounts: their posts, and the comments on them.
- **Batch lane:** everything else, at half price (within 24 hours).
- **The hot tier, until the platform's account registry publishes tiers:**
  - `analysis.urgent.hot_accounts` (source ids such as `fb:page:123`);
  - the accounts clients monitor (`sb-analysis sources monitor --client c --source fb:page:1`), re-read every `analysis.urgent.refresh` (1 minute). Turn that off with `analysis.urgent.watchlists: false`.

  A failed read keeps the last list. Media (images, videos) stays real time: its files are deleted at their deadline.
- **To look:** `sb_analysis_queue_depth{lane="realtime"}` against `{lane="batch"}`. Each real-time item costs about twice a batch one, so every hot account adds to the daily spend, which the budget caps.
- **How a batch round works** (every `batch.submit_every`, 10 minutes):
  - it claims up to `batch.max_items` (10,000) due items per route, leased for `batch.submit_lease` (15 minutes) only;
  - it builds the requests and sends them in batches of `batch.max_requests` (1,000). Each request repeats the prompt and output schema (about 23 KB), so a batch body is about 25–35 MB in memory;
  - once a batch is recorded, its items are leased for `batch.lease` (26 h, longer than the provider's 24 h);
  - on an error before sending, the round closes the requests it recorded and releases the rest.
- **Work a stopped round left behind** (a crash or restart between claim and send) comes back at the start of the next round, with `analysis: released batch work that was claimed but never sent` in the log:
  - requests recorded but never given a batch, older than `batch.submit_lease`, are marked failed, so their estimates stop counting against the day's budget;
  - items leased long but not in a sent batch are released.
  - Before 11 Nov a round leased its items for 26 h before sending, and claimed up to 100,000 items into batches of 5,000 requests (over 100 MB each). A crash then held them for 26 h, with their estimates counted against the budget.
- **To see the open batches:** `sb-analysis batches [--since 24h]`, run in the analysis pod. It prints each open or recent batch with its requests here (submitted, done, failed) and the provider's status and counts (processing, succeeded, errored, canceled, expired), with when it was sent and when it expires. It is read-only and prints no content.
- **A provider without a batch API, or the fallback after `batch.fallback_after`,** analyses a round's items in real time under the same claim. With more than one replica, a round that takes longer than `batch.submit_lease` can be claimed again by another.

## Queue backlog

**Look:**
- `sb_analysis_queue_depth` and `sb_analysis_queue_oldest_seconds` by lane and route;
- `make analysis-status` on the pilot.

**Causes, in order:**
1. **The budget paused work** (`sb_analysis_budget_paused == 1`): see [Budget](#budget).
2. **Provider errors** (`sb_analysis_provider_requests_total{outcome="error"}`): see [Provider outage](#provider-outage).
3. **Batches not collected** (`sb_analysis_open_batches` grows): check that a pipeline replica runs and can reach the provider. Batches finish within 24h; results stay downloadable for 29 days, so a restart collects them.
4. **Real-time lane slow** at a burst: raise `analysis.realtime.workers`, or set `analysis.urgent.tracked_entities: false` to send fewer items there.

**The queue's cap (backpressure):** at `analysis.backpressure.max_queue` items (200,000), ingest stops taking new posts and comments: `sb_analysis_ingest_paused` is 1, and they wait in Kafka. Media announcements and erasures keep flowing. Ingest resumes once the queue is down to `resume_queue` (150,000); the queue can pass the cap by at most one ingest batch.
- **Kafka keeps 7 days.** Content paused longer than that is gone from Kafka, and comes back only through a backfill from the permanent store. `AnalysisIngestPaused` warns after an hour paused.
- **To clear it,** fix the cause above. Raise `max_queue` only if Postgres has room: each queued item is a row.

## Media

`sb_analysis_media_ttl_at_risk > 0` means files are within 6h of deletion (`delete_after`). Files deleted before analysis are lost for it: fbs logs `ttl_expired`.
- Scale `analysis-media`, or raise `analysis.media.workers`.
- `failed_permanent` acks for videos with "ffmpeg not found" mean the media workers run the default image: use the media image `sb-analysis-media:dev` (`make docker-analysis-media`; Compose: `SB_ANALYSIS_IMAGE=sb-analysis-media:dev`).
- **What is analysed:**
  - fbs's media: `analysis.media.kinds` lists the kinds analysed;
  - the contract's media: `analysis.media.contract_kinds` lists the type/role pairs analysed (default: images and videos that are the content or an attachment, and cover images).
  - Others are acknowledged without analysis: `PROCESSED` on fbs's topic (as before), `SKIPPED` with reason `policy:type/role` on the contract's.
- **The contract's acknowledgements** (`content.media.processed.v1`):
  - each carries the consumer `sb-analysis`, an `event_id` of its own, and the platform code;
  - skips and failures carry a short `reason` (`policy:sticker/attachment`, `too_large`, `undecodable`, `gone`, `no_ffmpeg`, `route_disabled:image`, `refused:<model>`, `erased_subject`), with the details in `error`.
- **Identical files:** a contract file whose SHA-256 was analysed before, under another media ID, reuses that analysis. No download and no provider call; `media_analyses.reused_from` names the source.
- **Files deleted first** (`content.media.deleted.v1`): a job still waiting is closed (`media_jobs.state = 'gone'`) and not acknowledged.
- **Where media comes from:** only files a collector downloaded and announced on `content.media.ready.v1` (or fbs's `fb.media.ready.v1`). The media metadata on `content.media.v1` (URLs, sizes) is not a hand-off and isn't read.
  - An announcement without a bucket or key can't be fetched and isn't queued. It counts in `sb_analysis_media_total{outcome="no_location"}`, with `ingest: media announcements without a file location skipped` in the log.
  - fbs's own media (`fb.media.ready.v1`) is analysed and acknowledged on `fb.media.processed.v1`, but its results stay in Postgres until fbs moves to the contract (owner decision 17), so it isn't published on `analysis.media.v1`.
- **Transcripts:** a video of a contract post with captions uses the captions (the uploader's first, then the platform's speech-to-text; never a machine translation) and buys no speech-to-text. `transcript_source` says which: `platform_captions` or the speech-to-text model.
- **Memory and disk:** a video streams to a temporary file (`/tmp`, up to `analysis.media.max_bytes`, 500 MiB) and never passes through memory. An image is read into memory up to `image_max_bytes` (32 MiB) and `image_max_pixels` (40 megapixels, judged from its header before decoding); larger files fail permanently and are acknowledged. At start, `workers` is lowered to fit the container's memory limit (`worker_memory_mib` each, after 256 MiB), with a warning in the log. The chart gives `analysis-media` 2 GiB of memory and a 3 GiB `/tmp`.

## Speech-to-text

Videos are transcribed by a speech-to-text server inside the cluster: **Speaches** with Whisper large-v3-turbo, CPU only. The chart's `whisper` component runs it, and production turns it on (the owner, 7 November 2026). The audio never leaves the cluster.

- **Captions first:** a video whose post has the platform's captions uses them, and isn't sent to Whisper.
- **Limits:**
  - one transcription at a time per media process (`analysis.media.video.stt_concurrency`);
  - the first 10 minutes of a video at most (`max_seconds`).
  - A video that finds the slot taken waits `stt_wait` (5s), then goes back to the queue for `stt_retry_after` (2m) without counting a try: images keep flowing, and the file's deadline still applies.
- **Slow by nature:** on shared CPUs, Whisper runs at about real time or slower.
  - In production a call may take 20 minutes (`analysis.providers.whisper.timeout`), and a media job's lease is 45 minutes.
  - A failed call isn't retried at once; the media queue retries the video with its backoff.
- **Pinned:** the image by digest and the model by revision (`whisper.image.digest`, `whisper.model.revision`).
  - The init container downloads that revision once onto the pod's 10 GiB volume, and later starts find it there.
  - It downloads by plain HTTP, streamed to the volume (`HF_HUB_DISABLE_XET=1`), with a 3 GiB memory limit. The Xet client buffered the 1.6 GB model in memory and was OOM-killed at 1 GiB in production (7 Nov).
  - The server runs offline (`HF_HUB_OFFLINE=1`), so it never fetches anything else.
  - To change the model, change the revision: the init container downloads it, points the cache at it and removes the old one.
- **Rollout:** `Recreate`, because the volume can be mounted by one pod at a time.
- **To look:**
  - `sb_analysis_media_pending{type="video"}`: the videos waiting;
  - `sb_analysis_transcriptions_total{outcome}`: calls that worked or failed;
  - `sb_analysis_stt_deferred_total`: videos put back while the slot was busy.
- **Alerts:**
  - `AnalysisVideoBacklogGrowing`: the video backlog has grown for an hour;
  - `AnalysisTranscriptionsFailing`: speech-to-text calls are failing.
- **If it can't keep up:** the options are a smaller Whisper model, CPU-optimized nodes, or a speech-to-text API. Each is a cost choice for the owner.
- **If it fails:**
  - look at the `…-whisper` pod: the init container's log shows the model download, the server's log the calls;
  - check the volume (`kubectl get pvc`);
  - check that `analysis.providers.whisper.base_url` (set by the chart) reaches the Service.

## Dead letters

Items that failed `analysis.limits.max_attempts` times are in `analysis.dead_letters` with the last error, and `sb_analysis_dead_letters` counts them.
1. Read the errors: `SELECT route, left(error, 120), count(*) FROM analysis.dead_letters GROUP BY 1, 2 ORDER BY 3 DESC;`
2. Fix the cause (a schema change, a model that keeps truncating: raise the route's `max_tokens`).
3. Queue them again: `sb-analysis replay-failed [--route comment]`.

## Refusals

The model's safety classifiers can decline content (violence, hate).
- A refused group is retried item by item. A refused single item goes to the route's fallback provider if one is configured, and is otherwise stored with `refused = true` (neutral, no entities).
- A refusal rate above 2% (`AnalysisRefusalsHigh`) skews toxicity and sentiment statistics: configure a `fallback_provider` for the comment route, and report the rate in the evaluation.

## Provider outage

Every provider has a circuit breaker.
- **Real-time lane:** while the breaker is open, requests go to the route's `fallback_provider` / `fallback_model`, recorded with `fallback = true`.
- **Batch lane:** waits and retries every 5 minutes, without counting attempts.
- `sb_analysis_fallback_total` over 5% (`AnalysisFallbackShareHigh`) means results from two models are being mixed: trends are less comparable for that period.

API keys come from the environment: `SB_ANALYSIS_ANALYSIS__PROVIDERS__ANTHROPIC__API_KEY` (and `__OPENAI__`, `__GEMINI__`).
- **Compose:** in `deploy/analysis/analysis.env` (mode 600).
- **Kubernetes:** in the `secretEnv` Secret.

A 401 in the logs means the key is missing or revoked.

## Rate limits

`analysis.providers.<name>.rps` caps the requests a second to a provider. With `valkey` set (production: the platform's `VALKEY_*`), the cap is shared by every pod: four pods together send at most `rps`. Valkey's own clock decides, so the pods' clocks don't matter. Without Valkey, each process limits itself.
- **Valkey unreachable:** each pod falls back to `rps / analysis.rate_limit.fallback_divisor` (4) and counts `sb_analysis_rate_limit_fallback_total`; the service keeps running. With more pods than the divisor, the provider sees more than `rps`, and its 429s are retried. Fix Valkey, or raise the divisor to the number of pods.
- **Too slow:** a provider's queue grows while `rps` is reached: raise `rps` within the provider's own limits (the Anthropic console shows them).

## Budget

`analysis.budget.daily_usd` caps spend per Iraq day; `analysis.routes.<route>.daily_usd` caps one route. Spend is measured from real token usage at list prices (`analysis.pricing`), plus speech-to-text minutes at the transcription model's `per_minute` price (logged as `video` requests of that model; an own Whisper server is priced 0).
- **No cap only on purpose:** outside `service.env: dev`, a `daily_usd` of 0 (no cap) stops the service at start with an error, unless `analysis.budget.allow_unlimited: true` is set.
- **Production: $10 a day, a hard stop** (the owner, 7 November 2026; D-031): `daily_usd: 10`, `urgent_exempt: false`.
  - **At the cap:** every paid call waits until 00:00 Iraq time: real-time and batch items, images and videos, and the topic and entity curation.
  - **To see it:** `sb_analysis_budget_paused` is 1, and `AnalysisBudgetReached` fires.
  - **Models:** Claude Haiku for everything, with escalation and audit off. The own Whisper server is free, so it isn't limited.
  - `TestProductionConfig` fails if production's settings drift from that.
  - The pilot kept $5 a day.
- **At the cap:** paid work pauses (items wait); urgent real-time items continue only if `analysis.budget.urgent_exempt` (off in production). The topic and entity curation wait too.
- **To look:** `sb_analysis_spent_today_usd`, `sb_analysis_cost_usd_total` by model and route, or `make analysis-status`.
- **To change:** set the budget in the config and restart the analysis containers. A new day starts at 00:00 Iraq time.

Keep the price table in `configs/default.yaml` current when providers change prices.

## Outbox

Results (`analysis.result.v1`, `analysis.media.v1`) and media acks (`fb.media.processed.v1`, `content.media.processed.v1`) leave through `analysis.outbox`, published by the relay role.
- A growing `sb_analysis_outbox_backlog` means the relay is down or cannot produce.
- **A record Kafka refuses** (its topic not writable by the Kafka user, for example) waits and is tried again: after 5 s, then twice as long each time, at most every 10 minutes. The records behind it keep flowing, and nothing is dropped.
  - `sb_analysis_publish_failed_total{topic}` and `sb_analysis_outbox_held` show it, the `AnalysisPublishRefused` alert fires after 30 minutes, and the relay logs `analysis: outbox relay` with the counts by topic.
  - Since 11 Nov. Before, the relay sent the oldest 500 records all or nothing, so one refused record held back everything behind it.
  - sb-analysis writes `analysis.*` and `content.media.processed.v1` (the media acks, as consumer `sb-analysis`): its Kafka user needs write on both.
- Acks wait in the outbox too, so a long outage can push media past its TTL.
- Records are delivered at least once: consumers deduplicate on `event_id`.

## Erasure

`fb.erasure.v1` and `content.erasure.v1` reach the ingest role. **What happens is set by `analysis.erasure.mode`:**

| Mode | Effect |
|---|---|
| **`record`** (default; the owner keeps all data) | The request is stored in `analysis.erasures` (subject type and key, `mode = record`) and **nothing is deleted, redacted or suppressed**. A comment the collector later publishes as `redacted` keeps its text and results here (counted as `RedactedKept`). |
| `off` | The event is counted and logged, nothing else. |
| `apply` | The data is deleted (below). |

**What a deletion does** (in `apply` mode, or with `sb-analysis erasures apply`):
- **author:** their items are redacted and their analyses, mentions, media analyses, bot state and correction log deleted;
- **source:** everything of the page is deleted;
- the subject goes into `analysis.erased_subjects`, so later items and late batch results are dropped;
- pending media jobs are acknowledged.

**Commands:**
- `sb-analysis erasures list`: the requests received, their mode and whether they were applied (no content).
- `sb-analysis erasures apply --request <id> --yes-delete`: applies one recorded request, **on the owner's written order only**. It can't be undone. On the pilot: `make analysis-cmd ARGS='erasures apply --request <id> --yes-delete'`.

Only fbs creates these requests, through its admin-only API (`POST /erasure-requests`, `DELETE /pages/{id}?purge=true`).

## Entities: brand list and review

- **Import the owner's brand list** (same format as `configs/analysis/entities.seed.yaml`): `sb-analysis entities import --file brands.yaml`.
- **Track a client's brands:** `sb-analysis entities track --client acme --entity-id asiacell --role own` (own, competitor, watch). Tracked entities make items urgent (real-time lane) and eligible for escalation.
- **New entities are added automatically** (`analysis.entities.auto`): every 6 hours the curation model decides the names seen 5+ times (merge a spelling, approve a new entity, reject a private person or generic word). Run it now with `sb-analysis entities curate`. Decisions record who made them (`entity_candidates.resolved_by`).
- **Review names yourself** (optional): `sb-analysis entities review --min-seen 5`, then for each candidate:
  - **accept:** `sb-analysis entities resolve --norm <norm> --type brand --action accept --entity-id new-id --name "Name"`;
  - **merge into an existing entity:** `--action merge --entity-id asiacell`;
  - **reject:** `--action reject`.
  Earlier mentions are re-linked and rollups rebuilt.
- **Merge duplicates:** `sb-analysis entities merge --from old-id --into kept-id`.
- **People policy:** public figures only, always reviewed. Private persons are never stored.

## Topics: the growing list

The model proposes topics missing from the list. They are deduplicated at once (same words in English or Arabic, or a brand or person name, which becomes an `@entity` topic) and kept as candidates on the items. Once a day, candidates used by `analysis.topics.approve_min_items` items go to the curation model: synonyms are merged, names rejected, the rest approved, and a new taxonomy version is recorded.
- **Find a topic from words** (English or Arabic): `sb-analysis topics find "electrical cuts"`. It prints the topics, their sub-topics, the number of items and the SQL condition.
- **Look:** `sb-analysis topics list` (candidates, most used first); `--status active|merged|rejected|all`.
- **Decide yourself:**
  - `sb-analysis topics approve --id services.water_shortage [--en "Water shortages" --ar "شح المياه" --parent services]`;
  - `sb-analysis topics merge --from services.power_cuts --into services.electricity`;
  - `sb-analysis topics reject --id other.some_gossip`;
  - `sb-analysis topics rename --id … --en … --ar …`.
  Merges and rejections re-point stored analyses and rebuild their rollups.
- **Run the curation now:** `sb-analysis topics curate`.
- **Import a new taxonomy file** (topics added or renamed; nothing is deleted): `sb-analysis topics import --file taxonomy.v2.yaml`.
- **Turn the growth off:** `analysis.topics.dynamic: false` (the file becomes a fixed list again).

## Bots: flagged authors

Once a day every author with 5+ items in 30 days is scored from behaviour (bursts, volume, the same text in many threads, the same text as many other authors on the same day, replies within a minute, round-the-clock activity, spam share). Smaller accounts (2+ items) are scored when they took part in coordination, so networks of accounts that each post a little are caught. Score ≥45 is `suspected`, ≥70 `likely`.
- **Look:** `sb-analysis authors list` (flagged, highest score first, with reasons). The author column shows:
  - for items read from the contract, the author's account ID (`tt:account:123`);
  - for items read from fbs's `fb.*` topics, fbs's pseudonymous author key, in hex.
  - Authors the platform hides are never scored.
- **Decide:** `sb-analysis authors mark --key <author> --as human --by "<your name>" --note "<why>"` ("not a bot"); `--as bot` (shows as `confirmed`); `--as none` (back to the score).
  - `<author>` is what the list shows: an account ID or a hex key. With an account ID, the platform comes from the ID.
  - Every item of the author follows, earlier ones too.
  - Only authors with an account ID are published on `analysis.author.v1`, keyed by that ID.
- **The log of decisions:** `sb-analysis authors log [--key <author>]`: who, when, why, the level before and after.
- On the pilot, run these through `make analysis-cmd ARGS='...'` (example: `make analysis-cmd ARGS="authors list"`).
- **Score now:** `sb-analysis authors score`.
- **Tune:** `analysis.bots` (thresholds, window, minimum items). Before showing bot flags to clients, review a sample of flagged authors: false positives (super-fans, giveaway hunters) are expected.

## Re-analysis

Always estimate first:
```bash
sb-analysis reanalyze --model claude-haiku-5-5 --since 2026-10-01 --dry-run
```
Then queue with a cap:
```bash
sb-analysis reanalyze --model claude-haiku-5-5 --since 2026-10-01 --max-usd 50
```
Filters: `--platform`, `--source`, `--entity`, `--taxonomy-not tax-v1`, `--sample 0.1`, `--limit`. Re-analysis runs on the batch lane at background priority and stops at the budget.

## Changing a model, prompt or taxonomy

1. Run the evaluation with the new configuration (`sb-eval run` + `sb-eval report`). It must match or beat the current one within the confidence interval.
2. Run both on an overlap sample. Then compare the label distribution of `purpose = 'current'` analyses before and after the switch over the same items:
   ```bash
   sb-analysis reanalyze --sample 0.01 --since <30 days ago> --route <route> --max-usd <cap>
   ```
   with the new model configured on that route.
3. Change `analysis.routes.<route>`. At start the service records the change in `analysis.model_changes`, the series break that dashboards and the agent must show.
4. If trends must stay continuous for a client, re-analyse their tracked entities' history (see [Re-analysis](#re-analysis)).

## Agent access (read-only roles)

```bash
sb-analysis agent-role --client acme --name "Acme" --role sb_agent_acme --password-file /run/secrets/sb_agent_acme
```
This creates a login role that:
- can read only the `agent` views, scoped to the client's tracked entities (`client_entities`) and monitored sources (`sb-analysis sources monitor --client acme --source fb:page:123`);
- runs read-only, with a 15 s statement timeout, a 1 GB temp-file limit and 5 connections.

The password file is created by the operator. Never pass passwords on the command line or in chat.

## Metrics

| Metric | Meaning |
|---|---|
| `sb_analysis_items_ingested_total{outcome}` | new, edited, unchanged, skipped, deleted, redacted, suppressed |
| `sb_analysis_items_analyzed_total{route,model,outcome}` | written, stale (edited meanwhile), duplicate |
| `sb_analysis_items_failed_total{route,reason}` | missing, invalid, truncated, refused, error |
| `sb_analysis_items_deduplicated_total{route}` | items that shared the answer of an identical item of the same thread and parent (not sent to the model) |
| `sb_analysis_cost_usd_total{route="curation"}` | the daily topic curation calls |
| `sb_analysis_tokens_total`, `sb_analysis_cost_usd_total` | token usage and spend by model and route |
| `sb_analysis_queue_depth`, `sb_analysis_queue_oldest_seconds` | queue by lane and route |
| `sb_analysis_spent_today_usd`, `sb_analysis_budget_paused` | budget |
| `sb_analysis_media_total`, `sb_analysis_media_ttl_at_risk` | media outcomes and deadlines |
| `sb_analysis_output_repairs_total` | values validation fixed (invalid topics, private persons dropped) |
| `sb_analysis_fallback_total`, `sb_analysis_refusals_total` | fallback use and refusals |
| `sb_analysis_outbox_backlog`, `sb_analysis_published_total` | outbox |
