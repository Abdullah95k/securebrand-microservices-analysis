# sb-analysis: status

*Kept current for the master session and the owner. Last updated 6 November 2026 (Iraq time), commit `bf32dd9`.*

## Where it stands

| Area | State |
|---|---|
| Code | Built and tested: unit and integration tests with race detection against real Postgres, Redpanda and Valkey. Branch `claude/jolly-bell-mb5sct` in the public repository `Abdullah95k/securebrand-microservices-analysis`, **not merged**: the repository has no `main` yet, and the base branch for the pull request is still to decide. |
| Running | **Nowhere.** Never analysed real content; never called an AI provider with real data. |
| Pilot server | A separate deployment is ready (`deploy/analysis/compose.yml`, image `sb-analysis:dev`, folder `~/securebrand-analysis`). Not confirmed as cloned or built there; **not started**. |
| Quality | **Not measured.** The evaluation (`docs/analysis/evaluation.md`) is waiting for the owner's approval and API keys on the server. |
| Contract | Reads `fb.*` through its Facebook adapter (`analysis.inputs: [fb]`). This repository's draft `securebrand.content.v1` is **not used and will be retired** for the master's v1. Publishes `analysis.result.v1`, `analysis.media.v1`, `analysis.author.v1`, `analysis.topic.v1`; nobody consumes them yet. |
| Schema Registry | sb-analysis registers nothing (standing instruction: no `securebrand.content.*` until the master publishes v1). **But the pilot's shared registry already holds 9 subjects from this repository's copy** (confirmed 6 Nov, 00:58 Iraq time): the 4 `content.*` subjects, `securebrand/content/v1/content.proto`, and the 4 `analysis.*` subjects, almost certainly from a `make pilot-up` run from this copy around 1 November. They would block the master's v1 under the same name; delete them (with the owner's approval) or avoid them first. |

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
5. **The base branch** for this repository's pull request.

## Changes since the last update

- **2 Nov:** the analysis runs as its own Compose project next to the fbs pilot; it creates only its own topics (`sb-analysis kafka-topics`).
- **5 Nov:** the master reports above. No code, configuration or server changes.
- **6 Nov:** the owner's read-only check confirmed the 9 schema subjects above on the pilot. Nothing was changed.
- **6 Nov:** answered the master's REQ-006 questions at the end of `docs/master/analysis-contract-notes.md`. Facts and proposals only; nothing built.
