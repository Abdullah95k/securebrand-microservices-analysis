# sb-analysis: fixes (REQ-011)

*One row per fix the owner approved on 6 November 2026 (REQ-011), in build order. Kept current for the master session and the owner. Times are Iraq time.*

| # | Fix | PR | Status | Evidence |
|---|---|---|---|---|
| 1 | A repository of its own (Part D) | [#3](https://github.com/Abdullah95k/securebrand-microservices-analysis/pull/3) | In review | See [Fix 1](#fix-1-a-repository-of-its-own) |
| 2 | Its own packaging: Helm chart, images on a tag, CI, metric prefix `sb_analysis_` (A1, A3) | – | Not started; the metric prefix is done in fix 1 | – |
| 3 | No unlimited budget by accident (C7) | – | Not started | – |
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
