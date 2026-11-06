# sb-analysis: fixes (REQ-011)

*One row per fix the owner approved on 6 November 2026 (REQ-011), in build order. Kept current for the master session and the owner. Times are Iraq time.*

| # | Fix | PR | Status | Evidence |
|---|---|---|---|---|
| 1 | A repository of its own (Part D) | [#3](https://github.com/Abdullah95k/securebrand-microservices-analysis/pull/3) | **Done** (merged 6 Nov, `b080a72`) | See [Fix 1](#fix-1-a-repository-of-its-own) |
| 2 | Its own packaging: Helm chart, images on a tag, CI, metric prefix `sb_analysis_` (A1, A3) | [#4](https://github.com/Abdullah95k/securebrand-microservices-analysis/pull/4) | **Done** (merged 6 Nov, `22f8938`) | See [Fix 2](#fix-2-its-own-packaging) |
| 3 | No unlimited budget by accident (C7) | this repository's pull request for fix 3 | In review | See [Fix 3](#fix-3-no-unlimited-budget-by-accident) |
| 4 | Media memory: stream files to disk (B2) | – | Not started | – |
| 5 | The provider's rate limit, fleet-wide (B6) | – | Not started | – |
| 6 | Backpressure: cap the queue, pause ingest (C6) | – | Not started | – |
| 7 | The quality evaluation | – | Waiting: runs on the production cluster once fbs collects there (owner, 6 Nov) | – |
| 8 | The real-time lane: tracked brands and entities, hot-tier accounts | – | Not started; asked the master where account tiers come from | – |
| 9 | A backfill path (plan only) | – | Not started; asked the master which permanent store to read | – |

## The owner's answers (6 November)

- **Fix 7:** the evaluation runs on the production cluster, once fbs collects there (option b), not on the shared server.
- **Fix 3:** the canary on production gets a $5 daily cap on AI spending. The pilot keeps $5 a day (1 November).
- **Questions for the master:** [`questions-for-master-2026-11-06.md`](questions-for-master-2026-11-06.md) covers where the images and the chart go, the production namespace, account tiers, and the backfill store.
- **The master's answer on question 1 (6 Nov):**
  - the platform builds sb-analysis's images from a commit of this repository and keeps them in its own registry;
  - the chart needs a `values-production.yaml` following the platform's conventions;
  - the namespace is `analysis`, and credentials come from the secret `platform-credentials`, the AI key from `ai-provider` (`API_KEY`);
  - the Kafka user is `analysis-kafka`, allowed `content.*` and `analysis.*`, and the platform creates every topic;
  - the database is PostgreSQL 18, `analysis`;
  - the cap is $5 a day until the owner sets another figure.

- **The master's answers on the rest (6 Nov):**
  - **The platform's conventions:** they are in `infra/README.md` of the platform's private repository. The master sent the exact secret keys. Every name in `platform-credentials` matches the chart's mapping, so nothing is renamed; the CA certificates (`KAFKA_CA_CERT`, `POSTGRES_CA_CERT`) are mounted as files.
  - **Order:** in production, sb-analysis may read fbs's `fb.*` topics until REQ-022. Its Kafka user can also consume `fb.*` and `registry.*` and produce `fb.media.processed.v1`.
  - **Images:** the platform builds them to `registry.digitalocean.com/securebrand/analysis:<commit>`.
  - **Account tiers (fix 8):** from `registry.account_request.v1`. Until the registry publishes, a config list of hot accounts by id, empty by default.
  - **Backfill (fix 9):** from ClickHouse, through the secret `clickhouse-ro` (read-only). Until the platform's writer has filled it, a backfill reads nothing and logs that; never a collector's database.
  - **Budget:** $5 a day, as a hard stop.

## Fix 3: no unlimited budget by accident

- **New setting `analysis.budget.allow_unlimited`** (default `false`).
- **The service refuses to start** with a daily cap of 0 (meaning no cap) unless that setting is `true`.
  - This applies to every `service.env` except `dev`: production, the pilot, a staging setup, or no environment set. The owner asked for production; the other environments get the same protection against a missing setting.
  - Local development and the tests (`dev`) keep running without a cap.
  - The error names both ways out: set a cap, or set `allow_unlimited: true`.
- **The caps:**
  - production (`values-production.yaml`) and the chart's defaults: $5 a day;
  - the pilot (`deploy/analysis/pilot.yaml`): $5 a day, unchanged.
- **Still the owner's to decide:** whether urgent real-time items stop at the cap too (`urgent_exempt`, today `true`). The master asks for a hard stop.
- **Evidence:**
  - `TestBudgetNeverUnlimitedByAccident` covers 8 cases: production, the pilot, staging and no environment are refused without a cap and accepted with one or with `allow_unlimited`; dev is accepted.
  - `TestProductionBudgetFile` loads a production file with and without a cap.
  - With the check disabled on purpose, 5 cases fail.
  - Unit and integration tests and lint pass.

## Fix 2: its own packaging

- **The Helm chart** [`deploy/helm/sb-analysis`](../../deploy/helm/sb-analysis):
  - two Deployments, `analysis` (ingest, both lanes, relay, rollups) and `analysis-media` (on the ffmpeg image);
  - `sb-analysis migrate` as a hook before each install and upgrade;
  - a metrics Service, a ServiceAccount without a token, and a PodDisruptionBudget;
  - optionally a ServiceMonitor and the alert rules.
- **Credentials, never in values:**
  - each variable is read from the secret `platform-credentials`, key by key: `POSTGRES_*`, `KAFKA_*`, `S3_*`;
  - the AI key is read from `ai-provider` (`API_KEY`) for the Anthropic provider;
  - sb-analysis now accepts the database as parts (host, port, database, user, password, sslmode), so no URL has to be built around a password; SASL mechanisms in any case (`SCRAM-SHA-512`); and the S3 endpoint as a URL (`https://…` sets TLS);
  - tested with passwords holding quotes, spaces, backslashes, `@`, `:` and `/`.
- **[`values-production.yaml`](../../deploy/helm/sb-analysis/values-production.yaml):**
  - the namespace `analysis`, the platform's two secrets, and $5 a day;
  - **both workloads at 0 replicas until REQ-022.** Until then sb-analysis reads and writes its draft schemas, while the platform's `content.*` and `analysis.*` topics carry the contract's, and the draft media topic has the contract's name, `content.media.ready.v1`. Running now would misread the platform's media events and put draft records on the platform's topics.
  - The migrate hook still runs. CI fails if production renders a workload with replicas.
  - **Why the master's "read fbs's topics first" can't simply be switched on:** sb-analysis's results are in its draft format, which is not wire-compatible with the contract's `securebrand.analysis.v1` on the platform's `analysis.*` topics. The same field numbers carry different types: field 3 is text in the contract and a number code in the draft, and field 5 is a number in one and text in the other. Running early needs a switch that keeps results in Postgres and off those topics, while media acknowledgements still go to fbs (`fb.media.processed.v1`). That switch is not in REQ-011, so it is proposed to the owner first; the canary also waits for fixes 3 to 6.
- **The CA certificates** (`KAFKA_CA_CERT`, `POSTGRES_CA_CERT`) are mounted as files from the same secret. The config gains `postgres.sslrootcert`, so the database's certificate is verified too.
- **Images on a tag:** pushing `vX.Y.Z` publishes `ghcr.io/abdullah95k/sb-analysis:X.Y.Z`, `ghcr.io/abdullah95k/sb-analysis-media:X.Y.Z` and the chart (`oci://ghcr.io/abdullah95k/charts/sb-analysis`), after the vet and unit tests pass. Production uses the platform's own builds instead (the master, 6 Nov).
- **CI** checks the chart: lint with both values files, render, Kubernetes schemas (kubeconform), and the monitoring resources.
- **Alerts:** the analysis's 8 alert rules moved from fbs's chart into `files/alerts.yaml`, renamed to `sb_analysis_*`, with links to this repository's runbook. New tests fail when an alert names a metric sb-analysis does not expose, or links to a runbook heading that does not exist; both were checked by breaking each on purpose.
- **The metric prefix `sb_analysis_`** was done in fix 1.

## Fix 1: a repository of its own

**What changed:**
- **The Go module is `github.com/abdullah95k/securebrand-microservices-analysis`.** It used fbs's module name before.
- **fbs's code is gone from this repository:** its 11 programs, its packages, its Helm chart, its fake Facebook, its parser fixtures, its pilot and Phase 0 kit, its protos, and its documents. fbs's own repository keeps all of it, and this repository's history keeps it too.
- **sb-analysis depends on fbs's module** (`github.com/abdullah95k/securebrand-microservices`, at fbs's `main`, `f87d6ae`) for two public packages only:
  - the fb protos (`gen/go/securebrand/fb/v1`), read by the Facebook adapter;
  - fbs's migrations, which the evaluation sampler's test loads to check that sampling still works on fbs's real tables.
- **Its own packages:**
  - config: only the sections it uses;
  - topics: only the topics it reads and writes;
  - test helpers: an empty database per test, without fbs's schema;
  - the platform packages it uses (Kafka, Postgres, logging, metrics, ops server, S3, Valkey);
  - its own Kafka client helper.
- **Layout:** the packages moved up from `internal/analysis/*` to `internal/*`, and the migrations from `migrations/analysis` to `migrations`.
- **Its own names (no fbs prefix left):**
  - environment variables `SB_ANALYSIS_<SECTION>__<KEY>` and `SB_ANALYSIS_CONFIG` (were `FBS_…`);
  - test variables `SB_TEST_*`;
  - metrics `sb_analysis_*` (were `fbs_analysis_*`).

  Nothing ran in production yet, so no dashboard, alert or deployment depended on the old names. `deploy/analysis/compose.yml` and the documents are updated.
- **Its own build:**
  - Makefile, Dockerfile (`sb-analysis` and the media image `sb-analysis-media`), CI (test, integration, images);
  - a local test stack of its own (`deploy/dev/compose.yml`, Compose project `sb-analysis-dev`), so `make infra-up` can never touch an fbs stack.
- **A bug the split found, now fixed:** the migrations' version table was named without its schema. Postgres looks for such a table first in a schema named after the database user. With a user called `analysis`, which is likely in production, that is the `analysis` schema, which the first migration creates. A rollback, or a restart, then found no applied migrations. The table is now `public.analysis_goose_db_version`, the same table existing databases already use, and a new test (`TestVersionTableIgnoresUserSchema`) catches the bug. It failed without the fix.
- **Unchanged:**
  - no topic, event field, schema or endpoint that anyone uses was renamed or removed;
  - `analysis.*` and `content.*` keep their names, and the protos' wire format is unchanged (`buf breaking` passes).

  The protos' Go package paths changed with the module. They are not part of the wire format, nothing imports this repository's generated code, and `buf.yaml` exempts that one rule.

**Evidence (6 November, in this sandbox):**
- `go build ./...`, `go vet ./...`, and `golangci-lint` 2.15.0 (0 issues).
- `buf lint`, `buf generate` (generated code up to date), and `buf breaking` against `main` on the owned protos.
- Unit tests with `-race`.
- Integration tests against Postgres 18, Redpanda and Valkey, all passing. They include:
  - the end-to-end test from Facebook topics to `analysis.result.v1`, run against fbs's current fb protos;
  - the sampler on fbs's real migrations;
  - the version-table test.
- The `sb-analysis` image builds from local binaries, and `sb-analysis help` and `sb-eval` run in it.
- CI on the pull request runs the same checks, plus both image builds from source.
