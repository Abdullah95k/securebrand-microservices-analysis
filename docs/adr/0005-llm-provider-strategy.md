# ADR 0005: LLM APIs per content type, batch by default, measured before chosen

Status: accepted (2026-11-01; cost revision the same day, item 9); the model per route is decided by the evaluation harness. Design: [docs/design/analysis-service.md](../design/analysis-service.md) sections 7 and 16.

## Context

- **Volume:** up to ~300M text items a year (95% short Iraqi-dialect comments) plus several million posts, images and videos.
- **Cost at list prices (2026-11-01)** for Claude Haiku 5.5 (Batch API), with realistic tokens and the compact answer format: ~$15 per million comments (~$21 with the first, verbose format). Claude Sonnet 5.5 costs ~20x that.
- **Output tokens are ~75% of a comment's cost,** so the answer format matters more than the input.
- **Self-hosting:** a GPU pair (~$44k/year before staff) breaks even only around 3B comments/year.
- **Model lifetimes:** providers retire models over a 5-year horizon, so the choice cannot be permanent.

## Decision

1. **APIs now.** The provider abstraction has three implementations:
   - Anthropic (official Go SDK; Message Batches; JSON-schema structured output; prompt caching);
   - OpenAI-compatible (OpenAI, Gemini's compatible endpoint, a self-hosted vLLM or Whisper server);
   - a deterministic fake for tests and the local stack.
2. **Routes by content type:** comment, post, image, video, plus escalation and audit. One call per group returns every field. Each route sets provider, model, lane, effort/thinking, fallback and a daily budget.
3. **Defaults until the evaluation says otherwise:**
   - Haiku 5.5 for comments, posts, images and videos, with **thinking off** (Haiku 5.5 thinks by default and bills thinking as output);
   - Sonnet 5.5 for escalations: tracked-brand items with low calibrated confidence;
   - Opus 5.5 on a 0.02% audit sample.
4. **Lanes:**
   - **batch** (50% off, ≤24h) is the default lane;
   - **real time** serves items that match a tracked brand's alias or come from a priority source, plus media and fallbacks.
5. **Grouping:** one post plus up to 40 of its comments per request. The static prefix is cached; the per-item entity hints come from the alias matcher.
6. **Reliability:**
   - a circuit breaker per provider;
   - a fallback provider per route for the real-time lane. The batch lane waits up to 12h for the primary before falling back, to avoid mixing models in bulk data;
   - refusals are isolated by splitting the group, then go to the fallback, then are stored as refused.
7. **Consistency:**
   - pinned model ids; provenance stored on every result;
   - switches gated on the gold set and a 20k overlap sample;
   - series breaks recorded in `model_changes`;
   - history re-analysed selectively when it matters (text is kept);
   - a monthly anchor-set re-run.
8. **No model is chosen without the harness.** It reports per-task accuracy with bootstrap intervals, measured cost from real token usage, latency and failure rates. The cheapest model within the interval of the best wins, per route.
9. **Pay only for what the model must decide** (cost revision):
   - a compact answer format: one language code, a list of flags, whole-number scores (~45 output tokens per comment instead of ~70);
   - the script and comment keywords computed from the text;
   - identical comments of a thread (same parent, same text) sent once, the answer shared;
   - speech-to-text on the cheaper of a per-minute API and an own Whisper server (above ~150M items a year), whichever passes the dialect test; its cost counts toward the budget.

## Consequences

- Lean mix: about $3.7k / $9.2k / $26k a year at 100M / 300M / 1B items (first estimate $5.0k / $15.1k / $50.2k). About $42k/year at 300M if posts and media need Sonnet. All are estimates until measured.
- Switching providers or models is configuration. The OpenAI-compatible provider is also the path to self-hosted or distilled models.
- Comments, with phones and e-mails masked but no other change, go to third-party processors. This is flagged for legal review (DPAs, retention terms, paid tiers only).
- Prices and model names must be re-checked before each budget decision. `configs/default.yaml` holds the price table with its date.
