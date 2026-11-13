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
- **Sending and collecting run apart** (since 11 Nov): the batch lane sends a round every `batch.submit_every` (10 min) and collects finished batches every `batch.poll_every` (1 min), in two loops of their own.
  - **Why:** collecting a batch of 1,000 results took about 6 minutes in production, and a poll checks up to 10 batches. With both in one loop, no batch was sent from 21:18 to past 22:30 Iraq time on 11 Nov, with nothing in the log.
  - **A poll** checks up to 10 open batches, one at a time (30 min lease, enough to collect it), and collects those finished. It takes the batches never checked first, then those checked longest ago, and when it ends it puts the ones still running back behind the others, so every open batch has its turn however many there are.
  - **Why the order** (since 12 Nov): a poll took the 10 oldest each time and gave back the running ones as they were, so the next poll took the same 10. On 11–12 Nov, with the 10 oldest still running at the provider, about 20 newer batches that had ended (18,000 answers) waited uncollected for hours, with nothing in the log: no check failed.
  - **A batch whose collection fails** is held back for 10 minutes, and the poll goes on to the others; the error names the batch (`analysis: batch poll … batch msgbatch_… (comment): …`). Before 12 Nov it was given back at once and, being the oldest, claimed again by the same poll's next check.
  - **A collection** stops after 25 minutes (a stuck download) and is retried.
  - **Each result** is stored within 2 minutes, or left for the batch's next try (`analysis: batch result not stored; left for the batch's next try`, the first three of a batch) while the batch's other results are stored; the batch ends once every result is stored. The writes stop waiting for a lock after 30 seconds (`lock_timeout`, SQLSTATE 55P03). Since 12 Nov: one collection waited half an hour before storing its first result, holding back its other results and every batch after it.
  - **Speed** (since 12 Nov): a batch's results are stored by `batch.collect_workers` (2) at once, each in its own transaction and connection (keep it below `postgres.max_conns`, 4 in production). Each result's writes go to the database in one round trip; only the reads that decide them (the item's row and state, a duplicate check, the author's bot flag) go alone. On 12 Nov, one statement a round trip, about 100 a result of 8 items, made 1,000 results take 22 minutes (1.3 s each); now about 25 round trips a result, two results at a time.
  - **Sending waits for collection:** no round is sent while more than `batch.max_open` (30) batches are open, with `analysis: batch round skipped: too many batches open, collection behind` (about 15–25 are open when collection keeps up). Nothing is paid for meanwhile; the items wait in the queue.
  - **Progress:** `analysis: batch collecting` when a batch's results are downloaded (with their number and the download's seconds), then every 250 results; `analysis: batch collected` at the end. A collection's error names its step (status, results download, a result, requests without a result).
  - **To watch:** `sb_analysis_batch_last_poll_timestamp_seconds` (the collector is alive) and `sb_analysis_batch_last_collect_timestamp_seconds` (a batch was last collected then). An alert on the second staying more than an hour old while `sb_analysis_open_batches` is above 0 catches a stuck collector; `sb-analysis batches --since 4h` shows which batches have ended at the provider.
  - **To see it:** every round logs `analysis: batch round` (route, items claimed, items after the top-up, requests sent, batches, seconds), or `analysis: batch round: nothing to claim`. Each collection logs `analysis: batch collected` with its route and seconds. A round held back by the provider's circuit logs `analysis: batch provider unavailable (circuit open)`, and every circuit change logs `analysis: provider circuit changed`.
  - **Collection keeps up:** at about 170 requests a minute it collects about 50,000 comments an hour at 4–5 a request, against about 25,000 an hour that the $30 comment cap allows on average. If `sb_analysis_open_batches` keeps growing, collection is falling behind. Items of a batch not collected within `batch.lease` (26 h) would be sent again.
- **Work a stopped round left behind** (a crash or restart between claim and send) comes back at the start of the next round, with `analysis: released batch work that was claimed but never sent` in the log:
  - requests recorded but never given a batch, older than `batch.submit_lease`, are marked failed, so their estimates stop counting against the day's budget;
  - items leased long but not in a sent batch are released.
  - Before 11 Nov a round leased its items for 26 h before sending, and claimed up to 100,000 items into batches of 5,000 requests (over 100 MB each). A crash then held them for 26 h, with their estimates counted against the budget.
- **A round shares its claim across platforms** (since 12 Nov): each platform waiting gets an equal share of `batch.max_items` of its oldest **live** items (priority normal or urgent), then whatever is left goes oldest first across all of them, live before backfill.
  - **Why:** a round took only the oldest items of all platforms. Instagram's items, arriving from 11 Nov, waited behind about a million older ones of TikTok, YouTube and Facebook: none analysed in a day.
  - **What it changes:** only the order within a round; the budget and the round's size are as before. A platform with less than its share leaves the rest to the others. Backfill items never take a live platform's share.
  - **The lookup:** `analysis_queue_platform_idx` (migration 00012) on (lane, route, the content ID's platform code, priority, enqueued_at), built concurrently. The queue has no platform column, so nothing needed a backfill.
  - **To see it:** `sb-analysis stats` gives each platform's waiting items: how many are in a request sent and not answered yet, how many are backfill (claimed only after every live item), how many published within `batch.recent_first` are not sent yet, and how long the oldest not yet sent has waited (`… 72600 waiting (40000 in flight, 0 backfill, 1200 published in the last 48 h not sent yet; the oldest not sent for 9.8 h) …`). Results of items in flight come when their batch ends (`sb-analysis batches` shows the provider's progress).
- **Recent items first** (CLIENT-REQ-018, the owner confirmed it on 13 Nov; `batch.recent_first`: 48 h, 0 = off): within each platform's share, live items published within the window are claimed before its older ones; what is left of the round takes the recent live items of all platforms before the oldest of all.
  - **The order:** priority first, as before (backfill never comes before a live item, however recent). Among recent items, the oldest queued go first. An item stops counting as recent once it is older than the window.
  - **The backlog still drains:** older items, oldest first, fill every round whenever fewer recent items wait than it claims.
  - **The lookup:** an item can't be queued before it was published, so the recent ones are read from `analysis_queue_platform_idx` by queue time since the window's start: only the entries queued in the last 48 h, not the backlog. No new index.
  - **To see it:** the stats line's `published in the last 48 h not sent yet` (shown while it is on) should stay small next to the waiting total while the backlog drains.
- **A round fills its threads** (since 11 Nov; the platform agreed): after claiming the oldest `batch.max_items` items, a round also claims each claimed thread's other waiting comments, up to `grouping.max_items` (40) a thread and `batch.top_up_items` (10,000) in all.
  - **Why:** a claim of the oldest items held one or two comments of most threads, so most requests carried one or two comments. The cached prompt and schema (most of a comment's cost) were read once for each.
  - **What it changes:** only how full requests are. The prompt, the grouping by thread and its limits are as before. Comments retried after a fault of their own answer aren't taken; they still go alone.
- **A round fills its pages too** (since 11 Nov; the master agreed): short posts (up to `grouping.post_short_chars`, 280) go on the comment route, grouped by page. After the threads, a round also claims each claimed page's other waiting short posts, up to `grouping.max_items` a page, within the same `batch.top_up_items`. Long posts (the post route) pack together whatever their page, so they aren't topped up.
  - **How it finds them:** a short post's queue entry carries its page (`analysis_queue.source_id`, migration 00011, with `analysis_queue_source_idx` built concurrently).
  - **The backfill:** entries queued before 00011 get their page from a backfill in the `rollup` role. It reads 10,000 comment-route entries a pass in content-ID order, with a 2-second pause between passes, and keeps its place in `analysis.backfills`, so a restart goes on where it stopped. Once done it does nothing more. Until then, the posts it hasn't reached are claimed as before, without a top-up.
  - **To see it:** `sb-analysis stats` prints `page backfill (…): running: N queue entries read, M given their page`, then `done: …`. The log says `analysis: page backfill done`.
- **A full group's answer fits:** the comment and post routes' `max_tokens` are 16,000 (since 11 Nov; before, 6,000 and 2,000). A cut-off answer fails every item in it, and they then go alone. Only the tokens written are paid, but a cut-off answer is paid in full for nothing.
  - **A request carries no more items than its answer fits** (since 11 Nov): at most 60% of the route's `max_tokens` at the route's measured output tokens per item (the measure the estimate uses), within `grouping.max_items` and `max_chars`.
  - **Why:** long posts went 13–14 a request (`max_chars` 12,000 was the only limit) and need over 1,100 output tokens each, so 9,344 were cut off at 16,000 on 11 Nov. Comments, at about 90 output tokens each, are held by `max_items` (40) as before; long posts now go about 8 a request.
- **Who goes alone** (since 11 Nov): an item is sent in a request of its own only when it was tried before and its last failure was about its own answer: invalid or truncated output, missing from the answer, or refused. An item back for another reason (never sent, aborted by a shutdown, errored or expired in its batch, no result) groups with its thread as usual.
  - Until then every item tried before went alone. The oldest backlog, almost all tried before (the old 26 h leases, restarts, errored batches), went out one item a request: 61,530 requests for 61,530 items in 6 h on 11 Nov.
  - **To watch:** `sb_analysis_items_topped_up_total{route}`, the `analysis: batch round topped up its threads and pages` log line (comments and posts added, and how many threads and pages), and in `sb-analysis stats` the items per request and the cost per 1,000 items. Also `sb_analysis_output_repairs_total` (`duplicate_item`, `unexpected_item`), in case fuller requests answer less cleanly.
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
  - the queue now, per route, by tries counted (none, one, two, three or more), how many last failed on their own answer (they go alone), and why entries came back (top 8 reasons). It scans the queue;
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

**The queue's cap (backpressure):** at `analysis.backpressure.max_queue` items (200,000; production 1.5M, resuming at 1.4M), counted over the whole queue (every route and lane: `sum(sb_analysis_queue_depth)`), ingest stops taking new comments: `sb_analysis_ingest_paused` is 1, and they wait in Kafka.
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
  - the contract's media: `analysis.media.contract_kinds` lists the type/role pairs analysed (default: images and videos that are the content or an attachment, cover images, profile pictures and banners, and YouTube's video thumbnails). A pair can name one platform: `youtube:image/thumbnail`.
  - **Profile pictures and banners** (the owner's D-061, 13 Nov) of monitored pages, accounts and channels only (`analysis.media.monitored_roles`): an account is monitored here when it is the source of a stored item (its posts are collected; a commenter never is). Others are acknowledged `SKIPPED` with `policy:not_monitored`. Their request says what the picture is, and to describe it, read its text and logos, and never say who a person is or guess personal traits. They feed the client's page labels; a lawyer's review comes before any client sees them (the owner, 13 Nov).
  - **Thumbnails:** YouTube's only (its videos aren't handed over, so the thumbnail is the video's only picture), at `thumbnail_max_side` (512 px); other platforms' are skipped, as their videos are analysed themselves.
  - A file whose owner is an account (its picture or banner), audio or playlist doesn't wait for its owner: only posts and comments are stored.
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
  - **A comment outside the comment sample** (`analysis.comments.sample_share`) is counted, never stored, so its files don't wait for it (since 13 Nov): they are analysed at once, without its text. At start the media process makes the comments' waiting files due again (`analysis: comments' media woken`).
- **Which files first** (since 13 Nov, the owner's D-060): with `media.recent_first` (production 6h; off by default), files announced in that window are claimed first, newest first, and the rest after them, nearest deadline first. A quarter of the claims (`deadline_share` 0.25) go first to the files in their last 12 hours (`deadline_within`), so those aren't starved while new files arrive faster than the workers take them. With it off, every file goes nearest deadline first. Before, with more files than the budget allows, every file analysed was a day or more old. While the image cap is spent the order makes no difference: nothing is claimed.
- **Videos and speech-to-text** (since 13 Nov): while every speech-to-text slot is busy (`stt_concurrency`, the number of whisper servers), the workers claim images only, and videos are claimed again once a slot is free. Before, with videos first by deadline, the other workers kept claiming videos, waiting 5 s for the slot and putting them back, and images barely got through (about 23,000 files a day analysed on 12–13 Nov).
- **A video near its deadline** (`media.video.frames_only_within`, 6 h; since 13 Nov): while the slots are busy it is still claimed, and if no slot (or no whisper server) is free it is analysed from its frames alone, with `transcript_source` "none", rather than expire. `sb_analysis_transcriptions_total{outcome="frames_only"}` counts them; `sb-analysis stats` shows each platform's videos by captions, speech-to-text and frames only.
- **Throughput:** about 5 s an image per worker; a video without captions takes about its own length on a whisper server, and holds its worker all that time. So while TikTok has videos without captions waiting (always, in November 2026), the whisper servers' slots keep that many workers busy, and only the rest take images: on 13 Nov, 2 of 4, about 1,400 images an hour. Production runs 8 workers since 13 Nov (2 for speech-to-text, 6 for images, in 3,584 MiB and a 5 GiB `/tmp`, requesting 500m CPU: 4 workers used 0.23 cores; `TestProductionMediaLimitFitsWorkers` checks they fit). For more, raise `workers` with the memory limit (384 MiB each after 256 MiB) rather than add a replica: a second replica gets as many speech-to-text slots again, so two videos would share each whisper server and each would take twice as long. All workers share the process's 4 database connections, which they hold only for short reads and writes.
- **In `sb-analysis stats`:** the media line's "now" counts are of the files announced in the window (which may wait behind older files with nearer deadlines); "acknowledged in the window, whenever announced" counts the acknowledgements sent in it. A video's speech-to-text is logged as a request of its own (done, no tokens), and isn't counted as a re-send. "Pending now by deadline" ends with the files waiting for their post or comment, counted as the worker decides it (a post or comment not stored yet, a comment in the sample, still within `owner_wait` and `owner_margin`), and the files whose owner isn't stored either but which go ahead without it: comments outside the sample (never stored), pages' and accounts' pictures, and files whose wait has ended. Before 14 Nov both were counted as waiting (on 13 Nov TikTok's 8,975 and Facebook's 5,205).
- **Where media comes from:** only files a collector downloaded and announced on `content.media.ready.v1` (or fbs's `fb.media.ready.v1`). The media metadata on `content.media.v1` (URLs, sizes) is not a hand-off and isn't read.
  - An announcement without a bucket or key can't be fetched and isn't queued. It counts in `sb_analysis_media_total{outcome="no_location"}`, with `ingest: media announcements without a file location skipped` in the log.
  - fbs's own media (`fb.media.ready.v1`) is analysed and acknowledged on `fb.media.processed.v1`, but its results stay in Postgres until fbs moves to the contract (owner decision 17), so it isn't published on `analysis.media.v1`.
- **Transcripts:** a video of a contract post with captions uses the captions (the uploader's first, then the platform's speech-to-text; never a machine translation) and buys no speech-to-text. `transcript_source` says which: `platform_captions` or the speech-to-text model.
- **Images in a format not read yet** (since 13 Nov): JPEG, PNG, GIF and WebP are read. An image that none of them reads fails with its format named from its first bytes (`image: unknown format (heif-heic)`, `(avif)`, `(bmp)`, `(tiff)`, `(jxl)`; otherwise the content type, such as `(text/html; charset=utf-8)` or `(video ftyp isom)` for a video sent as an image). A known image format (HEIC/HEIF, AVIF, JPEG XL, BMP, TIFF) goes back for 6 hours without losing a try while its deadline is more than 18 hours away, so it can be analysed once a converter reads it, then fails for good as before; anything else fails at once. Watch `sb_analysis_media_total{outcome="unknown_format"}` and the log line `analysis: media in a format not read yet; kept for later` (with `platform`). On 13 Nov, 134 of 135 permanent failures in two hours were an unknown format.
- **Memory and disk:** a video streams to a temporary file (`/tmp`, up to `analysis.media.max_bytes`, 500 MiB) and never passes through memory. An image is read into memory up to `image_max_bytes` (32 MiB) and `image_max_pixels` (40 megapixels, judged from its header before decoding); larger files fail permanently and are acknowledged. At start, `workers` is lowered to fit the container's memory limit (`worker_memory_mib` each, after 256 MiB), with a warning in the log. The chart gives `analysis-media` 2 GiB of memory and a 3 GiB `/tmp` (production: 3,584 MiB and 5 GiB, for 8 workers).

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
  - `AnalysisTranscriptionsFailing`: at least 3 speech-to-text calls failed in 30 minutes, more than 10% of them (since 13 Nov; any single error fired it before, often of a video that then succeeded).
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
- **Batches count at their estimate until they come back** (usually within an hour, at most 26 h), then at their real cost. No discount: the estimate itself is made accurate, so the cap stays a ceiling (the master, 11 Nov).
  - **The estimate** (`provider.Pricing.Estimate`, `pipeline.estimate`) prices a request's prompt (system prompt and schema, marked for caching) at the share its route's answered requests read from the cache over the last day, and the rest as cache writes; its item text as input; its output at the route's measured tokens per item over the last day (`store.RouteMeasures`, read every 10 minutes, from at least 100 answered requests).
  - **Without a measure** (a new route, or under 100 answered in the last day): output at 150 tokens an item (`pipeline.DefaultOutPerItem`) and the prompt as a cache miss, the dearest case.
  - **Sizes:** the prompt runs about 1.75 bytes a token (11 Nov: 23,237 bytes read from the cache as 13,238 tokens), item text is counted at 2 bytes a token (mostly Arabic, on the high side). The old estimate assumed 3, so it read the prompt as about 7,700 tokens.
  - **Example, 11 Nov:** a one-comment batch request on Haiku cost $0.000113 (13.2k tokens read from the cache, about 205 input, 148 output). Estimated with the cache read: about $0.000115. As a miss: about $0.000875.
  - **The worst case** of every request, its prompt written to the cache, is stored with it (`provider_requests.est_max_usd`, migration 00010; for requests from before it, the estimate).
  - **Why:** on 11 Nov a one-item comment request was estimated at $0.00041 and cost $0.000113. 26,000 in flight counted for about $10.7 of the comment route's $30 while they would cost about $3, and the route stopped sending for hours. Before that, assuming the prompt always cached (at 3 bytes a token), the estimate ran about 6 times low for 1–2 item requests, and the day passed the cap by what was in flight ($12.84 against $10).
  - **A round over the cap** gives the group that didn't fit and every group not built yet back in one release (`analysis: batch round held by the budget`, `sb_analysis_budget_held_items_total{route}`). A route whose day is already spent doesn't claim at all (`analysis: batch round skipped: the budget is spent for today`).
  - **To see it:** `sb-analysis stats` prints today's spend per route as the budget counts it, against the caps: answered, the requests in flight at their estimate and at their worst case, and the **worst case** of the route and the day (real cost plus every request in flight missing the cache: the most the day can come to with output as measured). It also prints what new requests are priced at: output per item and the share of the prompt read from the cache, or the defaults.
  - **Output above the measure isn't bounded:** an answer can run to `max_tokens` (16,000). The measure follows it within the day.
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
  - items `fb.*` never got analysed (queued, skipped or failed), which are analysed and paid once. One still waiting whose contract copy is unchanged takes the contract's version (since 11 Nov), so its one analysis is published on `analysis.result.v1`;
  - an item marked analysed with no stored analysis, which is analysed as an edit.
- **Read one path, not both** (since 11 Nov): once fbs publishes its posts and comments on `content.*` (`contract.enabled`), set `analysis.inputs` from `[content, fb]` to `[content, fb-media]` in the same change.
  - **Done in production** on 11 Nov at 21:45 Iraq time (the master, #110, with fbs `3af55b0`); `values-production.yaml` reads `[content, fb-media]` since. `TestProductionConfig` fails if it drifts back.
  - **To check it:** the `sb-analysis starting` log line lists the inputs; in the consumer group `sb-analysis`, `fb.post.v1` and `fb.comment.v1` have no consumer (their lag only grows), while `fb.media.ready.v1`, `fb.erasure.v1` and `content.*` keep theirs.
  - `fb-media` reads only fbs's `fb.media.ready.v1` (acknowledged on `fb.media.processed.v1`) and `fb.erasure.v1`, not `fb.post.v1` or `fb.comment.v1`.
  - **Why:** an item read on both paths with a different hash (link titles, media names) is an edit each time either path re-scrapes it, and so is analysed again and again.
  - What's left unread on `fb.post.v1` and `fb.comment.v1` arrives through fbs's backfill on `content.*` with the same IDs. The comment sample is by ID, so the same 10% is kept.
  - If fbs also announces media on `content.media.ready.v1`, one path must go: then `fb-media` would read each file twice.
- **Media:** a re-announced file whose SHA-256 was analysed before is reused at no cost, so fbs should send `sha256` on `content.media.ready.v1`.
- **Older readings are ignored** (since 11 Nov, agreed with fbs): fbs's `envelope.version` is the reading's time (`observed_at` in microseconds) and only rises per item. A contract copy at or below the stored version is the same reading again or an older one: nothing is written, queued, paid or counted in the sample (`sb_analysis_items_ingested_total{outcome="stale"}`).
  - **Why:** a pilot copy (read 1–7 Nov) of a comment production also read (from 10 Nov) would otherwise be an edit: paid for again, its older text replacing the newer. A redelivered older reading arriving after a newer one is the same case.
  - **Not compared:** items stored from fbs's own topics have no version (0), so their first contract copy is adopted or unchanged as before. **Run the production backfill before the pilot's:** it gives those items their production version first, so the pilot's older copies of them are then ignored. The other way round, an analysed one would be adopted with the pilot's older text.
  - Deletions and erasures apply whatever their version.
  - **A newer version with the same text** (fbs adding a video's captions, D-056) is unchanged: captions aren't part of the input hash, so nothing is analysed or paid. The captions are stored, and so is the newer version (since 12 Nov), so an older reading after it is stale.
  - **Captions feed the video's transcript only if they're stored before its analysis.** A video analysed before its captions arrive keeps its Whisper transcript (the own server, free) and isn't analysed again.
- **The backfill:** adopted and unchanged items aren't queued, so the ingest pause doesn't hold them. Each costs a few database writes: a pace of up to about 200 items a second is fine.
  - **Comments under the sample:** a comment already stored (every `fb.*` comment from before D-053) is kept, and adopted if analysed. One never seen is kept only if it is in the 10% sample. Backfilled comments count in `comment_sample_daily` on the day they arrive, so that day's received and sampled counts include them.
  - **Live first** (since 11 Nov, the master): a post or comment whose `provenance.mode` is `CRAWL_MODE_BACKFILL` takes the batch lane at background priority (2), even from a hot account or naming a tracked entity. A claim takes every live item waiting (priority 1, or 0 for urgent ones) before it, however long it has waited.
    - A live copy of the same item lowers its priority to the live one's (the queue keeps the lower of the two).
    - A round's thread and page top-up still takes the backfill items of the threads and pages it claimed, so they ride along in live requests. That costs less than sending them alone later, but it is spend from the same daily caps.
    - With a large live queue, backfill items wait until it is drained, within the day's caps.

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
