# ADR 0004: One platform-neutral analysis service, a canonical content contract, and a Postgres work queue

Status: accepted (2026-11-01). Design: [docs/design/analysis-service.md](../design/analysis-service.md).

## Context

The collectors stay platform-specific. Today only the Facebook collector (fbs) exists. Instagram, TikTok, X, Telegram and YouTube will follow, each with its own normalisation service.

SecureBrand needs one analysis of posts, comments, images and videos, comparable across platforms:
- language;
- sentiment, overall and per entity;
- topics and entities;
- intent, emotion, toxicity and spam.

Analysis runs on LLM APIs. Batch APIs halve the price but answer in minutes to hours.

## Decision

1. **One service, `sb-analysis`.**
   - **Input:** a canonical contract, `securebrand.content.v1` (`content.item.v1`, `content.erasure.v1`).
   - **Facebook today:** an in-process adapter reads `fb.post.v1`, `fb.comment.v1`, `fb.media.ready.v1` and `fb.erasure.v1`, so the service runs before any normaliser exists.
   - **Platform differences become routing config:** text length, media type and context. Each route chooses model, lane and fallback.
   - **Roles** (ingest, pipeline, media, erasure, relay, rollup) are flags of one binary, deployable separately.
2. **Output:** `securebrand.analysis.v1` on `analysis.result.v1` and `analysis.media.v1`. Every record carries provenance: provider, model, prompt, taxonomy and schema versions, analysis version, and purpose.
3. **Postgres is the work queue**, not Kafka offsets:
   - ingest writes items and commits offsets at once;
   - workers claim queue rows with `FOR UPDATE SKIP LOCKED` leases;
   - results, the queue update and an outbox row commit in one transaction;
   - a relay publishes the outbox to Kafka.
4. **The service computes its own idempotency key:** `text_hash` = SHA-256 of normalised text, kind and media ids.
   - An unchanged re-scrape does nothing; an edit re-queues the item.
   - A result is written only if its input hash still matches the item.

## Consequences

- One prompt, model and taxonomy per route across platforms, so cross-platform comparisons are meaningful. A platform that needs special handling gets a route, not a service.
- Batch results can arrive hours later without blocking Kafka partitions. Restarts never lose or double-count work: leases expire, and results are idempotent.
- Budget pauses, retries, dead letters and selective re-analysis are rows in Postgres, visible and replayable.
- Postgres handles a little more write load: one queue row and one outbox row per item, ~10 rows/s at target volume.
- When a Facebook normaliser exists, `analysis.inputs` switches from `fb` to `content` without code changes.
