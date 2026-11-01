# ADR 0007: A self-growing topic list, ad and breach flags in the same call, behaviour-based bot detection

Status: accepted (2026-11-01). Design: [docs/design/analysis-service.md](../design/analysis-service.md) sections 6.1-6.3.

## Context

The owner asked for four additions:
- legal and policy breach detection;
- topics without the limit of three, growing when content is about something the list lacks, including people, positions and companies, without duplicates;
- ad detection on posts;
- bot accounts flagged, with every post or comment of a flagged account flagged too.

Constraints:
- **Cost:** the analysis was just made cheaper. Output tokens are ~75% of a comment's cost, so every new field must cost nothing when it does not apply.
- **Consistency:** five-year trends need stable topic ids and versioned changes.
- **Privacy:** authors are pseudonymous HMACs, and there is no profile data.
- **Speech:** Iraqi law has been used against critics of the government and against LGBT content.

## Decision

1. **Ads and breaches are flags in the same call.** They join the existing `flags` list (usually `[]`), so they add no output tokens when absent:
   - ad types: `ad_own`, `ad_paid`, `ad_hidden`, `ad_sale`;
   - 15 breach categories, stored as `ad_type` and `legal[]`.
   - **Breaches are leads for human review.** Criticism of the government, officials, institutions, companies or religion, political opinions, insults, crime reporting and discussion of sexuality or identity are never breaches.
2. **The topic list moves to the database and grows:**
   - **Proposals:** the model proposes missing themes (`new_topics`).
   - **Deduplication:** proposals are deduplicated at once against every known name, English and Arabic, with a word-order-, clitic- and plural-insensitive key and against the entity registry. New ones are kept as candidates on the items.
   - **Daily curation:** a Sonnet 5.5 call merges synonyms, rejects names and approves candidates used by ≥10 items.
   - **Versioning:** each change is a taxonomy version. Merges re-point stored topic arrays and publish `TopicUpdate`.
   - **Limits:** up to 10 model topics per item.
3. **People, positions, companies and places are topics through the entity registry:** `@<entity id>` is derived from linked entities, not from free text. Duplicates across spellings are already solved there (آسياسيل = Asiacell). Positions are a new entity type (an office independent of its holder), and 14 Iraqi offices are seeded.
4. **Bot detection is behavioural, daily, explainable:**
   - **Score:** a rule-based score over 30 days from eight signals, with stored reasons and two levels (suspected ≥45, likely ≥70).
   - **Formulaic texts** never count as repetition or coordination.
   - **Owner override:** `confirmed` or `human`.
   - **Flags on items:** every item takes its author's current level through the views; results carry the level at analysis time; `AuthorFlag` events announce changes; rollups count bot items.

## Alternatives considered

- **Free-text topics with no list:** they duplicate without end ("الكهرباء", "electricity cuts", "power outage"), which breaks trends. Rejected.
- **Embedding similarity for topic deduplication:** better recall on synonyms, but it needs an embeddings service (not built yet, ADR 0006). The daily LLM curation covers synonyms at negligible cost. Revisit with embeddings.
- **Asking the model whether an author is a bot:** a single comment does not show it. Rejected.
- **A machine-learned bot classifier:** there are no labels yet. The rules are explainable and tunable; the owner's overrides become labels for a later model.
- **A separate call or model for breaches and ads:** doubles the cost for fields that are absent in most items. Rejected.

## Consequences

- **Cost:** +5-7% (about $3.9k / $9.7k / $27.8k a year at 100M / 300M / 1B items), mostly the empty `new_topics` list.
- **The topic list changes over time:**
  - analyses record the version, and downstream stores follow `analysis.topic.v1`;
  - the prompt cache breaks at most once per change;
  - a batch answer that names a topic merged meanwhile loses that topic (rare: changes are daily).
- **False flags have a cost:** bot flags (super-fans, giveaway hunters) and breach flags (sarcasm, quotes) both produce false positives. Both are measured or reviewed before they are shown to clients:
  - the labeller labels ads and breaches;
  - the owner reviews a sample of flagged authors.
- **Bot scoring scans 30 days of items daily in Postgres:** minutes at pilot scale. It moves to ClickHouse at hundreds of millions of items a year.
- **Flagged for the lawyer:** bot scores are profiling, and breach flags are allegations.
