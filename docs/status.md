# sb-analysis: status

*Kept current for the master session and the owner. Last updated 12 November 2026, 03:05 (Iraq time).*

## Where it stands

| Area | State |
|---|---|
| Code | Built and tested: unit and integration tests with race detection against real Postgres, Redpanda and Valkey. In `main` of the public repository `Abdullah95k/securebrand-microservices-analysis`. **A repository of its own since 6 Nov** (REQ-011 fix 1, `b080a72`): fbs's code is removed, and fbs's module is a dependency for the fb protos only. |
| Running | **Started in production (7 Nov, 06:39 Iraq time)** at `5df7e33`: the migrate hook passed and svc-analysis synced. It read `[content, fb]` (`[content, fb-media]` since 11 Nov, 21:45 Iraq time), with a $10 daily hard stop and Claude Haiku. Running at `fdf8e73` since 10:47 Iraq time, 7 Nov (less database use while idle). The whisper server is up: the model download finished and it is Ready. PostgreSQL now allows 200 connections (was 50), since 14:32 Iraq time. No new runs on the shared server (owner's D-026). |
| Pilot server | **No new runs there** (owner's D-026, 6 Nov). The Compose deployment next to the fbs pilot (`deploy/analysis/compose.yml`) stays in the repository; it was never started. |
| Quality | **Not measured, and on hold.** The owner (7 Nov): "just Haiku for now", so the one-off check with Opus labels (about $3–5) doesn't run. Haiku can still be measured at no AI cost if a person labels the sample (`docs/analysis/evaluation.md`, section 2). |
| Contract | **Contract v1.2.0** (owner, 7 Nov; REQ-022 done on v1.0.0, `fe0dc52`). sb-analysis reads the contract's `content.post.v1`, `content.comment.v1`, `content.media.ready.v1` and `content.media.deleted.v1`, and publishes the contract's `analysis.*` and `content.media.processed.v1` (as `sb-analysis`). In CI, every kind it produces passes the contract's own conformance checks (production mode). It also reads fbs's `fb.*`, whose results stay in Postgres. Record: [`analysis-contract-adoption.md`](master/analysis-contract-adoption.md). |
| Schema Registry | sb-analysis registers nothing (standing instruction: no `securebrand.content.*` until the master publishes v1). **But the pilot's shared registry already holds 9 subjects from this repository's copy** (confirmed 6 Nov, 00:58 Iraq time): the 4 `content.*` subjects, `securebrand/content/v1/content.proto`, and the 4 `analysis.*` subjects, almost certainly from a `make pilot-up` run from this copy around 1 November. The master (6 Nov): they don't block the platform, since staging and production get their own registry. **Leave them in place; delete nothing.** |

## Work in progress (from 6 Nov)

- **REQ-011, the fixes:** one row per fix in [`docs/master/analysis-fixes.md`](master/analysis-fixes.md). Fixes 1 to 6 (all those before the canary) and 8 (the real-time lane) are merged, and so is fix 9's plan ([`analysis-backfill-plan.md`](master/analysis-backfill-plan.md)). Fix 7 (the quality evaluation) waits for fbs to collect in production.
- **REQ-022, adopting contract v1.0.0: done** (6 Nov, `fe0dc52`; pull requests #11, #12, #13). Switching production on waits for the owner: see "Switching on" in [the record](master/analysis-contract-adoption.md). Record: [`docs/master/analysis-contract-adoption.md`](master/analysis-contract-adoption.md).
- **Questions for the master:** [`docs/master/questions-for-master-2026-11-06.md`](master/questions-for-master-2026-11-06.md).

## Reports for the master (5 Nov)

| Part | File |
|---|---|
| A: the content contract and the input fields each analysis needs | [`docs/master/analysis-contract-notes.md`](master/analysis-contract-notes.md) |
| B: every output field, versioning, and a ClickHouse layout | [`docs/master/analysis-outputs.md`](master/analysis-outputs.md), [`analysis-outputs.yaml`](master/analysis-outputs.yaml) |
| C and D: operations, models, costs, media, embeddings; the repository and its fbs copy | [`docs/master/analysis-operations.md`](master/analysis-operations.md) |
| E: gaps against the production standard; the owner's decisions | [`docs/master/analysis-gaps.md`](master/analysis-gaps.md) |

## Standing instructions (from the owner, 5–6 Nov)

- Don't rename or remove topics, event fields, schemas or endpoints others use. Additions are fine, and are reported to the owner.
- Don't register `securebrand.content.*` or `securebrand.common.*` in any shared Schema Registry. In production the platform registers every subject and creates every topic.
- Don't create or change topics on a Kafka shared with another service.
- Nothing on the shared server (201.79.36.209) without the owner's approval.
- What goes to an AI provider doesn't change: phone numbers, e-mails and author names never leave (D-010).
- Erasure stays as it is (it records, deletes nothing); don't remove it, and don't apply it without the owner's written order.
- Every analysis result is kept, every version.
- Keep this document current.

## Open decisions (owner or master)

1. **fbs's move to the contract:** a cut-over at one time T, designed when fbs's version is near (master, 7 Nov). No adapters for tks, igs or yts.
2. **sb-analysis's own text copy:** keep it, or slim it to working state once the platform's entity layer and the client app exist (the owner's decision 7, "keep both for now"; REQ-011, later).
3. **How many comments to analyse (owner, 11 Nov):** about 2.7M comments arrive a day, steadily (the master's Kafka readings), against about 270k items a day analysed at today's caps. At the measured $0.13 per 1,000, every comment would cost about $351 a day.
   - Comments not analysed aren't lost: their raw text stays in ClickHouse (tt, yt, ig) and fbs's Postgres (fb), for a later re-analysis.
   - The options and their costs are under "11 Nov, 17:45 Iraq time" below.

## Changes since the last update

- **2 Nov:** the analysis runs as its own Compose project next to the fbs pilot; it creates only its own topics (`sb-analysis kafka-topics`).
- **5 Nov:** the master reports above. No code, configuration or server changes.
- **6 Nov:** the owner's read-only check confirmed the 9 schema subjects above on the pilot. Nothing was changed.
- **6 Nov:** answered the master's REQ-006 questions at the end of `docs/master/analysis-contract-notes.md`.
- **6 Nov, owner-approved changes (additions only; no topic, field or schema renamed or removed):**
  - tagged people's names are hidden from AI providers (`analysis.privacy.mask_tagged_users`, default on);
  - Facebook's UTF-16 tag offsets are converted exactly (a bug fix);
  - erasure events delete nothing by default (`analysis.erasure.mode: record`), with new commands `erasures list` and `erasures apply`;
  - migration `00003` adds `content_items.tag_spans` and two columns of `erasures`.
- **6 Nov:** `main` created and the draft pull request #1 opened (owner's approval).
- **6 Nov:** the master confirmed the REQ-006 and REQ-015 answers. Its direction for contract draft v0.2:
  - `AccountRef account` on `Post`;
  - the consumer name `sb-analysis` (both sides keep `analysis` until adoption);
  - media retries at most 3, with `attempt`;
  - `delete_after` at least 24 hours;
  - a `reason` field on `MediaProcessed`;
  - `securebrand.analysis.v1` moves into the contracts repository (to review when v0.2 is up).

  Adoption comes in its own request after v1.0.0.
- **6 Nov:** review of the contract draft v0.1 (REQ-015) in [`docs/master/analysis-contract-review.md`](master/analysis-contract-review.md). Review only; nothing built.
- **6 Nov:** fixed the failing chart test ("deploy" check) in pull request #1. It was a test-only problem: the small test broker holds 256 partitions; the topic setup fills them, and with this branch's 8 extra topics no room was left for Kafka's own consumer-offsets topic, so nothing could read. The test script (`scripts/chart-smoke.py`) now has the broker create that topic first. No topic, field, schema or endpoint changed.
- **6 Nov, 04:00:** pull request #1 merged into `main` after all three CI checks passed (test, integration, deploy), under the owner's rule that a pull request may be merged once CI is green. Nothing was deployed or started anywhere.
- **6 Nov:** pull request #2 merged (this document's update).
- **6 Nov, the owner's requests:** REQ-011 (the fixes; approved to build) and REQ-022 (adopt contract v1.0.0, published as tag `v1.0.0` in `Abdullah95k/securebrand-contracts`; after the pre-canary fixes). The owner's answers:
  - the quality evaluation runs on the production cluster, once fbs collects there;
  - the canary's AI budget is $5 a day.
- **6 Nov, REQ-011 fix 1 (in review):** a repository of its own. fbs's code is removed, and sb-analysis has its own config, topics, test helpers, Makefile, Dockerfile, CI and local test stack.
  - **Changed names:** environment variables `SB_ANALYSIS_*` (were `FBS_*`), metrics `sb_analysis_*` (were `fbs_analysis_*`). Nothing depended on the old names yet.
  - **Bug fix:** the migrations' version table is now schema-qualified.
  - **Unchanged:** no topic, field or schema was renamed or removed. Details in [`docs/master/analysis-fixes.md`](master/analysis-fixes.md).
- **6 Nov:** REQ-011 fix 1 merged (pull request #3, `b080a72`).
- **6 Nov, the master:** the platform builds production images itself; namespace `analysis`; secrets `platform-credentials` and `ai-provider`; Kafka user `analysis-kafka` (`content.*`, `analysis.*`); database `analysis`; $5 a day.
- **6 Nov, REQ-011 fix 2 (in review):** its own Helm chart with `values-production.yaml`, images on a tag, chart checks in CI, and the alert rules moved from fbs's chart.
  - **Both workloads are at 0 replicas in production until REQ-022:** the platform's topics carry the contract's schemas, not sb-analysis's draft ones.
  - **New, nothing removed:** the database settings as parts (host, port, and so on) alongside the DSN.
- **6 Nov, the master's answers:**
  - **conventions:** the secret's keys match the chart's mapping; the CA certificates are mounted as files;
  - **account tiers:** from `registry.account_request.v1`, with a config list until it publishes;
  - **backfill:** from ClickHouse (`clickhouse-ro`) only;
  - **production images:** built by the platform (`registry.digitalocean.com/securebrand/analysis`).
- **6 Nov:** REQ-011 fix 2 merged (pull request #4, `22f8938`); the master has the commit, the two images and the chart for its production pin.
- **6 Nov, REQ-011 fix 3 (in review):** outside `dev`, a daily cap of 0 (no cap) is refused unless `analysis.budget.allow_unlimited: true` is set. This is a new setting; nothing was removed.
- **6 Nov:** REQ-011 fix 3 merged (pull request #5, `3f18a15`).
- **6 Nov, REQ-011 fix 4 (in review):** media files stream to disk; images get their own limits (bytes, and pixels checked before decoding); the number of media workers fits the container's memory. All new settings; nothing removed.
- **6 Nov:** REQ-011 fix 4 merged (pull request #6, `34545d9`).
- **6 Nov, REQ-011 fix 5 (in review):** each AI provider's rate limit is shared by every pod through Valkey, with a per-pod fallback while Valkey is unreachable. All new settings; nothing removed.
- **6 Nov:** REQ-011 fix 5 merged (pull request #7, `cfc8125`).
- **6 Nov, REQ-011 fix 6 (in review):** at 200,000 queued items ingest pauses new content, which waits in Kafka, and resumes at 150,000. Media announcements and erasures keep flowing. All new settings; nothing removed.
- **6 Nov:** REQ-011 fix 6 merged (pull request #8, `1006f37`); the master has the commit to pin.
- **6 Nov, REQ-011 fix 8 (in review):** items from hot-tier accounts go to the real-time lane, along with mentions of tracked entities; everything else goes batch. The hot tier is a config list plus the clients' watchlists until the platform's registry publishes tiers. All new settings; nothing removed.
- **6 Nov:** REQ-011 fix 8 merged (pull request #9, `8cc528d`). The master re-pinned production to fix 6's commit; the platform's ClickHouse keeps one table per message type plus `_latest` views, in database `securebrand`.
- **6 Nov, REQ-011 fix 9 (plan, in review):** a backfill reads older items from ClickHouse through `clickhouse-ro`. It goes through the same adapter as Kafka, stays in the batch lane within the daily budget, can resume after a stop, and shows a dry-run estimate first. Built after REQ-022, with the owner's approval.
- **6 Nov:** REQ-011 fix 9's plan merged (pull request #10, `4002dfd`).
- **6 Nov, REQ-022 step 1 (in review):** the contract in and out.
  - **Reads** `content.post.v1` and `content.comment.v1` when `analysis.inputs` has `content` (it meant the never-published draft before).
  - **Publishes** the contract's `securebrand.analysis.v1` with the headers `sb-type` and `sb-contract`; every result passes the contract's conformance check in CI.
  - **Deleted, as REQ-022 asked:** the draft `securebrand.content.v1`, our copy of `securebrand.analysis.v1`, and the draft topics `content.item.v1` and `content.erasure.v1` from the local test topic list. Nobody produced or consumed them.
  - **Changed:** results of fbs's own `fb.*` items stay in Postgres (they have no contract IDs).
  - **Added:** migration `00004`, three columns on `analysis.content_items`. Nothing removed.
- **6 Nov:** REQ-022 step 1 merged (pull request #11, `cbf46fd`). The master has the commit; production stays at 0 replicas.
- **6 Nov, REQ-022 step 2 (in review):** the contract's media hand-off.
  - **Reads** `content.media.ready.v1` and `content.media.deleted.v1`.
  - **Acknowledges** on `content.media.processed.v1` as `sb-analysis`, with a unique `event_id`, `SKIPPED` for policy skips, and a short `reason` on skips and failures.
  - **Policy:** a new setting, `analysis.media.contract_kinds`, says which (type, role) pairs are analysed.
  - **Reuse:** a file identical to one already analysed (same SHA-256) reuses that analysis.
  - **Added:** migration `00005`, five columns and an index. fbs's acknowledgements are unchanged.
- **6 Nov:** REQ-022 step 2 merged (pull request #12, `a0d6a6f`).
- **6 Nov, REQ-022 step 3 (in review):**
  - **`authors` commands:** `sb-analysis authors list`, `mark` and `log` show and take authors' account IDs.
  - **Captions:** a contract post's captions become its video's transcript, so no speech-to-text is bought. `MediaAnalysis` says where the transcript came from (`transcript_source`).
  - **Added:** migration `00006`, three columns on `analysis.content_items`. Nothing removed.
- **6 Nov, REQ-022 step 4 (in review, with step 3):**
  - **The test:** a fake platform publishes posts (one with an estimated time), comments, a partial deletion and media, including a file deleted before analysis.
  - **What it checks:** every kind sb-analysis produces comes off Kafka with the contract's headers and passes the contract's checks: `ItemAnalysis`, `MediaAnalysis`, `AuthorFlag`, `TopicUpdate`, `MediaProcessed`.
  - **Small fixes:** a media transcript always carries its language and source; documents keep their type.
- **6 Nov:** REQ-022 steps 3 and 4 merged (pull request #13, `fe0dc52`). **REQ-022 is done.** The master has the commit; production stays at 0 replicas until the owner decides.
- **6 Nov:** status update merged (pull request #14, `8dacd75`).
- **7 Nov, the owner's decisions:**
  - production reads every collector: the contract's `content.*` (fbs, tks, yts, igs as each adopts it) and fbs's `fb.*`;
  - contract v1.2.0;
  - no daily cap, Claude Haiku only.
- **7 Nov, applying them (in review):**
  - contract v1.2.0 pinned, and CI's end-to-end test now uses the contract's own checks for all five kinds;
  - `values-production.yaml`: both workloads on (1 replica each), `inputs: [content, fb]`, no cap (`allow_unlimited: true`), escalation and audit off, curation on Haiku;
  - new `TestProductionConfig` guards those settings, and CI checks that production renders both workloads.
  - Nothing removed; no topic, field or schema changed. The headers now say `sb-contract: 1.2.0`.
- **7 Nov:** the decisions merged (pull request #15, `b3d94d4`). The master pinned it and gave `analysis-kafka` write access to `analysis.*`. The master confirmed tks and igs publish the contract and yts will, and that fbs moves later, with a cut-over.
- **7 Nov, speech in videos (in review):** the owner wants speech transcribed, with an out-of-the-box Whisper server; the plan was confirmed with the master.
  - **The server:** the chart gains an optional `whisper` component, on in production. It runs Speaches with Whisper large-v3-turbo on CPU:
    - the image pinned by digest, the model by revision, downloaded once to a 10 GiB volume;
    - `Recreate` rollouts, internal Service only.
  - **In sb-analysis:** captions first; Whisper takes one video at a time, the first 10 minutes at most. A video that finds it busy goes back to the queue.
  - **New:** metrics (`media_pending`, `transcriptions_total`, `stt_deferred_total`), two alerts, and settings (`stt_concurrency`, `stt_wait`, `stt_retry_after`). Nothing removed.
- **7 Nov:** speech in videos merged (pull request #16, `c37a753`); the master pinned it.
- **7 Nov, the owner confirmed a $10 daily cap as a hard stop (D-031; in review):**
  - `values-production.yaml`: `daily_usd: 10`, `urgent_exempt: false`;
  - the topic and entity curation now also check the budget before their paid call;
  - a new alert, `AnalysisBudgetReached`.
- **7 Nov:** the $10 daily hard stop merged (pull request #17, `602fd60`); the master pinned it, with the chart's alerts and ServiceMonitor switched on in the platform's values.
- **7 Nov, the migrate hook failed on the live cluster** (reported by the master; its log is on the way). With the owner's approval, the hook now opens only Postgres: before, it also read the Kafka and Valkey settings and certificates and built the whole app, so a problem there could block the database step. In review.
- **7 Nov:** the migrate fix merged (pull request #18, `e911125`). The master found the hook's real cause on the platform side: `POSTGRES_CA_CERT` was empty. PostgreSQL's certificate is from Let's Encrypt; the platform now fills the CA with Let's Encrypt's roots and, on this session's advice, sets `sslmode=verify-full` for every service, so the host name is checked too.
- **7 Nov, PostgreSQL behind PgBouncer (in review):** production's PostgreSQL is reached through DigitalOcean's PgBouncer in transaction mode, with 50 connections shared by every service.
  - **Before:** reproduced locally through a PgBouncer set up the same way, sb-analysis failed with `prepared statement … does not exist / already exists`.
  - **Now:**
    - it describes statements instead of keeping them prepared (`cache_describe`), and closes idle connections after 30 s;
    - production opens at most 4 connections per process;
    - the whole suite runs through PgBouncer in CI and with `make test-pgbouncer`.
- **7 Nov:** the PgBouncer fit merged (pull request #19, `5df7e33`). CI ran the suite through PgBouncer too.
- **7 Nov, 06:39 Iraq time: sb-analysis started in production.** The migrate hook passed at `5df7e33`.
  - The master fixed the `analysis-media` image pull (401): DigitalOcean's pull secret sits only on the namespace's default ServiceAccount.
  - **In review:**
    - `values-production.yaml` now names that pull secret too;
    - a full database at start (53300, when several services start at once) is retried for about two minutes instead of restarting the pod.
- **7 Nov, the whisper server's model download was OOM-killed** in production at its 1 GiB limit (the Xet client buffers the model in memory; found by the master). It now downloads by plain HTTP, streamed to the volume, with a 3 GiB limit. In review, with the start-up fixes above.
- **7 Nov:** the start-up fixes merged (pull request #20, `4e7a58e`): the pull secret, the retry for a full database, and the model download by plain HTTP. Sent to the master to pin; waiting for the whisper pod's first start.
- **7 Nov: `4e7a58e` live** (the master's check at 08:12 Iraq time). svc-analysis is Synced and Healthy, and the whisper pod is Running and Ready with no restarts.
  - **The shared PostgreSQL ran out of connections from 07:37 to 08:00 Iraq time.** sb-analysis restarted once and one migrate hook failed; both recovered without help.
  - **sb-analysis's share is small:** 2 pods × at most 4 connections, about 16 while a rolling update overlaps them, and 2 more during the migrate hook. No transaction stays open across an AI, Whisper or storage call.
  - **The cause (the master):** tks, igs and fbs rolled out at the same time. PgBouncer keeps a server pool per database and user, and about 12 of them share the 50 slots. At 07:46 Iraq time: fbs 11 (10 idle; now capped at 4 per process), PgBouncer 9, tks 9, analysis 6, igs 4, DigitalOcean 4, schema registry 2, postgres 2. The master is putting the database's size to the owner. sb-analysis's limits stay as they are (the master agreed).
  - **Whisper (the master, 08:40 Iraq time):** Ready since 07:33 Iraq time with no restarts, and it answers its health check. The model download finished this time.
  - **No transcription yet:** no video without captions has arrived, because the collectors don't have their proxies yet.
  - **analysis-media's readiness** failed twice (08:12 and 08:39 Iraq time). Its check pings PostgreSQL with a 3 s timeout, and the shared database was busy. It processes the queue, not requests, so a short not-ready changes nothing.
- **7 Nov, 10:07 Iraq time: PostgreSQL full again** (the master: 47 of 50, analysis 4). The master asked every service to stop touching the database every few seconds, and for a migrate hook that waits for a free slot. **Merged in pull request #25 (`fdf8e73`):**
  - **Idle polling:** an empty queue is polled by one worker for all of them, less often each time: real time up to every 15 s, media up to 30 s, the outbox up to 10 s. A real-time item is still claimed within about a minute.
  - **Readiness** pings PostgreSQL at most once a minute and reuses the answer; liveness never touches it.
  - **Result:** an idle `analysis` pod sends about 20 queries a minute instead of about 12 a second; `analysis-media` under 10 instead of 2 a second.
  - **The migrate hook** waits up to 30 minutes for a free slot, including PgBouncer's own "pool full" errors, instead of about 2.
- **7 Nov, 10:47 Iraq time: `fdf8e73` live** (the master; platform pull request #32, with igs's equivalent change).
  - The migrate hook ran twice and applied both times. The second time it found the database full ("server login has been failing … remaining connection slots are reserved"), waited 1 s, then went through. Before, that failed the deployment.
  - About 7 minutes after the new pods started, sb-analysis held 5 server connections (1 active, 4 idle), down from 6. The platform now closes idle server connections after 10 s, and PgBouncer replaces older ones within an hour.
- **7 Nov, 14:32 Iraq time: PostgreSQL's limit is 200 connections, up from 50** (the master; platform pull request #38). Right after, 64 were in use across every service. sb-analysis's pods came through PostgreSQL's restart without restarting. Nothing pending from sb-analysis.
- **7 Nov: the quality check stays Haiku-only** (the owner: "just Haiku for now"). The one-off Opus labelling of 1,200 items (fix 7) doesn't run, so Haiku's quality stays unmeasured. A person can still label the sample at no AI cost. Decision 22.
- **10 Nov, 23:58 Iraq time: too little analysis (the master).** First reported as 559 results in 24 h; corrected at 00:30 (a different window): 4,537 results published and 4,509 stored in 24 h, 186,545 stored since 7 Nov, nothing lost. Still behind: about 13,800 new tt items a day, no media analyses and no author flags. analysis-analysis had restarted 8 times.
  - **Not by design:** every post and comment is queued; nothing samples or filters. Items mentioning a tracked entity go real time, the rest through Anthropic's batch API.
  - **A defect in the batch lane fits it** (counts asked of the master to confirm):
    - a round claimed up to 100,000 items, leased them for 26 h before sending, then sent batches of 5,000 requests (over 100 MB each in memory, in a 1 GiB pod);
    - a crash or an error before sending held those items for 26 h, and their estimates kept counting against the $10.
  - **In review, the fix:**
    - items are leased for 15 minutes until their batch is sent, and only then for 26 h;
    - rounds take 10,000 items per route, in batches of 1,000 requests;
    - a failed round gives everything back at once;
    - each round first releases work a stopped round left behind and closes its requests, so their estimates stop counting.
- **10 Nov, the master's counts** (00:16 Iraq time):
  - **the pod:** OOMKilled at 94% of 1 GiB (the last restart at 05:21 Iraq time);
  - **spend:** $0.84 and $0.88 for the day, so the budget isn't the limit;
  - **the queue:** 203k items, oldest 3 days, with ingest paused;
  - **the batch lane:** 10,983 batch requests sent in 24 h, 1,116 back;
  - **media:** about 900 images analysed ($0.20), but nothing reached Kafka: no media analyses, no acks, and none of the 4 flagged authors.
  - **The batch fix merged** (pull request #28, `4099fa1`), sent to the master to pin. As a stopgap the master raises the analysis pod to 2 GiB.
  - **Publishing is not blocked** (the master's metrics, 00:17 Iraq time, correcting my first guess): outbox backlog 0, no produce errors, and `analysis-kafka` may write `content.*`. In 24 h we published 2,354 results (`analysis.result.v1`) and 1,487 media acks on `fb.media.processed.v1`.
  - **The image analyses are fbs's own media** (`fb.media.ready.v1`). By owner decision 17, fbs's results stay in Postgres until fbs moves to the contract, so `analysis.media.v1` stays empty by design until media arrives through the contract.
  - **No collector hands media off yet** (the master, 00:30): no record on `content.media.ready.v1` in 24 h, and nothing undecodable. tks's media hand-off is off, like yts's, until the owner decides. The 100,716 "media" are `content.media.v1` metadata. Media analysis of tt and yt waits on that decision, by design.
  - **Merged, as hardening** (not the cause): the relay no longer lets one refused record hold back the rest (pull request #29, `76261b0`).
  - **Merged:** a read-only `sb-analysis batches` command for the open batches' provider status, and a count of media announcements skipped without a bucket or key (`sb_analysis_media_total{outcome="no_location"}`) (pull request #30, `912ea05`).
- **11 Nov, 01:25 Iraq time: `912ea05` live** (the master; with #28, #29 and #30; migration 00007 applied; analysis pod at 2 GiB):
  - **with 4099fa1 (00:47):** the stranded batches completed, 14,098 requests back in 30 minutes; the post queue fell from 6,690 to 336;
  - **since the new pod:** 19,919 batch requests sent in 15 minutes, 10 batches open, no restarts;
  - **the comment queue** is back at 200k: the released claims returned, and ingest stays paused until the queue is under 150k. It should drain as the batches complete;
  - **next:** the master's morning report, and `sb-analysis batches` once the platform has an ops action for it.
- **11 Nov, 09:16 Iraq time: is yt analysed, saved and acknowledged?** (the platform, for the owner). Answered from the code:
  - yt posts and comments are read like every collector's; while ingest is paused (queue over 200k, until 150k) they wait in Kafka;
  - yt media keeps flowing. Thumbnails, avatars and banners aren't analysed by policy: each is acknowledged SKIPPED with its reason, so yts can delete it;
  - results are stored in Postgres and published to `analysis.result.v1` and `analysis.media.v1` for the writer.
  - **Merged and live:** `sb-analysis stats` (pull request #32, `5932d27`, live 09:53 Iraq time). Also fixed: `items_ingested_total` had counted a mixed batch under its first item's platform.
- **11 Nov, 09:53 Iraq time: the first per-platform counts** (24 h):
  - **YouTube:** 79,781 analysed, 28,195 waiting; 348 files, all thumbnails, avatars or banners, skipped and acknowledged.
  - **TikTok:** 17,170 analysed, 132,653 waiting; **57,000 media files pending**. tks deletes them 72 h after announcing them, the first about 04:00 Iraq time on 14 Nov.
  - **Facebook:** 349 analysed, 33,195 waiting; 6,035 files analysed.
  - **The budget:** $12.84 spent against $10, so paused. Text needs about $30 a day at the measured $0.13 per 1,000, plus a backlog; tt images about $13 a day at $0.22 per 1,000. Whisper takes about 1,000–2,000 tt videos a day without captions.
  - **For the owner:** A (about $60 a day for 3 days, then about $45), B ($10 with caps that keep room for images), or C (narrower media scope). Put by the master.
  - **Merged and live:** media jobs check the budget before fetching their file, so a paused budget costs no downloads (pull request #33, `3367e51`; live 10:31 Iraq time, analysis and analysis-media, 0 restarts).
  - **Still with the owner:** the budget (A, B or C), yt thumbnails (about $7 a day), and Whisper's capacity (a second replica, a 120 s limit, or frames only while it is behind).
- **11 Nov, 13:35 Iraq time: the platform's questions for the owner's budget briefing**, answered from the code and the measured costs:
  - **tks's 29k downloads in 3 h vs our 58,100 announced in 24 h:** different windows (ours ended at 09:53, tks's started at 10:15). A file reaches us only once announced on `content.media.ready.v1`; each media ID counts once.
  - **At tks's current (catch-up) rate:** images (141k a day, 80% covers) about $31 a day at the measured $0.22 per 1,000; videos (90k a day) about $36 a day at an estimated $0.40 per 1,000 (8 frames and the transcript). Media about $67 a day, text about $30.
  - **Found:** while ingest is paused, posts wait in Kafka but their media keeps arriving, so a video would go to Whisper and be analysed without its post's text and captions.
  - **Going to the owner** (via the platform): the budget, and whether covers stay analysed (dropping `image/cover` saves about $25 a day at this rate, for every collector).
- **Merged: a file waits for its post** (the platform agreed, 13:32 Iraq time; pull request #35, `53078a7`). It goes back to the queue, without losing a try, until its post is stored (ingest wakes it), or 12 h before its deadline, or 7 days after it arrived. Also fixed: claiming a file with no deadline failed (pgx can't read `infinity` into a time).
- **11 Nov, 13:36 Iraq time: the owner's decisions (D-052, via the platform):** the daily cap goes from $10 to **$60**; YouTube thumbnails: not now; tks downloads videos only for posts up to 7 days old.
  - **In review:** `daily_usd: 60`, with caps per route so neither side starves the other: comment $30, post $12, image $18, video $12. Text keeps at least $30 and media at least $18.
  - **Merged:** pull request #36 (`b7d6e67`, with #35), sent to the master to pin.
  - **Caveat:** batch requests count at their (low) estimate until their batch returns, so a day can pass the cap by what is in flight (11 Nov: $12.84 against $10).
  - **The estimate fix** (the master agreed, 13:51 Iraq time; in review): a batch request's system prompt and schema are priced as plain input. For a 1-comment request that's $0.00041 instead of $0.00005, a little above what is paid, so the cap is reached from below.
- **11 Nov: a second Whisper server** (the owner's video choice, D-052). The platform asked what it takes.
  - It needed a chart and code change: one volume per server (ReadWriteOnce), and one video per server at a time (a Service balances connections).
  - **Merged:** `whisper.replicas`; the first server stays as it is; the analysis sends each server one video at a time (pull request #37, `b18f5e8`). The master pins it with `whisper.replicas: 2`; the autoscaler adds a 7th node for the CPU (2 CPU and 4Gi per server).
- **11 Nov, 14:29 Iraq time: first readings after `b7d6e67` and `3a739b6`** (the master):
  - **No items analysed in 90 min:** the $10 cap held everything until 13:57, and since then the first batches were in flight. The 200k pause holds only ingest, not the analysis of queued items. The master checks the open batches and the spend.
  - **Media:** in 30 min, 235 analysed, 6,234 waiting for their post, 470 failed with a retry. sba-whisper-1 was Pending; the autoscaler added 2 nodes.
  - **Found, my bug from #37:** while sba-whisper-1 wasn't Ready, a video sent to it (with sba-whisper busy) failed at once and lost a try.
  - **In review, the fix:** an unreachable server leaves the pool and is tried every 10 s. A video waits up to 1 minute for a reachable one, else goes back to the queue without losing a try. The master is checking the 470's reasons, in case there is more than this.
- **11 Nov, 14:42 Iraq time: still no batch sent since the cap went to $60.** The master: no open batches, budget not paused, no batch log lines. But spend rose $2.70 in 10 minutes.
  - **Cause:** a round claims 10k comment items and builds every request (about 10 a second) before sending any. The 14:07 round was cut by the restart at 14:20; the 14:30 round was still building. The spend is the requests' estimates, counted as they are built.
  - **Merged** (pull request #39, `8d6455d`; the master agreed): each batch goes out as soon as its 1,000 requests are built, and the first round runs at start. Also, `sb-analysis stats` shows why media files are retrying or failed (counts and reasons, for the platform's analysis-stats action; no ad-hoc SQL on production).
- **11 Nov, 15:15 Iraq time: fbs's cut-over to the contract (REQ-018, from 13 Nov).** fbs asked whether items analysis already has from `fb.*` would be analysed and paid for again.
  - **Answer:** matching is by ID and a hash. Posts would often differ (link titles, media IDs) and be paid again. Matching items would never reach `analysis.result.v1`, since `fb.*` results stayed in Postgres.
  - **In review, "adoption at cut-over"** (the master agreed): the first contract version of an item already analysed from `fb.*` is adopted with no AI call, and its stored analysis is published on `analysis.result.v1`. Edits and new items are analysed as usual.
  - **Merged** (pull request #40, `84ceb71`).
  - **Asked of fbs** (via the owner): the same IDs, a reply as a comment with its parent, the link title in description, and `media_id` plus `sha256` on `content.media.ready.v1`.
- **11 Nov, 15:22 Iraq time: after `8d6455d`** (the master): the batch lane sent 21,113 requests in 25 minutes and 10 batches are open; spend $30.14 (estimates in flight).
  - **Media failure reasons** (from `sb-analysis stats`): TikTok 84 retrying, 83 of them Whisper answering HTTP 500. Waiting for the whisper pods' logs. Also 1 ffmpeg killed (timeout or memory) and 3 jobs cut by the restart.
  - **In review:** a pool server's errors name it; jobs cut by a shutdown go back without losing a try.
- **11 Nov, 15:28 Iraq time: found, the first whisper server had never transcribed.** The master: since 7 Nov sba-whisper answered every transcription with 500 ("Cannot load the target vocabulary from the model directory"). Its /health was fine, so it stayed Ready. sba-whisper-1 works.
  - Over 3 h: 1,522 transcriptions failed and 117 succeeded, all on sba-whisper-1 since 14:27.
  - **In review (one PR with the two fixes above):**
    - the init container checks the model's files against the Hub (size and checksum), sets a faulty copy aside and downloads again;
    - the server starts only once it transcribes a second of silence;
    - on a video's last try, a server error means its frames are analysed without a transcript (the master agreed);
    - `sb-analysis media-retry` puts back the videos that failed this way while their files last.
  - **Merged and live** (pull request #41, `5d90a15`, with #40). Both whisper servers are Ready: 27 transcriptions ok and 0 errors in 10 minutes. media-retry: 19 videos matched, all in time, applied.
- **11 Nov, 16:10 Iraq time: TikTok media over 5 days** (analysis-stats 120h): 105,461 announced, 822 analysed, 94,605 pending, 10,015 skipped by policy (audio covers, profile pictures).
  - **Why:** until 13:57 today the $10 cap held media most of each day; since 14:00 files wait for their posts; and the 200k pause held posts too.
  - **Merged and live** (pull request #42, `661b17b`): the pause holds comments only; `sb-analysis stats` shows pending media by deadline.
- **11 Nov, 17:15 Iraq time: after `661b17b`** (the master): media moving (449 analysed in 30 min, no holds for posts). But the queue jumped from about 201k to 537k in an hour: the posts' Kafka backlog came in at once (tiktok 279k, youtube 81k, facebook 39k, instagram's first 24k).
  - My premise that posts were few was wrong: short posts queue on the comment route.
  - Comments stay paused until the queue falls to 150k. Numbers for the owner's choice (raise the text caps for a few days, or a later backfill) are with the master.
  - **In review (the master agreed):** a safety valve. Posts pause too, at 1,000,000 queued (resume at 900,000), with `sb_analysis_ingest_posts_paused` and `AnalysisIngestPostsPaused`.
- **11 Nov, 17:20 Iraq time: the comment backlog** (the master's readings at 14:20Z).
  - **Waiting:**
    - in Kafka: 2,297,212 comments on `content.comment.v1`, 747,903 on `fb.comment.v1`, and 531,733 posts on `content.post.v1`;
    - in the queue: about 537k.
    - Real cost in the last 24 h: comment $12.74, post $1.39, image $1.85, video $0.20.
  - **Arrivals:** the 3.05M comments arrived in about 16 h, since the pause at about 22:00Z on 10 Nov. That's about 30× the pace before 10 Nov, so it looks like the collectors' catch-up. I've asked the master for the hourly rates and for the age of the oldest unread record.
  - **At today's caps:**
    - text runs at about 250–270k items a day;
    - comments resume around 03:00 on 16 Nov (Iraq time), if the catch-up ends now and posts arrive at about 60k a day;
    - by the time the oldest pass Kafka's 7 days, about 400k comments are read, and about 2.5M expire.
  - **Saving them through Kafka alone:**
    - about 630k items a day for 7 days: comment route about $70 a day and post route about $30 (half the posts long), with the daily cap about $130;
    - about $430 more over the week, of which about $330 is for the comments that would otherwise expire.
    - This only works if the catch-up has ended.
  - **Store first (my suggestion, for the owner):** raise the comment pause to about 6M, so ingest moves the waiting items into Postgres in about 8–10 hours.
    - Nothing expires, and there's no AI cost until items are analysed.
    - It costs roughly 5–10 GB of disk and several hours of heavy writes on the shared server, which needs the owner's approval.
    - It's a config change only.
  - **Backfill instead:** let them expire, and the collectors replay them later. The AI cost per item is the same. It depends on their stores keeping everything.
  - **`content.post.v1` still 532k behind:** ingest ran at about 117 items/s in the first hour, which would clear 532k in about 75 minutes. Readings are asked for, to tell whether posts are still arriving fast or ingest slowed.
  - **In review:** the comment and post gates now share one queue count (`82b6503`), instead of counting the queue twice every 5 s.
- **11 Nov, 17:45 Iraq time: comments are a steady flow, not a catch-up** (the master, end-offset growth at 14:27Z).
  - Comments arrive at about 2.7M a day (24 h and the last 6 h agree), and posts at about 222k a day. `content.post.v1` is draining at about 8.8k a minute, so ingest is fine.
  - So the estimates at 17:20 (about 2.5M expiring once, and the one-week caps) are replaced by a steady-state choice.
  - **The owner's options** (daily total including media's $30):
    1. $60 (today): all posts, the priority comments, and about 2% of the others;
    2. about $100: the same with a fixed 10% sample (my suggestion to start);
    3. about $155 with 25%, or about $240 with 50%;
    4. about $415 (about $12.5k a month): everything. This needs faster batch building and a higher claim limit first.
  - **What exists:** watched accounts and items naming a tracked entity go first, in the real-time lane.
  - **New code, once the owner chooses:** one selection step at ingest. It takes, in order:
    - the urgent items;
    - the comments on urgent posts;
    - a stable sample of the rest.
    - Unchosen comments are counted, not stored.
  - **Possibly cheaper per comment** (measurements asked for):
    - the 1-hour prompt cache for batches;
    - no AI for comments with no letters.
    - There's no cheaper model on our list.
- **11 Nov, 18:30 Iraq time: posts pause at 1M; fuller requests next.**
  - **Merged** (pull request #43, `6f12507`, the master to pin):
    - posts also pause at 1,000,000 queued (resume at 900,000); comments keep the 200k pause;
    - the two gates share one queue count.
    - **CI:** the runner's Ubuntu mirrors were unreachable all afternoon, so the integration job now runs ffmpeg from our own media image (Debian's ffmpeg, as in production) when apt can't install it.
  - **The comment route's cost, measured** (the master, 24 h to 14:40Z): 114,402 comments, $13.01, about $0.114 per 1,000.
    - Cache reads were 55% of it: 12,515 tokens a comment, against a cached prompt and schema of about 7,000. So most requests carried one or two comments, or were re-sent.
    - Cache writes were about 89,000 tokens: the 1-hour batch cache would only add cost. It was held, and is kept unpushed.
  - **In review (the master agreed):**
    - `sb-analysis stats` adds requests by route and lane (how full, re-sent, tokens and cost per request) and the real-time lane's setup (hot accounts, watchlists, tracked entities; none set up in production);
    - a batch round fills its threads' requests from the queue (`batch.top_up_items`), with migration 00008 (an index on the queue by thread, built concurrently).
    - The master pins it off first (`top_up_items: 0`) for a baseline, then on. Expected: about $0.06 per 1,000 comments instead of $0.114.
    - Several threads per request (iii-b) is on hold until these numbers are in.
- **11 Nov, 19:00 Iraq time: 10% of comments (the owner's D-053).**
  - **Merged** (pull request #44, `691663c`, the master to pin): per-request stats and the round's thread top-up, pinned off first (`top_up_items: "0"`) for a baseline.
  - **In review:** `analysis.comments.sample_share` (production 0.10).
    - A fixed 10% of comments on every platform, chosen by content ID, the backlog included.
    - The rest are counted per day and platform, not stored or analysed; their raw text stays with the platform.
    - Real-time items and comments already stored are always kept.
    - `sb-analysis stats` shows received against sampled per platform, with the scale factor.
    - Migration 00009 adds `analysis.comment_sample_daily`.
- **11 Nov, 19:30 Iraq time: why every comment went out alone.**
  - **Live** (pull request #45, `b560bb5`, master #104): the 10% sample (9.7–10.1% per platform in the first 5 minutes), the top-up on, and comments flowing again (the pauses raised to 1.5M and 2M).
  - **The before reading** (6 h at 15:59Z): 61,530 comment requests for 61,530 items, 100% one-item, 13,238 cache-read tokens per request, $0.113 per 1,000.
  - **The cause:** an item tried before went alone, whatever its failure. The oldest backlog had almost all been tried before (old 26 h leases, restarts, errored batches), so every round sent one item a request, and the top-up (first tries only) found nothing to add.
  - **In review (the master agreed):**
    - only an item whose own answer failed goes alone; items back for other reasons group again;
    - the top-up takes those too;
    - `stats` shows the queue by tries and why entries came back.
    - Expected: 3–10 items a request at first, $0.05–0.07 per 1,000 comments instead of $0.113.
- **11 Nov, 20:15 Iraq time: no comment or post batches for an hour.**
  - **The reading** (the master, `stats 1h` at 17:02Z on `9df9e1e`): no comment or post batch requests in the hour.
    - The comment queue was 827k never tried (so retries weren't why requests carried one item), and the top-up added nothing.
    - 1,112 posts went alone after cut-off answers.
  - **Causes:**
    1. **The route caps by estimate.** In-flight batch requests count at their estimate, about 3.6× their real cost for one-item comment requests. So the $30 comment cap was reached with real spend far below it, and rounds held everything until batches returned. A held round also built and released every group one by one.
    2. **One item a request:** a claim takes the oldest 10,000 of about 880k items, and a page's or thread's items are spread across the whole backlog. So each claim holds about one item per page or thread. The top-up fills comment threads; short posts (grouped by page) aren't topped up yet.
    3. **Cut-off answers:** max_tokens 6,000 (comments) and 2,000 (posts) were too small for full groups.
  - **In review (the master asked for a fix):**
    - in-flight requests counted at the route's measured ratio of real to estimate (0.25–1);
    - a held round releases at once, and a spent route claims nothing;
    - today's spend per route in `stats`;
    - max_tokens 16,000 for comment and post.
  - **Proposed next:** a page top-up for short posts. It needs the page on the queue entry, a new column and a migration.
- **11 Nov, 20:35 Iraq time: the estimate made accurate, in place of the ratio.**
  - **The master (17:16Z):** the cap must stay a true ceiling ("up to 60 usd"), so no discount on in-flight work: make the estimate itself accurate, and show the worst case.
  - **Merged before that answer arrived** (pull request #48, `a249332`): the ratio, the held round released at once, the spent route claiming nothing, max_tokens 16,000. **Not to be pinned:** it is replaced in pull request #49.
  - **In review (pull request #49, with the fb-media input):**
    - no ratio: in-flight requests count at their estimate;
    - the estimate prices the prompt at the route's measured share read from the cache (last day), the rest as cache writes, and output at the route's measured tokens per item; without a measure, 150 tokens an item and the prompt as a miss;
    - the prompt's size at 1.75 bytes a token, as measured (the old 3 read it as about 7,700 tokens instead of 13,238);
    - a one-comment request: about $0.000115 estimated against $0.000113 paid (was $0.00041);
    - every request's worst case (its prompt written to the cache) stored in `provider_requests.est_max_usd` (migration 00010, a nullable column: no rewrite);
    - `stats` prints the worst case per route and for the day, and what new requests are priced at.
  - **Then:** the page top-up for short posts (the master agreed), as its own pull request.
- **11 Nov, 20:50 Iraq time: the estimate is in; short posts get the page top-up.**
  - **Merged** (pull request #49, `c0e38aa`, sent to the master to pin in place of `a249332`): the accurate estimate with each request's worst case, the `fb-media` input and the unchanged-copy fix. CI green (test, integration, images).
  - **In review:** the page top-up for short posts (the master agreed, 17:16Z).
    - Short posts (up to 280 characters) go on the comment route, grouped by page, but a claim held about one post a page.
    - A round now also claims each claimed page's other waiting short posts, after the threads, within the same `batch.top_up_items`.
    - A short post's queue entry carries its page: migration 00011 adds `analysis_queue.source_id` (nullable, no rewrite), an index built concurrently, and `analysis.backfills`.
    - Entries queued before it get their page from a throttled backfill in the `rollup` role: 10,000 entries a pass, a 2-second pause, its place kept across restarts. `stats` prints its progress.
  - **Also in review: live first for fbs's backfill** (the master, 17:46Z). A contract post or comment marked `provenance.mode = CRAWL_MODE_BACKFILL` takes the batch lane at background priority, so claims take every live item before it.
  - **Confirmed for the master** (tested): a comment already analysed from `fb.*` and sent again through the contract is adopted with no AI call, even outside the 10% sample, and published once. Sent again unchanged, nothing happens. A comment never seen is kept only in the sample.
- **11 Nov, 22:10 Iraq time: fuller requests in production; fbs through the contract.**
  - **Live:** `c0e38aa` (the accurate estimate, fb-media) at 20:57; `b6bd362` (the page top-up, live first for backfills) at about 21:05 (master #108, #109).
  - **The reading** (the master, `stats 1h` at 18:16Z):
    - comment route: 2,698 requests for 13,125 items, **4.86 items a request, 49% one-item** (was 1.00 and 100%). 49% is expected with the 10% sample: a thread keeps about 1 comment in 10;
    - post route: 732 requests for 10,000 items, 13.66 items a request, 1% one-item;
    - spend today $28.20 of $60; comment $21.46 of $30 ($15.31 answered, 10,761 requests in flight at $6.16, mostly at the old, higher estimate until they come back);
    - new requests priced at 152 output tokens an item, the prompt 100% read from the cache (135,777 answered in the last day);
    - page backfill running: 540,000 queue entries read, 313,573 short posts given their page;
    - the sample: 10.0% on fb, ig and tt, 9.9% on yt.
  - **fbs switched** at 21:45 (the master, #110): fbs `3af55b0` publishes its posts and comments on `content.*`, and sb-analysis reads `[content, fb-media]`. Facebook items now come from `content.*`.
  - **In review:** `values-production.yaml` set to `[content, fb-media]` (the master agreed), so a deploy from the repository doesn't read both paths again.
  - **Next:** the master's 19:20Z reading (the real cost per 1,000 comments on the new code); fbs's backfill (fbs #84), which the master will announce.
- **11 Nov, 22:50 Iraq time: no batches sent since 21:18; fixed in review.**
  - **The reading** (the master, 19:21–19:35Z): no `batch submitted` line after 18:18Z, and no comment or post request in the hour. Polls kept collecting (`batch collected` about every 6 minutes). Nothing wrong with the budget ($24.93 of $60). The fbs switch pod (about 21:50) sent nothing either.
  - **The cause (code):** the batch lane collected and sent in one loop. A poll checks up to 10 finished batches, and collecting one of 1,000 results takes about 6 minutes, so a poll held back every round for most of an hour. A new pod polls before its first round, so with about 11 batches waiting it never sent. No item was lost: they waited in the queue.
  - **The fix (in review):** sending and collecting in two loops of their own; a poll claims one batch at a time; a log line for every round, every collection's duration, and every change of the provider's circuit. A test holds a collection open and checks that a new batch is still sent; it fails on the old loop.
- **11 Nov, 23:55 Iraq time: the stall fix is pinned; older readings ignored, for the backfills.**
  - **Pinned:** `7f61d47` (the stall fix, with `[content, fb-media]` in the chart; platform #113). The master's after-reading is pending: GitHub Actions failed to start runs from about 23:23.
  - **fbs's backfills** (production's, then the pilot's: about 2.57M comments, 1–7 Nov; the owner: the same 10% sample, about $28 over about 3 days). Confirmed from the code:
    - imported and backfill-mode items take the same sample, behind live items; `ACQUISITION_IMPORT` isn't treated differently;
    - the sample is fixed by content ID, so a re-send never changes it;
    - the cost per comment is the same as live.
  - **In review: older readings ignored** (fbs confirmed `envelope.version` only rises per item). A contract copy at or below the stored version changes nothing; without it, a pilot copy of a comment production also read would be paid for again and replace the newer text. The production backfill should run first.
  - **The queue's cap** counts the whole queue (1.5M, resuming at 1.4M), so the master paces the pilot backfill on the total depth.
- **12 Nov, 00:35 Iraq time: sending works again; fuller and cheaper comment requests; long posts cut off.**
  - **The reading** (the master, `stats 1h` at 21:11Z, on `7f61d47`):
    - comment route: 6,069 requests for 60,780 items, **10.01 items a request** (18% one-item); per answered request 624 input, 13,810 cache read and 864 output tokens; **$0.055 per 1,000 comments** (was $0.113; 161 answered so far);
    - post route: 10,863 requests for 30,000 items, 2.76 items a request (86% one-item: the cut-off posts below, retried alone);
    - open batches 3, no restarts; the day so far $1.66 of $60 ($5.10 at worst).
  - **Long posts cut off:** 9,344 posts (and 80 comments) came back "truncated": requests of 13–14 long posts need over 16,000 output tokens. Each goes alone now (a single post's answer fits), and an item goes to the dead letters after `limits.max_attempts` (5).
  - **In review:** a request carries no more items than 60% of its route's `max_tokens` holds at the route's measured output per item: about 8 long posts a request.
  - **Pinned:** `a5bc8ff` (older readings ignored), before fbs's production backfill (about 2.03M events at 500 a second).
- **12 Nov, 01:40 Iraq time: Instagram never analysed; the claim shared across platforms.**
  - **Pinned:** `6b102da` (groups fit their answer: about 8 long posts a request). fbs's production backfill is running (about 887,600 comments and 64,300 posts at 500 a second).
  - **The finding** (the master, 22:15Z): no Instagram result in ClickHouse in 7 days, though 71k ig items wait and the 10% sample holds; fb only 3,733.
    - **ig: queue order.** A round claimed the oldest items of all platforms, and ig's (from 11 Nov) sat behind about a million older ones. Nothing is switched off; no code treats ig differently.
    - **fb: by design.** Results of items read from fbs's own topics stay in Postgres (decision 17); only contract items are published, since 21:45. The production backfill publishes every adopted item's stored analysis once.
  - **In review:** each round gives every platform waiting an equal share of its oldest live items, then the rest oldest first (live before backfill). Migration 00012 adds an index on the content ID's platform code, built concurrently. `stats` shows each platform's oldest waiting item.
- **12 Nov, 01:55 Iraq time: fair share merged; captions-only versions.**
  - **Merged:** `f06aaab` (the claim shared across platforms, migration 00012), in the master's platform #118.
  - **fbs's video captions (D-056):** a new version of a post whose only change is its captions is already unchanged (captions aren't part of the input hash): no new analysis, nothing paid. The captions are stored for the video's transcript. **In review:** such a version's number is now stored too, so an older reading after it stays stale.
- **12 Nov, 03:05 Iraq time: Instagram still 0 an hour after fair share.**
  - **The reading** (the master, 23:49Z): ig 72,600 waiting, 0 analysed; no comment batch answered in the hour on any platform (12,198 requests in flight); spend $19.48 of $60.
  - **Two possible causes:** batch latency (ig items in batches not yet answered), or igs marking its items as backfill, which fair share leaves out and which go after every live item. The master is checking `sb-analysis batches` and igs's provenance mode.
  - **In review:** `stats` splits each platform's waiting items into in flight and backfill, and gives the oldest not yet sent.
  - **`57P01` (connection terminated by the server)** at about 15:59Z and 23:47Z: comes from `pg_terminate_backend` or a server restart, not from any timeout of ours; rounds fail and retry, nothing paid. For the platform to check on DigitalOcean.
