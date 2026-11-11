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
- **How a batch round works** (at start, then every `batch.submit_every`, 10 minutes; before 11 Nov the first round waited 10 minutes):
  - it claims up to `batch.max_items` (10,000) due items per route, leased for `batch.submit_lease` (15 minutes) only;
  - it builds the requests and sends each batch as soon as `batch.max_requests` (1,000) of them are built. Building runs at about 10 requests a second (a few database round trips each), so a round of 10,000 items takes about 10–15 minutes. Before 11 Nov nothing was sent until the whole round was built, and a restart in between lost the round's work. Each request repeats the prompt and output schema (about 23 KB), so a batch body is about 25–35 MB in memory;
  - a request counts against the budget at its estimate as soon as it is built, before it is sent;
  - once a batch is recorded, its items are leased for `batch.lease` (26 h, longer than the provider's 24 h);
  - on an error before a batch is sent, the round closes the requests built for it and releases the items not yet built. Batches already sent are followed as usual.
- **Work a stopped round left behind** (a crash or restart between claim and send) comes back at the start of the next round, with `analysis: released batch work that was claimed but never sent` in the log:
  - requests recorded but never given a batch, older than `batch.submit_lease`, are marked failed, so their estimates stop counting against the day's budget;
  - items leased long but not in a sent batch are released.
  - Before 11 Nov a round leased its items for 26 h before sending, and claimed up to 100,000 items into batches of 5,000 requests (over 100 MB each). A crash then held them for 26 h, with their estimates counted against the budget.
- **A round fills its threads** (since 11 Nov; the platform agreed): after claiming the oldest `batch.max_items` items, a round also claims each claimed thread's other waiting comments, up to `grouping.max_items` (40) a thread and `batch.top_up_items` (10,000) in all.
  - **Why:** a claim of the oldest items held one or two comments of most threads, so most requests carried one or two comments. The cached prompt and schema (most of a comment's cost) were read once for each.
  - **What it changes:** only how full requests are. The prompt, the grouping by thread and its limits are as before. Comments being retried after a failure aren't taken; they still go alone.
  - **To watch:** `sb_analysis_items_topped_up_total{route}`, the `analysis: batch round topped up its threads` log line, and in `sb-analysis stats` the items per request and the cost per 1,000 items. Also `sb_analysis_output_repairs_total` (`duplicate_item`, `unexpected_item`), in case fuller requests answer less cleanly.
  - **To turn it off:** set `batch.top_up_items: 0` and restart. The thread index it uses (`analysis_queue_thread_idx`, migration 00008) is built concurrently, without locking the queue.
- **Per platform:** `sb-analysis stats [--since 24h]`, run in the analysis pod, prints for each platform:
  - items ingested, analysed (refused among them), waiting in the queue now, and in the dead letters;
  - media files announced, analysed, and their states now (pending, done, skipped, failed, gone), with how many were acknowledged to their collector (done, skipped and failed are; gone ones aren't owed an ack) and why files were skipped (`policy:image/thumbnail` and so on);
  - the files pending now by time to their deadline (past it, within 12 h, 12-24 h, 24-48 h, later or none), and how many of them wait for their post (since 11 Nov);
  - files waiting to be tried again after a failure (now), and why files are retrying or failed (failed in the window), by kind, largest first. A reason is the error's first 100 characters with digits shown as `#`, so the same failure on different addresses counts together. Errors name a file at most, never its content (since 11 Nov);
  - the outbox waiting to be published, by topic;
  - the provider requests sent in the window, by route and lane (since 11 Nov):
    - how many were done, failed and still in flight;
    - the items they carried, items per request, and how many carried one item;
    - per done request: input, cache-read, cache-write and output tokens, and the real cost per 1,000 items;
    - why requests failed, by reason (the error's first 100 characters, digits as `#`). A failed request got no answer (never sent, aborted by a shutdown, errored or expired in its batch), so nothing was paid for it;
    - per route, the items sent in more than one request (a retry after a failure, an errored or expired batch request) and the extra sends;
    - the cache-read tokens per request are the cached prompt and schema itself;
  - what the real-time lane's rules have to work with: hot accounts in the config, the accounts clients monitor (when `urgent.watchlists` is on) and by how many clients, and the entities clients track. With none, every item takes the batch lane.

  It is read-only and prints counts only. `sb_analysis_items_ingested_total{platform}` counts each item under its own platform; before 11 Nov a batch mixing collectors counted under its first item's platform.
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

**The comment sample** (the owner's D-053, 11 Nov): `analysis.comments.sample_share` (production 0.10) is the share of comments analysed.
- **Which ones:** a comment is in or out by its content ID alone (a hash), so the same comment always gets the same answer, on every platform, the backlog included. Posts aren't sampled.
- **Outside the share:** a new comment is counted, not stored or analysed. Its raw text stays with the platform (ClickHouse; fbs's Postgres for Facebook) for a later analysis. `sb_analysis_items_ingested_total{outcome="not_sampled"}` counts these.
- **Always kept:**
  - a comment that takes the real-time lane (hot account, tracked entity);
  - a comment already stored, so its edits and deletions keep it current.
- **To read it:** `sb-analysis stats` prints per platform the comments received, those in the sample, those kept outside it, and the factor to scale analysed comments by (1/share). Counts are per Iraq day (`analysis.comment_sample_daily`). A re-sent comment counts each time, in or out alike, so sampled ÷ received stays close to the share.
- **To change it:** set the value and restart. A larger share takes every comment a smaller one took, plus more. Comments outside the old share that were never stored come back only by a backfill from the platform.

**The queue's cap (backpressure):** at `analysis.backpressure.max_queue` items (200,000), ingest stops taking new comments: `sb_analysis_ingest_paused` is 1, and they wait in Kafka.
- **Posts keep flowing** (since 11 Nov) until the much higher post cap, `max_queue_posts` (1,000,000; they resume at `resume_queue_posts`, 900,000). A post's media files wait for it (`analysis.media.owner_wait`), its captions included. Before, the pause held posts too, and tks's covers and videos waited behind them.
  - Letting posts through brought in the posts' Kafka backlog at once: about 420k items in the first hour on 11 Nov. Short posts queue on the comment route, so the post route's depth undercounts them.
  - At the post cap `sb_analysis_ingest_posts_paused` is 1, `AnalysisIngestPostsPaused` warns after 30 minutes, and the log says `new posts wait in Kafka too`. That is a safety valve against one collector's backfill.
- Media announcements and erasures keep flowing too.
- Ingest of comments resumes once the queue is down to `resume_queue` (150,000). The queue can pass the cap by one ingest batch, plus the posts.
- **Kafka keeps 7 days.** Comments paused longer than that are gone from Kafka, and comes back only through a backfill from the permanent store. `AnalysisIngestPaused` warns after an hour paused.
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
- **While the budget is paused** (`sb_analysis_budget_paused` 1), media waits too: image and video calls are paid. A job finds the day's (or its route's) cap already spent before its file is fetched, and goes back to the queue for 10 minutes without losing a try, while its deadline keeps running; the worker takes the nearest deadline first. Before 11 Nov the file was downloaded and decoded first, every 10 minutes, for nothing. Per-route caps (`analysis.routes.<route>.daily_usd`) can keep room for `image` and `video` within the daily cap.
- **A file announced before its post waits for it** (since 11 Nov; the platform). While ingest is paused, or when a collector announces the file first, its post (or comment) isn't stored yet. The post's text and captions belong with the analysis, and captions spare Whisper.
  - Such a job goes back to the queue without losing a try, before its file is fetched, and counts in `sb_analysis_media_total{outcome="waiting_for_post"}`.
  - Storing the post wakes it (ingest, at once). It also looks again every 6 hours, in case a wake was missed.
  - It goes ahead without its post `analysis.media.owner_wait` (168h: Kafka keeps 7 days) after it arrived, or `owner_margin` (12h) before its deadline, whichever comes first. `owner_wait: 0` turns the wait off.
  - So a TikTok file (72 h deadline) waits at most 60 hours for its post.
  - An identical file analysed before is reused only once its own post is stored, so the copy is dated by its own post.
- **Where media comes from:** only files a collector downloaded and announced on `content.media.ready.v1` (or fbs's `fb.media.ready.v1`). The media metadata on `content.media.v1` (URLs, sizes) is not a hand-off and isn't read.
  - An announcement without a bucket or key can't be fetched and isn't queued. It counts in `sb_analysis_media_total{outcome="no_location"}`, with `ingest: media announcements without a file location skipped` in the log.
  - fbs's own media (`fb.media.ready.v1`) is analysed and acknowledged on `fb.media.processed.v1`, but its results stay in Postgres until fbs moves to the contract (owner decision 17), so it isn't published on `analysis.media.v1`.
- **Transcripts:** a video of a contract post with captions uses the captions (the uploader's first, then the platform's speech-to-text; never a machine translation) and buys no speech-to-text. `transcript_source` says which: `platform_captions` or the speech-to-text model.
- **Memory and disk:** a video streams to a temporary file (`/tmp`, up to `analysis.media.max_bytes`, 500 MiB) and never passes through memory. An image is read into memory up to `image_max_bytes` (32 MiB) and `image_max_pixels` (40 megapixels, judged from its header before decoding); larger files fail permanently and are acknowledged. At start, `workers` is lowered to fit the container's memory limit (`worker_memory_mib` each, after 256 MiB), with a warning in the log. The chart gives `analysis-media` 2 GiB of memory and a 3 GiB `/tmp`.

## Speech-to-text

Videos are transcribed by a speech-to-text server inside the cluster: **Speaches** with Whisper large-v3-turbo, CPU only. The chart's `whisper` component runs it, and production turns it on (the owner, 7 November 2026). The audio never leaves the cluster.

- **Captions first:** a video whose post has the platform's captions uses them, and isn't sent to Whisper.
- **More servers** (`whisper.replicas`; a second one is the owner's choice of 11 Nov, D-052):
  - Each server has its own Deployment, 10Gi volume (it downloads the 1.6 GB model once, from huggingface.co) and Service. The first keeps the names it had alone, so adding servers leaves it running.
  - The chart gives the analysis every server's address (`analysis.providers.whisper.base_urls`), and sets `stt_concurrency` to the number of servers.
  - Each server gets one video at a time: a transcription waits for a free one. A shared Service balances connections, not requests, and connections are reused, so two videos could otherwise land on one server.
  - Each server requests 2 CPU and 4Gi (limits 3 CPU and 6Gi). Throughput grows with the servers: about 1,000–2,000 TikTok videos a day each, at roughly real time.
  - **A server that can't be reached** (down, or a new replica not ready yet) leaves the pool, with `analysis: speech-to-text server unreachable` in the log. It is tried every 10 s and rejoins as soon as it accepts connections (`... answers again, back in the pool`).
    - A video waits up to 1 minute for a reachable server. If none comes, it goes back to the queue for `stt_retry_after` without losing a try, as when Whisper is busy. This also covers a single server during a restart.
    - It counts in `sb_analysis_transcriptions_total{outcome="unavailable"}` and `sb_analysis_stt_deferred_total`.
    - Before 11 Nov (pull request #37 alone), a video sent to a server that wasn't Ready yet failed at once and lost a try.
  - **An error from a server of the pool names it** (`server sba-whisper-1: provider: status 500 ...`). An HTTP error is Speaches' own, so the server's log says why.
- **The model on each server's volume is checked at every start** (the init container, `files/whisper-model.py`). Each file must be present, of the Hub's size and with the Hub's checksum (SHA-256 for `model.bin`, the git blob hash for the others); without the Hub, present and not empty.
  - A faulty copy is set aside in `/data/huggingface/faulty-models/` (once; a later one is removed), and the revision is downloaded again.
  - From 7 to 11 Nov the first server's copy couldn't be loaded ("Cannot load the target vocabulary from the model directory"): every transcription it got answered 500. The old check skipped `vocabulary.*`.
- **A server counts as started only once it transcribes:** its startup probe (`files/whisper-ready.py`) sends a second of silence to `/v1/audio/transcriptions`, which loads the model. Until that succeeds, the pod isn't Ready, its Service has no endpoint, and the analysis passes over it. `/health` alone answers even when the model can't load. The probe runs every 20 s, for up to 15 minutes, then the container restarts.
- **Speech-to-text failing on a video's last try:** if the server still answers with an error (HTTP 5xx) on the video's last allowed try (`analysis.media.max_tries`), its frames are analysed without a transcript instead of the whole video failing (the master, 11 Nov). Its `transcript_source` reads `none`. Earlier tries fail and are tried again as usual.
- **Trying failed videos again:** `sb-analysis media-retry --reason transcribe [--platform tiktok] [--since 120h] [--min-left 6h]` counts the media files that failed with that text in their error, and how many still have `--min-left` before their deadline. Add `--apply` to put those back from their first try. It prints counts only.
- **A media job cut by a shutdown** (a deploy or restart) goes back to the queue at once without losing a try (since 11 Nov; before, it counted as a failure).
- **Limits:**
  - one transcription at a time per media process and server (`analysis.media.video.stt_concurrency`, raised by the chart to the number of servers);
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
- **Production: $60 a day, a hard stop** (the owner, 11 November 2026; D-052, raised from $10, D-031): `daily_usd: 60`, `urgent_exempt: false`.
  - **Caps per route:** `comment` $30, `post` $12, `image` $18, `video` $12. Images and video together stop at $30, so comments and posts keep at least $30 (about 230k items a day at the measured $0.13 per 1,000). Comments and posts together stop at $42, so media keeps at least $18. Room one side leaves goes to the other, up to its caps.
  - **At the cap:** every paid call waits until 00:00 Iraq time: real-time and batch items, images and videos, and the topic and entity curation.
  - **To see it:** `sb_analysis_budget_paused` is 1, and `AnalysisBudgetReached` fires.
  - **Models:** Claude Haiku for everything, with escalation and audit off. The own Whisper server is free, so it isn't limited.
  - `TestProductionConfig` fails if production's settings drift from that.
  - The pilot kept $5 a day.
- **Batches count at their estimate until they come back:** a batch request counts at its estimated cost until its batch returns (usually within an hour, at most 26 h), then at its real cost.
  - Until 11 Nov the estimate assumed the system prompt cached, which a batch can't count on. It ran about 6 times low for requests of 1–2 items, so the day passed the cap by what was in flight ($12.84 against $10).
  - Now a batch request's system prompt and schema (about 23 KB) are priced as plain input: about $0.0004 for a 1–2 item comment request on Haiku, a little above what is paid. So the cap is reached from below: paid work may pause a little early, and resumes as batches return at their real cost.
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

## fbs's cut-over to the contract

fbs moves from its own `fb.*` topics to the contract's `content.*` (REQ-018, from 13 Nov). It also backfills every stored Facebook post, comment and reply, with the same IDs (`fb:post:<post_id>`, `fb:comment:<comment_id>`).

- **How an item is matched:** by content ID, then by a hash of its kind, its text (whitespace collapsed) and its sorted media IDs. Same ID and same hash: unchanged, so nothing is queued and nothing is paid.
- **Adopted** (since 11 Nov, agreed by the master): the first contract version of an item already analysed from `fb.*` is not analysed again, whatever its hash. The two paths word posts (link titles) and name media slightly differently.
  - The item takes the contract's fields and envelope.version.
  - Its stored analysis is published on `analysis.result.v1` in the contract's format, with no AI call. Results of `fb.*` items stayed in Postgres until then (decision 17).
  - It counts as `sb_analysis_items_ingested_total{outcome="adopted"}`.
  - Not stored with the old analysis, so the published record lacks them: why the item took its lane (`routing` reads `normal`) and the tracked entities matched (`matched_entity_ids` is empty). The entities the model found are there.
  - The adopted item keeps its `fb.*` source and author keys in Postgres. The published record carries the contract's account and author IDs.
- **Not adopted:**
  - later versions (edits), which are analysed as usual;
  - items `fb.*` never got analysed (queued, skipped or failed), which are analysed and paid once;
  - an item marked analysed with no stored analysis, which is analysed as an edit.
- **Media:** a re-announced file whose SHA-256 was analysed before is reused at no cost, so fbs should send `sha256` on `content.media.ready.v1`.
- **The backfill:** adopted and unchanged items aren't queued, so the 200k ingest pause doesn't hold them. Each costs a few database writes: a pace of up to about 200 items a second is fine.

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
