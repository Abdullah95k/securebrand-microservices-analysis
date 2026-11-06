# sb-analysis against the production standard (Part E)

*For the master session. Every item of 04-service-standard.md that applies to a non-collector, checked against the code at commit `bf32dd9`, 5 November 2026 (Iraq time).*

**Statuses:** **Met**, **Partly**, **Missing**, **N/A**. **Effort** is a rough estimate in working days for this session. Paths are relative to the repository root.

## A. Packaging and deployment

| # | Status | Evidence | Fix | Effort |
|---|---|---|---|---|
| A1 Image per binary, versioned, in GHCR | **Partly** | Dockerfile target `sb-analysis` (`deploy/docker/Dockerfile`). The release workflow (`.github/workflows/release.yml`) builds only this copy's `fbs` images (plus a `-media` variant), never `sb-analysis`. No release has been tagged. | Release `ghcr.io/abdullah95k/sb-analysis:<version>` and `<version>-media` from the analysis repository's own workflow, never under the `fbs` name | 0.25 |
| A2 Non-root, read-only root filesystem, no capabilities | **Met** (Kubernetes) / **Partly** (Compose) | The chart's shared security context (`deploy/helm/fbs/values.yaml`: `runAsNonRoot`, `readOnlyRootFilesystem`, drop `ALL`); a distroless non-root image. `deploy/analysis/compose.yml` doesn't set `read_only` or `cap_drop`. | Add `read_only: true`, `cap_drop: [ALL]` and a `tmpfs` for temporary files to the Compose file | 0.1 |
| A3 Helm chart with every setting in values | **Partly** | Components `analysis` and `analysis-media` live inside **fbs's** chart (`deploy/helm/fbs`), tested by `make chart-check` and `chart-smoke` | A chart of its own (`deploy/helm/sb-analysis`), with its alerts, KEDA, migrate hook and values | 1 |
| A4 Config validated at start-up | **Met** | `internal/config/analysis.go` `Validate()` (routes, providers, budget, media, topics, entities, bots). sb-analysis refuses to start and prints why. | — | — |
| A5 Secrets from env or files, never baked in | **Met** | API keys as `FBS_ANALYSIS__PROVIDERS__*__API_KEY` from `deploy/analysis/analysis.env` (gitignored; `make analysis-up` refuses unless it is mode 600) or the chart's `secretEnv`. A keyless hosted provider is skipped, never logged with a key. | — | — |
| A6 Versioned migrations by a hook; backward compatible for one release | **Partly** | goose migrations in `migrations/analysis` (own version table), run by the Helm hook `analysisMigrate` or `run --migrate`. Backward compatibility isn't tested. | A CI job that runs the previous release's tests against the new schema; a written rule (add columns, never drop in the same release) | 0.5 |

## B. Runtime behaviour

| # | Status | Evidence | Fix | Effort |
|---|---|---|---|---|
| B1 `/healthz`, `/readyz`, `/metrics` | **Partly** | `:9090`, all three (`internal/platform/httpserver`). `/readyz` checks **Postgres only**. | Add Kafka to `/readyz` (and S3 for the media role) | 0.1 |
| B2 Requests and limits, measured | **Partly** | Set in the chart: analysis 250m/256 MiB with a 1 GiB limit; media 500m/512 MiB with a 2 GiB limit. **Not measured.** | Measure in the soak test (G3) and adjust | with G3 |
| B3 No browser | **Met** | None used | — | — |
| B4 Graceful shutdown | **Met** | `run.Group` stops every component on SIGTERM. Work is leased (queue, media, batches) and offsets are committed after the database write, so nothing in flight is lost. Compose `stop_grace_period: 60s`. | — | — |
| B5 State survives restarts | **Met** | No platform sessions. Submitted provider batches are stored (`provider_batches`) and polled again after a restart. All other state is in Postgres. | — | — |
| B6 Scales out; fleet-wide limits shared | **Partly** | Scales out through the consumer group, `SKIP LOCKED` and leases. The daily budget is fleet-wide (Postgres). **The provider rate limit (`rps`) is per process**, so 4 copies send 4× the rate. | A shared limiter (Postgres or Valkey), or `rps` divided by copies | 0.5 |
| B7 UTC inside; Iraq time for the owner | **Met** | Timestamps in UTC; rollup and budget days in `Asia/Baghdad`. Owner output says so ("WHEN (Iraq time)", `day_iraq`). | — | — |

## C. Reliability

| # | Status | Evidence | Fix | Effort |
|---|---|---|---|---|
| C1 Bounded retries, DLQ, caps | **Met** | 5 attempts per item, then `analysis.dead_letters`, replayed with `sb-analysis replay-failed`. Provider retries are capped (`max_retries: 2`). Media gets 5 tries with backoff capped at 30 minutes. | — | — |
| C2 Retention on every topic | **Met** (own topics) | `analysis.result.v1` and `analysis.media.v1` 7 days; `analysis.author.v1` and `analysis.topic.v1` 30 days (`configs/topics.yaml`). sb-analysis never changes fbs's topics (`sb-analysis kafka-topics` creates only its own). | — | — |
| C3 Stuck jobs closed | **Met** | Queue and media leases expire and are retried. Batch requests are leased for 26 hours. After 12 hours of batch failures, items move to the backup in real time. | — | — |
| C4 Circuit breakers | **Met** | One breaker per provider (`internal/platform/breaker`, used by `internal/analysis/provider/registry`). Not per query (not applicable). | — | — |
| C5 Session pool | **N/A** | No platform sessions. The analogue is provider availability: the backup provider plus alerts. | — | — |
| C6 Backpressure | **Partly** | Ingest never slows down: Postgres is the buffer, so a slow provider grows the queue on disk. The daily budget pauses non-urgent work. | A cap on queue depth that pauses ingest (Kafka keeps the rest for 7 days), plus an alert on queue size | 0.5 |
| C7 Daily budget cap | **Met** | `analysis.budget.daily_usd` (speech-to-text included; urgent items optionally exempt); `fbs_analysis_budget_paused` | — | — |

## D. Platform access

**N/A for D1–D5.** sb-analysis never accesses a social-media platform, uses no proxies and never touches bot checks.

## E. Data output and contract

| # | Status | Evidence | Fix | Effort |
|---|---|---|---|---|
| E1 Publishes once to Kafka | **Partly** | Results reach Kafka once, through a transactional outbox (`analysis.outbox`). But sb-analysis also keeps **its own copy of every text** and all results in Postgres (its system of record; the owner's decision 7, "keep both copies for now"). | Once ClickHouse holds content and results, slim sb-analysis's Postgres to working state (queue, registry, topic list, bot state). Needs the owner's decision. | 1–2 |
| E2 Platform events and `content.*` | **N/A** (not a collector) | Consumes `fb.*` through its Facebook adapter. `content.*` input built, switched off. | Move to the master's `content.*` v1 (analysis-contract-notes.md, section 4) | 3–4 |
| E3 Registered protobuf schemas, `BACKWARD_TRANSITIVE` | **Missing** | sb-analysis registers nothing. `analysis.proto` imports this repository's draft `content.proto`, so registering it now would also register `securebrand.content.v1` (forbidden until the master's v1). | After v1: import the master's enums, then register the `analysis.*` subjects with a job of its own, `BACKWARD_TRANSITIVE` | 1 |
| E4 Event ID, IDs, times, version, producer, raw pointer | **Partly** | Present: `event_id`; `content_id` (platform and native ID); `created_at`; `provenance.analyzed_at`; `analysis_version`; model, prompt, schema and topic-list versions. **Missing:** sb-analysis's own name and build version, and a pointer to the raw model answer. **Media:** `analysis_version` is always 1. | Add `producer` and `producer_version` to `Provenance` (additive); version media results like items (E7) | 0.5 |
| E5 Keys keep the order that matters | **Met** | `analysis.result.v1` keyed by `content_id` (one item's versions in order), media by `media_id`, flags by author, topics by topic ID | — | — |
| E6 Absent is not zero | **Partly** | Proto3 scalars can't mark "absent". A **refused** item carries neutral defaults, so consumers must check `refused`. `Sentiment.confidence` isn't set inside entity mentions. | Document it in the contract now. In a later version use `optional` for scores and counts, and add entity confidence. | 0.25 |
| E7 Edits and changes as new versions | **Met for items**, **Partly for media** | Items: an edit, escalation or re-analysis is a new version and record; history is kept (`analysis.analyses`, `is_current`). Media: a re-analysis **overwrites** `analysis.media_analyses` and keeps `analysis_version` at 1. | A media history table and a counted version, as for items | 0.5 |
| E8 Raw responses kept | **Missing** | The validated result is kept (`analyses.result` JSON), but **not the model's raw answer**, so a fix to the answer parser can't be applied to past answers without paying again | Keep each request's raw answer (compressed) in object storage, keyed by request ID, kept forever (R6) | 1 |
| E9 Bio and profile links | **N/A** | Collectors' item | — | — |
| E10 Coverage reported | **N/A** (collector item) | For analysis: per item `state` (analysed, skipped with a reason, failed, deleted) in `content_index`; dead letters | — | — |

## F. Observability

| # | Status | Evidence | Fix | Effort |
|---|---|---|---|---|
| F1 Metrics with a per-service prefix | **Partly** | 25 metric families (analysis-operations.md, section 9), but with the prefix **`fbs_analysis_`**, shared with fbs | Rename to `sb_analysis_` while keeping the old names for one release, and update the alerts | 0.5 |
| F2 Logs without content or personal data | **Partly** | JSON logs with IDs, counts and reasons. **One exception:** `internal/analysis/curate/entities.go` logs a candidate entity's name when a decision fails. It could be a private person's name before the model rejects it. | Log the candidate's hash instead | 0.1 |
| F3 Alert rules with runbook links | **Partly** | 8 rules in fbs's PrometheusRule (group `fbs.analysis`, `deploy/helm/fbs/files/alerts.yaml`): real-time and batch lanes stale, media deadline at risk, dead letters, refusals, backup share, budget 80% spent, outbox backlog. **Missing:** no ingest for N minutes; consumer lag on `fb.comment.v1`; queue size; disk and memory. | Its own PrometheusRule with those added | 0.5 |
| F4 Grafana dashboard | **Missing** | fbs's dashboard has no analysis panels | An sb-analysis dashboard: throughput, queue age, spend, refusals, backup share, media | 1 |
| F5 Owner health check | **Met** | `make analysis-status` (counts and spend per Iraq day); owner guide `docs/owner/analysis-decisions.md`; runbook `docs/runbooks/analysis.md` | — | — |

## G. Testing and CI

| # | Status | Evidence | Fix | Effort |
|---|---|---|---|---|
| G1 Unit and integration tests in CI, race detector | **Met** | `.github/workflows/ci.yml`: `go test -race -count=1 ./...` against real Postgres, Redpanda and Valkey. New tests are mutation-checked by hand. | — | — |
| G2 Fake platform | **Met** | A deterministic fake AI provider (`internal/analysis/provider/fake`) and fbs's fakefb; the pipeline's end-to-end tests run on them | — | — |
| G3 Load and soak tests (24 hours or more) | **Missing** | None for sb-analysis | A soak test with the fake provider at the pilot's volume (~450,000 items a day) for 24 hours, watching memory, disk, queue age and the outbox | 1–2 |
| G4 CI guards against secrets and real data | **Partly** | A CI step fails on committed gold or labelled data (`*.labels.jsonl`, `*.gold.jsonl`, `gold/`, `results.jsonl`). The evaluation tool refuses to write inside a git checkout. No secret scanner. | Add gitleaks to CI | 0.1 |
| G5 Contract conformance tests | **N/A until the master's kit exists** | — | Run the kit against the `content.*` adapter and the `analysis.*` output | 0.5 |

## H. Operations

| # | Status | Evidence | Fix | Effort |
|---|---|---|---|---|
| H1 Runbooks | **Partly** | `docs/runbooks/analysis.md` covers: queue backlog, media, dead letters, refusals, provider outage, budget, outbox, erasure, entities, topics, bots, re-analysis, model or prompt changes, agent access, metrics. **Missing:** start and stop, upgrade, rollback and backfill as sections (start and stop are in the owner guide). | Add those sections | 0.5 |
| H2 Backups with a tested restore | **Missing** | sb-analysis's state (entity registry and decisions, topic list and versions, bot-correction log, client tracking, results) sits in the pilot's Postgres, **with no backup**. Results can be rebuilt only by paying for re-analysis; owner decisions can't be rebuilt at all. | Production: managed Postgres with point-in-time recovery, plus a tested restore of schema `analysis`. Pilot: a nightly `pg_dump` of schema `analysis`, only with the owner's approval. | 0.5 |
| H3 Go-live checklist and canary | **Partly** | Rollout plan in `docs/design/analysis-service.md` section 18; owner steps. No checklist of its own. | Checklist: quality check passed, budget set, alerts on; canary of a few pages for 2–3 days, then all pages | 0.5 |
| H4 Cost report per 1,000 accounts | **Partly** | Cost model from code (`sb-eval cost`); estimates per 1,000 accounts in analysis-operations.md, section 13. Real spend per day is recorded (`provider_requests`, `make analysis-status`). | After the pilot runs: a measured report per 1,000 accounts from `provider_requests` | 0.5 |

## I. Security

| # | Status | Evidence | Fix | Effort |
|---|---|---|---|---|
| I1 Least privilege, one credential per service | **Partly** | On the pilot it shares fbs's database user `fbs`. Read-only login roles per client exist (`sb-analysis agent-role`, row-level views in schema `agent`). One key per AI provider. | Its own database user, limited to schema `analysis`; a separate read-only role for `sb-eval sample` | 0.25 |
| I2 Network policies, egress only as needed | **Missing** | fbs's chart has an ingress-only NetworkPolicy, off by default; no egress rules for sb-analysis | Egress only to the provider hosts (analysis-operations.md, section 6), Postgres, Kafka and S3 | 0.5 |
| I3 Dependencies current; images scanned | **Partly** | Dependencies current when written (Anthropic SDK v2.4.0). No `govulncheck` and no image scan in CI. | Add both to CI | 0.25 |
| I4 Client keys with scopes; personal data behind a scope | **Partly** | Per-client read-only database roles that see only the client's tracked entities and sources. `author_key` is pseudonymous; raw author IDs and names never reach sb-analysis. No API of its own: the client app serves data. | Decide with the master whether the agent views stay or ClickHouse row policies replace them | — |

## Gaps outside the standard (found in this review)

| Gap | Fix | Effort |
|---|---|---|
| **Latency against R4** (alerts in 1–5 minutes). Most comments use the batch lane (minutes to 24 hours), per the owner's decision 3. Only tracked-brand mentions and urgent sources go in real time. | The master and the owner decide which items are "hot": watchlists, the hot tier. Those go in real time; everything in real time adds ~$1,800 a month per 1,000 accounts. | config |
| **Quality per language isn't reported:** the evaluation report has no Sorani or Kurmanji breakdown | Add per-language scores to `sb-eval report` | 0.5 |
| `MediaAnalysis.transcript_lang` is never set | Fill it from the transcription answer | 0.1 |
| Thumbnails aren't analysed, even when the video itself isn't downloaded | Add `thumbnail` to `analysis.media.kinds` for that case | config |
| The binary links the whole fbs crawler (through `internal/app`, for one function) | A repository of its own (analysis-operations.md, Part D) | 2–3 |
| The env prefix `FBS_` is shared with fbs | `SB_ANALYSIS_`, with `FBS_` accepted for a release | 0.25 |
| `fbctl schemas apply` or `make pilot-up` from this copy would register `content.*` and `analysis.*` in the shared registry | Retire the copy (Part D); until then, never run them from here | — |
| sb-analysis's Postgres grows ~100 GB a month per 1,000 accounts, because of its text copy | E1 fix | — |

## Fixed since 5 November

| Gap | Fix (6 Nov, owner-approved) |
|---|---|
| Names of tagged people reached the AI provider; Facebook's UTF-16 tag offsets were read as characters | `analysis.privacy.mask_tagged_users` (default on), exact UTF-16 conversion, tag positions stored (migration `00003`), applied to everything sent and to the evaluation sample |
| An erasure event deleted analysis data, gated only by fbs's admin scope | `analysis.erasure.mode` (default `record`: nothing deleted); `sb-analysis erasures list` and `apply --request <id> --yes-delete` |

## The owner's decisions in `docs/owner/analysis-decisions.md` the master must know

Recorded on 1 November 2026 unless noted.

| # | Decision | What it means for the platform |
|---|---|---|
| 1 | **Comments may be sent to Anthropic** | Anthropic is a processor. Phone numbers and e-mails are masked before sending; author names never leave. The lawyer should check the data-processing and retention terms (Anthropic offers zero retention on request). |
| 2 | **Daily spending limit: set after the quality test** | $5 a day on the pilot until then |
| 3 | **"Within hours is fine"** for most comments | Batch at half price; tracked-brand mentions in about a minute. It conflicts with R4's 1–5 minute target unless "hot" items are defined (gaps above). |
| 4, 13 | **Topics are added automatically by an AI;** a person steps in only if needed | Sonnet decides proposed topics used by 5+ items, every 6 hours; duplicates are checked in Arabic and English; each change is a new topic-list version and a `TopicUpdate` |
| 5 | **Brands, organisations, public figures, positions and places are added automatically** | Names seen 5+ times are decided by Sonnet every 6 hours; spellings are merged into known entities |
| 6 | **Public figures and public accounts are stored; private people never** | When the AI is unsure, the person is not stored; private persons are only counted (`private_person_mentions`) |
| 7 | **Keep both copies of comments for now** (fbs's and sb-analysis's) | The storage line above; revisit with ClickHouse |
| 8–9 | **No human labeller; rely on the AI** | Quality is measured against Opus's labels (agreement, not accuracy); an ongoing Opus audit of 1 in 5,000 items |
| 10 | **Backup AI: Google Gemini** (the owner asked me to choose) | Gemini 3.1 Flash-Lite for comments and posts, Gemini 3.5 Flash for escalations, used only when its key is set |
| 11 | **Insulting religion is always flagged** (`religious_offense`) | A respectful question or disagreement, a stated belief, and criticism of a cleric's political actions are not flagged on their own. Peaceful political criticism and a person's sexuality or gender identity are not flagged on their own. Hate speech, threats, defamation, misinformation and the rest always are. **Flags are allegations for human review, not findings.** |
| 12 | **Bot flags are shown, and people can mark "not a bot", with a log** | Every item of a flagged account carries the flag. Corrections are logged (who, when, why, before and after) and published as `AuthorFlag`. |
| 14 | **Every comment is analysed,** even on very large posts | No sampling; identical comments of a thread are analysed once and copied |
| — | **Haiku, not a separate vision service, for logos and images** (chosen after comparing with Google Vision) | One vision model for logos, text in images and descriptions |
| — | **Bot detection: 5 or more items in 30 days** (the owner found 10 too many); small accounts that take part in coordination are scored too | `analysis.bots.min_items: 5` |
| — | **Generic titles must be accurate** ("رئيس الوزراء" alone isn't always Iraq's Prime Minister) | Bare titles are hints resolved from context, never a direct link |
| — | **The analysis runs as its own service, never inside fbs's stack** (2 Nov) | `deploy/analysis/compose.yml`, its own image and folder |
| 15 | **Tagged people's names are hidden from the AI** (6 Nov) | `@user` in everything sent; tagged brands and public figures stay; the stored text is unchanged |
| 16 | **No deletion on an erasure request without the owner's order** (6 Nov) | `analysis.erasure.mode: record` by default; deletion only with `erasures apply` |
| — | **"I don't want to delete anything"** on the server (2 Nov) | No clean-up of images or build cache without the owner. The pilot disk was 88% full on 2 Nov, mostly Redpanda (9 GB) and Postgres (2.4 GB, growing about 1 GB a day). |

**Flagged for the owner's lawyer** (not decided; from the same document):
- outside AI companies as processors;
- keeping private people's comments forever;
- opinions about politicians and religion linked to a pseudonymous author (special-category data under GDPR);
- bot scores as automated profiling;
- legal flags as allegations.
