# sb-analysis: status

*Kept current for the master session and the owner. Last updated 7 November 2026 (Iraq time).*

## Where it stands

| Area | State |
|---|---|
| Code | Built and tested: unit and integration tests with race detection against real Postgres, Redpanda and Valkey. In `main` of the public repository `Abdullah95k/securebrand-microservices-analysis`. **A repository of its own since 6 Nov** (REQ-011 fix 1, `b080a72`): fbs's code is removed, and fbs's module is a dependency for the fb protos only. |
| Running | **Started in production (7 Nov, 06:39 Iraq time)** at `5df7e33`: the migrate hook passed and svc-analysis synced. It reads `[content, fb]`, with a $10 daily hard stop and Claude Haiku. Running at `4e7a58e` (the master's check at 08:12 Iraq time, 7 Nov). The whisper server is up: the model download finished and it is Ready. No new runs on the shared server (owner's D-026). |
| Pilot server | **No new runs there** (owner's D-026, 6 Nov). The Compose deployment next to the fbs pilot (`deploy/analysis/compose.yml`) stays in the repository; it was never started. |
| Quality | **Not measured.** The evaluation (`docs/analysis/evaluation.md`, about $3–5) is approved; it runs on the production cluster once fbs collects there (owner, 6 Nov). |
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

1. **For the owner: the one-off quality check (fix 7).** It has Claude Opus label 1,200 real items once (about $3–5) to measure Haiku against. Does "Haiku only" cover this one-off check too?
2. **fbs's move to the contract:** a cut-over at one time T, designed when fbs's version is near (master, 7 Nov). No adapters for tks, igs or yts.
3. **sb-analysis's own text copy:** keep it, or slim it to working state once the platform's entity layer and the client app exist (the owner's decision 7, "keep both for now"; REQ-011, later).

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
  - **Waiting for:** the model download's log, and the first Whisper transcription.
