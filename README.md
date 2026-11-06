# sb-analysis

SecureBrand's content analysis service. For every post and comment it finds:

- the language and dialect (Iraqi Arabic, Sorani, Kurmanji, English and others);
- sentiment, overall and per entity, with the aspect;
- the entities mentioned, linked to a registry that grows automatically;
- topics, from a hierarchical list that grows automatically;
- intent, emotion, toxicity, spam, sarcasm and urgency;
- ad types, and legal or policy flags for human review.

Images and videos get logos, text in the image, a description, entities and topics. Bot and coordinated-account detection runs on authors. Results go to Postgres (schema `analysis`, every version kept) and, for items that came through the platform's contract, to Kafka (`analysis.*`, the contract's `securebrand.analysis.v1`).

It reads two inputs, chosen by `analysis.inputs`:

- `content`: the platform's shared contract (`securebrand-contracts` v1.0.0): `content.post.v1` and `content.comment.v1`;
- `fb`: what fbs collects from Facebook (`fb.*` topics), through a Facebook adapter. These results stay in Postgres: they have no contract IDs, so they can't go on the contract's `analysis.*` topics.

fbs itself lives in its own repository, [`Abdullah95k/securebrand-microservices`](https://github.com/Abdullah95k/securebrand-microservices); this repository takes only fbs's public protos and migrations, as a Go module dependency.

## Documents

| Topic | Where |
|---|---|
| Design | [`docs/design/analysis-service.md`](docs/design/analysis-service.md) |
| Decisions (ADRs) | [`docs/adr/`](docs/adr/) |
| Operations | [`docs/runbooks/analysis.md`](docs/runbooks/analysis.md) |
| Quality evaluation | [`docs/analysis/evaluation.md`](docs/analysis/evaluation.md) |
| The owner's decisions and steps | [`docs/owner/analysis-decisions.md`](docs/owner/analysis-decisions.md) |
| Reports for the master session | [`docs/master/`](docs/master/) |
| Status | [`docs/status.md`](docs/status.md) |

## Development

Requires Go (the version in `go.mod`) and Docker.

```bash
make build
```

```bash
make test
```

Integration tests run against real Postgres, Redpanda and Valkey, in a local Compose project of their own (`sb-analysis-dev`):

```bash
make infra-up
```

```bash
make test-integration
```

```bash
make lint
```

The messages come from the platform's contract, the Go module `github.com/abdullah95k/securebrand-contracts` (pinned at v1.0.0); this repository has no protos of its own. fbs's `securebrand.fb.v1` comes from fbs's module.

## Configuration

Defaults are in [`configs/default.yaml`](configs/default.yaml). Override them with files (`--config a.yaml,b.yaml`, or `SB_ANALYSIS_CONFIG`) and with environment variables `SB_ANALYSIS_<SECTION>__<KEY>`, for example:

- `SB_ANALYSIS_KAFKA__BROKERS=broker:9092`
- `SB_ANALYSIS_POSTGRES__DSN=postgres://…`
- `SB_ANALYSIS_ANALYSIS__PROVIDERS__ANTHROPIC__API_KEY` (from a secret, never a committed file)

Metrics are on the ops port (`:9090/metrics`), all named `sb_analysis_*`.

## Images

```bash
make docker
```

```bash
make docker-media
```

`sb-analysis:dev` holds `sb-analysis` and `sb-eval`; `sb-analysis-media:dev` adds ffmpeg for the media workers. A tag `vX.Y.Z` publishes both to GHCR, with the Helm chart (`.github/workflows/release.yml`).

## Kubernetes

The Helm chart is [`deploy/helm/sb-analysis`](deploy/helm/sb-analysis):
- two Deployments, `analysis` and `analysis-media`;
- `sb-analysis migrate` as a hook before each install and upgrade;
- optionally a ServiceMonitor and the alert rules (`files/alerts.yaml`).

[`values-production.yaml`](deploy/helm/sb-analysis/values-production.yaml) is for the platform's cluster:
- credentials come key by key from the secret `platform-credentials`, and the AI key from `ai-provider`;
- the platform builds the images and sets `image.repository`, `image.tag`, `mediaImage.repository` and `mediaImage.tag`;
- the chart creates no topics and registers no schemas.

## Repository layout

```
cmd/sb-analysis      the service and its owner commands
cmd/sb-eval          the quality evaluation tool
internal/            adapter, pipeline, prompt, provider, store, media, bots, curate, taxonomy, entities, ...
internal/platform/   Kafka, Postgres, logging, metrics, ops server, S3
migrations           the analysis's SQL migrations (schema "analysis")
configs              default config, topics for local brokers, entity and taxonomy seeds
deploy/docker        the images
deploy/helm          the Helm chart (values-production.yaml for the platform's cluster)
deploy/dev           the local test stack
deploy/analysis      the Compose deployment next to the fbs pilot
docs                 design, ADRs, runbook, evaluation, reports
```

## Rules

- The repository is public: no scraped content, gold labels, API keys or credentials, ever. Examples are synthetic.
- Erasure is recorded, never applied, unless the owner orders it (`analysis.erasure.mode: record`).
- Phone numbers, e-mails and tagged people's names never reach an AI provider.
