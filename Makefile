SHELL := /bin/bash
GO ?= go
PG_DSN ?= postgres://analysis:analysis@localhost:5432/analysis?sslmode=disable
TEST_ENV = SB_TEST_PG_DSN="$(PG_DSN)" SB_TEST_REDIS_ADDR=localhost:6379 SB_TEST_KAFKA_BROKERS=localhost:19092
DEV_COMPOSE = docker compose -f deploy/dev/compose.yml
ANALYSIS_IMAGE ?= sb-analysis:dev
MEDIA_IMAGE ?= sb-analysis-media:dev

.PHONY: all build test vet lint infra-up infra-down test-integration docker docker-media docker-prebuilt \
	analysis-image docker-analysis-media analysis-stats analysis-up analysis-down analysis-cmd analysis-status \
	eval-sample eval-silver eval-label eval-agreement eval-run eval-report eval-cost clean

all: build test

build:
	$(GO) build ./...

## Unit tests (no infrastructure). Integration tests skip themselves.
test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

# A lint failure fails the target (`a && b || c` ran c, and passed, when b failed).
lint: vet
	@if command -v golangci-lint >/dev/null; then golangci-lint run ./...; else echo "golangci-lint not installed; ran go vet only"; fi

## Start Postgres, Redpanda and Valkey for the integration tests: a Compose
## project of its own (sb-analysis-dev), never an fbs stack.
infra-up:
	$(DEV_COMPOSE) up -d --wait postgres redpanda valkey

## Stop them and delete their data (only sb-analysis-dev's).
infra-down:
	$(DEV_COMPOSE) down -v

## Integration tests against real Postgres/Redpanda/Valkey (make infra-up first).
test-integration:
	$(TEST_ENV) $(GO) test -count=1 -timeout 25m ./...

## The images: sb-analysis (sb-analysis and sb-eval) and the media image (with ffmpeg).
docker analysis-image:
	docker build -f deploy/docker/Dockerfile --target sb-analysis -t $(ANALYSIS_IMAGE) .
docker-media docker-analysis-media:
	docker build -f deploy/docker/Dockerfile --target analysis-media -t $(MEDIA_IMAGE) .

## The image from binaries built here (when the Go builder image cannot be pulled).
docker-prebuilt:
	rm -rf bin && mkdir -p bin
	CGO_ENABLED=0 GOOS=linux $(GO) build -trimpath -ldflags="-s -w" -o bin/ ./cmd/sb-analysis ./cmd/sb-eval
	docker build -f deploy/docker/Dockerfile.prebuilt -t $(ANALYSIS_IMAGE) .

## Content analysis next to the fbs pilot, as a Compose project of its own
## (deploy/analysis/compose.yml): its own image and containers on the
## pilot's network. fbs's pilot-up and restarts never touch it, and nothing
## here touches fbs's containers, image, topics or settings
## (docs/owner/analysis-decisions.md).
ANALYSIS_ENV = deploy/analysis/analysis.env
FBS_NETWORK ?= fbs_default
FBS_POSTGRES ?= fbs-postgres-1
ANALYSIS_COMPOSE = SB_ANALYSIS_IMAGE=$(ANALYSIS_IMAGE) FBS_NETWORK=$(FBS_NETWORK) docker compose -f deploy/analysis/compose.yml
ANALYSIS_PSQL = docker exec -i $(FBS_POSTGRES) psql -U fbs -d fbs
NEED_FBS = @docker network inspect $(FBS_NETWORK) >/dev/null 2>&1 || { echo "no $(FBS_NETWORK) network: the fbs pilot must be running on this machine"; exit 2; }
NEED_KEYS = @test -f $(ANALYSIS_ENV) || { echo "missing $(ANALYSIS_ENV) (the API keys): docs/owner/analysis-decisions.md, step 5"; exit 2; }; \
	test "$$(stat -c %a $(ANALYSIS_ENV))" = 600 || { echo "$(ANALYSIS_ENV) must be mode 600: chmod 600 $(ANALYSIS_ENV)"; exit 2; }

## Shape of the collected data (counts and lengths only, no content).
analysis-stats:
	$(ANALYSIS_PSQL) -f - < scripts/analysis/pilot-stats.sql

## Starts the analysis (sends comments to the LLM provider: only after the
## owner's decision, docs/owner/analysis-decisions.md).
analysis-up:
	$(NEED_FBS)
	$(NEED_KEYS)
	$(ANALYSIS_COMPOSE) run --rm kafka-topics
	$(ANALYSIS_COMPOSE) up -d --force-recreate analysis
	@echo "analysis running; follow it with: make analysis-status"

analysis-down:
	$(ANALYSIS_COMPOSE) stop analysis

## Owner commands, e.g.
##   make analysis-cmd ARGS="authors list"
##   make analysis-cmd ARGS='topics find "electricity cuts"'
analysis-cmd:
	$(NEED_FBS)
	$(ANALYSIS_COMPOSE) run --rm --no-deps -T --entrypoint /usr/local/bin/sb-analysis analysis $(ARGS)

## Analysis progress and spend (counts only).
analysis-status:
	@$(ANALYSIS_PSQL) -c "SELECT state, count(*) FROM analysis.content_index GROUP BY state ORDER BY 1" \
	  -c "SELECT route, lane, count(*) AS queued FROM analysis.analysis_queue GROUP BY 1, 2" \
	  -c "SELECT (created_at AT TIME ZONE 'Asia/Baghdad')::date AS day_iraq, model, count(*) AS requests, sum(items) AS items, \
	      round(sum(coalesce(cost_usd, est_cost_usd))::numeric, 2) AS usd FROM analysis.provider_requests GROUP BY 1, 2 ORDER BY 1 DESC, 2 LIMIT 20"

## Evaluation (docs/analysis/evaluation.md). The gold set stays in GOLD_DIR,
## outside the repository; the tools run from the analysis image (no Go needed).
GOLD_DIR ?= /srv/securebrand/gold
MODEL ?= claude-haiku-5-5
PROVIDER ?= anthropic
EVAL_ARGS ?=
EVAL = docker run --rm -v $(GOLD_DIR):/gold --entrypoint /usr/local/bin/sb-eval
eval-sample:
	@test -d $(GOLD_DIR) || { echo "create it first: mkdir -p $(GOLD_DIR) && chown 65532:65532 $(GOLD_DIR) && chmod 700 $(GOLD_DIR)"; exit 2; }
	$(NEED_FBS)
	$(EVAL) --network $(FBS_NETWORK) -e SB_ANALYSIS_POSTGRES__DSN=postgres://fbs:fbs@postgres:5432/fbs?sslmode=disable $(ANALYSIS_IMAGE) \
	  sample --out /gold/gold.labels.jsonl $(EVAL_ARGS)
## Reference labels from the strongest model (no human labeller): Opus
## 5.5 labels the sample, the production model is measured against it.
eval-silver:
	$(NEED_KEYS)
	$(EVAL) --env-file $(ANALYSIS_ENV) $(ANALYSIS_IMAGE) silver --gold /gold/gold.labels.jsonl $(EVAL_ARGS)
eval-label:
	@echo "Open http://127.0.0.1:8099 through an SSH tunnel (docs/analysis/evaluation.md). Ctrl+C to stop."
	$(EVAL) --network host $(ANALYSIS_IMAGE) label --file /gold/gold.labels.jsonl
eval-agreement:
	$(EVAL) $(ANALYSIS_IMAGE) agreement --file /gold/gold.labels.jsonl
eval-run:
	$(NEED_KEYS)
	$(EVAL) --env-file $(ANALYSIS_ENV) $(ANALYSIS_IMAGE) run --gold /gold/gold.labels.jsonl --out /gold/results.jsonl \
	  --provider $(PROVIDER) --model $(MODEL) $(EVAL_ARGS)
eval-report:
	$(EVAL) $(ANALYSIS_IMAGE) report --gold /gold/gold.labels.jsonl /gold/results.jsonl
eval-cost:
	$(EVAL) $(ANALYSIS_IMAGE) cost --measured /gold/results.jsonl --model $(MODEL)

clean:
	rm -rf bin/
