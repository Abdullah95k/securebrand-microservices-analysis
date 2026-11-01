# ADR 0006: Postgres as system of record with daily rollups now; ClickHouse for the analytics agent later

Status: accepted (2026-11-01). Design: [docs/design/analysis-service.md](../design/analysis-service.md) section 12.

## Context

- **Retention:** text and its analysis are kept indefinitely (owner's decision). That is ~300M items/year, ~1.5B in five years.
- **Queries:** brand monitoring asks group-by questions: sentiment over time per brand, topic, page and platform.
- **Future consumer:** a chat agent with read-only SQL, text search and example retrieval will answer questions such as "most common topics when people mention brand X in the last year".
- **Scale now:** the pilot produces ~46M items/year.

## Decision

1. **Postgres is the system of record**, with its own migrations and DSN so it can move to a separate database. It holds:
   - `content_items`, `analyses` and `entity_mentions`, partitioned monthly on the item's creation time;
   - `content_index`, hash-partitioned, for idempotency lookups;
   - the registry, the queue and the outbox.
2. **`entity_mentions` is denormalised for analytics.** Each row carries per-entity sentiment and aspect, plus the item's topics, platform, source, kind, language, intent and Iraq-time day. "Topics when people mention X" therefore needs no join.
3. **Daily rollups** (entity, entity × topic, topic, source) are recomputed per dirty day. That keeps them correct under re-analysis, registry merges and erasure.
4. **Agent access:**
   - **schema:** an `agent` schema of views;
   - **scoping:** one Postgres login role per client, mapped in `client_roles`, and security-barrier views filtered on `current_user`. A client sees mentions of its tracked entities anywhere, plus everything from its monitored sources;
   - **limits:** `default_transaction_read_only`, `statement_timeout`, `temp_file_limit`, `work_mem`, a connection limit. The agent's SQL tool must additionally accept one `SELECT`, cap rows, and enforce its own deadline.
5. **Full-text search:** an expression GIN index on `to_tsvector('simple', analysis.ar_normalize(text))`.
6. **ClickHouse is the analytics store for the agent.** It is added before the agent goes live, or past ~200M mentions, fed from `analysis.result.v1` (Kafka engine + materialized views, `ReplacingMergeTree(analysis_version)`). The agent queries only ClickHouse. Its settings profiles, constraints, quotas and row policies enforce limits and scoping server-side.
7. **Embeddings** are a costed option, not built. API cost is ~$320/year at 300M items (Gemini Embedding 2 batch); storage is the real cost (~18-70 GB/year). The model is chosen by a retrieval evaluation (recall@10 / nDCG on ~200 Arabic, dialect, Arabizi and English queries).

## Consequences

- No new infrastructure for the pilot. Dashboards read rollups in milliseconds.
- Ad-hoc scans over billions of rows are not served by Postgres. They wait for ClickHouse, which needs no service change (the Kafka topic is the interface).
- Storage grows ~330 GB/year in Postgres at 300M items, with no retention drops. Old partitions can move to cheaper storage.
- Erasure must reach every store: Postgres now, ClickHouse later, by consuming the same erasure topics.
