# sb-analysis: status

*Kept current for the master session and the owner. Last updated 6 November 2026, 04:00 Iraq time.*

## Where it stands

| Area | State |
|---|---|
| Code | Built and tested: unit and integration tests with race detection against real Postgres, Redpanda and Valkey. In `main` of the public repository `Abdullah95k/securebrand-microservices-analysis` since 6 Nov (pull request [#1](https://github.com/Abdullah95k/securebrand-microservices-analysis/pull/1), merge `0706a71`, after green CI). `main` started as the fbs baseline the work was built on (`be2ec2f`). |
| Running | **Nowhere.** Never analysed real content; never called an AI provider with real data. |
| Pilot server | A separate deployment is ready (`deploy/analysis/compose.yml`, image `sb-analysis:dev`, folder `~/securebrand-analysis`). Not confirmed as cloned or built there; **not started**. |
| Quality | **Not measured.** The evaluation (`docs/analysis/evaluation.md`) is waiting for the owner's approval and API keys on the server. |
| Contract | Reads `fb.*` through its Facebook adapter (`analysis.inputs: [fb]`). This repository's draft `securebrand.content.v1` is **not used and will be retired** for the master's v1. Publishes `analysis.result.v1`, `analysis.media.v1`, `analysis.author.v1`, `analysis.topic.v1`; nobody consumes them yet. |
| Schema Registry | sb-analysis registers nothing (standing instruction: no `securebrand.content.*` until the master publishes v1). **But the pilot's shared registry already holds 9 subjects from this repository's copy** (confirmed 6 Nov, 00:58 Iraq time): the 4 `content.*` subjects, `securebrand/content/v1/content.proto`, and the 4 `analysis.*` subjects, almost certainly from a `make pilot-up` run from this copy around 1 November. The master (6 Nov): they don't block the platform, since staging and production get their own registry. **Leave them in place; delete nothing.** |

## Reports for the master (5 Nov)

| Part | File |
|---|---|
| A: the content contract and the input fields each analysis needs | [`docs/master/analysis-contract-notes.md`](master/analysis-contract-notes.md) |
| B: every output field, versioning, and a ClickHouse layout | [`docs/master/analysis-outputs.md`](master/analysis-outputs.md), [`analysis-outputs.yaml`](master/analysis-outputs.yaml) |
| C and D: operations, models, costs, media, embeddings; the repository and its fbs copy | [`docs/master/analysis-operations.md`](master/analysis-operations.md) |
| E: gaps against the production standard; the owner's decisions | [`docs/master/analysis-gaps.md`](master/analysis-gaps.md) |

## Standing instructions (from the owner, 5 Nov)

- Don't rename or remove topics, event fields, schemas or endpoints others use. Additions are fine, and are reported to the owner.
- Don't register `securebrand.content.*` in any shared Schema Registry until the master publishes v1.
- Nothing heavy on the shared pilot server (201.79.36.209) without the owner's approval.
- Keep this document current.

## Open decisions (owner or master)

1. **Real-time scope.** Which items must be analysed within 1–5 minutes (R4). The rest stays in the half-price batch lane.
2. **The daily AI budget** (after the quality test).
3. **sb-analysis's own text copy:** keep it, or slim to working state once ClickHouse holds content (the owner's decision 7, "for now").
4. **A repository of its own** for sb-analysis, without the fbs copy (about 2–3 days; Part D).

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
