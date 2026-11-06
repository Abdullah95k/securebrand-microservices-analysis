# sb-analysis: status

*Kept current for the master session and the owner. Last updated 6 November 2026 (Iraq time).*

## Where it stands

| Area | State |
|---|---|
| Code | Built and tested: unit and integration tests with race detection against real Postgres, Redpanda and Valkey. In `main` of the public repository `Abdullah95k/securebrand-microservices-analysis`. **A repository of its own since 6 Nov** (REQ-011 fix 1, `b080a72`): fbs's code is removed, and fbs's module is a dependency for the fb protos only. |
| Running | **Nowhere.** Never analysed real content; never called an AI provider with real data. From 6 Nov, testing happens on the production cluster, at full scale (owner's D-026): no new runs on the shared server. |
| Pilot server | **No new runs there** (owner's D-026, 6 Nov). The Compose deployment next to the fbs pilot (`deploy/analysis/compose.yml`) stays in the repository; it was never started. |
| Quality | **Not measured.** The evaluation (`docs/analysis/evaluation.md`, about $3–5) is approved; it runs on the production cluster once fbs collects there (owner, 6 Nov). |
| Contract | Reads `fb.*` through its Facebook adapter (`analysis.inputs: [fb]`). The platform's contract **v1.0.0 is published** (6 Nov); adopting it (REQ-022) comes after the fixes REQ-011 lists before the canary. It retires this repository's draft `securebrand.content.v1` and its copy of `securebrand.analysis.v1`. Publishes `analysis.result.v1`, `analysis.media.v1`, `analysis.author.v1`, `analysis.topic.v1`; nobody consumes them yet. |
| Schema Registry | sb-analysis registers nothing (standing instruction: no `securebrand.content.*` until the master publishes v1). **But the pilot's shared registry already holds 9 subjects from this repository's copy** (confirmed 6 Nov, 00:58 Iraq time): the 4 `content.*` subjects, `securebrand/content/v1/content.proto`, and the 4 `analysis.*` subjects, almost certainly from a `make pilot-up` run from this copy around 1 November. The master (6 Nov): they don't block the platform, since staging and production get their own registry. **Leave them in place; delete nothing.** |

## Work in progress (from 6 Nov)

- **REQ-011, the fixes:** one row per fix in [`docs/master/analysis-fixes.md`](master/analysis-fixes.md). Fixes 1 (a repository of its own), 2 (its own chart, `values-production.yaml`, images on a tag) and 3 (no unlimited budget by accident) are merged; fix 4 (media memory) is in review.
- **REQ-022, adopting contract v1.0.0:** after the fixes REQ-011 lists before the canary; its record will be `docs/master/analysis-contract-adoption.md`.
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

1. **For the owner: run in production before REQ-022?** The master suggests reading fbs's `fb.*` topics in production until the contract is adopted. That needs a switch, not in REQ-011, that keeps results in Postgres and off the platform's `analysis.*` topics, whose contract format the draft results don't match. The canary also waits for fixes 3 to 6.
2. **For the owner: is the $5 a day a hard stop for urgent items too?** Today `analysis.budget.urgent_exempt: true` lets urgent real-time items continue over the cap. The master asks for a hard stop.
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
