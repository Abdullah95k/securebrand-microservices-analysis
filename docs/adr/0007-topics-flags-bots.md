# ADR 0007: A self-growing topic list, ad and breach flags in the same call, behaviour-based bot detection

Status: accepted (2026-11-01; revised the same day after two owner reviews: wider breach categories, affiliate marketing, sub-topics and topic search, network detection, ambiguous aliases; then insulting religion always flagged, automatic approval of topics and entities, a log of bot corrections, reference labels from the strongest model). Design: [docs/design/analysis-service.md](../design/analysis-service.md) sections 6.1-6.3.

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
   - ad types: `ad_affiliate`, `ad_own`, `ad_paid`, `ad_hidden`, `ad_sale`;
   - 21 breach categories (hate speech and threats come from the existing signals), stored as `ad_type` and `legal[]`.
   - **Breaches are leads for human review.** They cover what is illegal in Iraq and what causes social harm, including hate speech, misinformation, and defamation of anyone, officials included.
   - **Criticism, opinions and satire** are flagged only when they also do one of those things.
   - **Religion (`religious_offense`), always flagged, on the owner's instruction:** insulting, mocking or attacking a religion, sect, sacred text, symbol or religious figure. Iraqi law punishes this.
   - **Not flagged on their own:**
     - a respectful religious question, disagreement, or a person stating their own belief or doubt;
     - criticism of a cleric's political actions (that is politics, and many politicians are clerics);
     - peaceful political opinion;
     - a person's sexuality or gender identity, even where Iraqi law criminalises them.

     They are speech and identity, not harm to others, and flagging them would turn the product into a tool against its own commenters.
2. **The topic list moves to the database, nests three levels, and grows:**
   - **Ids across languages:** items store topic ids, so a topic works the same in every language. Sub-topic ids start with their parent's, so asking for a topic fetches its sub-topics.
   - **Search:** `topics find` turns words in any language into ids, using trigram similarity over all names and aliases.
   - **Proposals:** the model proposes missing themes (`new_topics`).
   - **Deduplication:** proposals are deduplicated at once against every known name, English and Arabic, with a word-order-, clitic- and plural-insensitive key and against the entity registry. New ones are kept as candidates on the items.
   - **Automatic curation, every 6 hours:** a Sonnet 5.5 call merges synonyms, rejects names and approves candidates used by ≥5 items. The owner asked for approval by AI, with people stepping in only when needed (`topics` commands).
   - **Versioning:** each change is a taxonomy version. Merges re-point stored topic arrays and publish `TopicUpdate`.
   - **Limits:** up to 10 model topics per item.
3. **People, positions, companies and places are topics through the entity registry:** `@<entity id>` is derived from linked entities, not from free text. Duplicates across spellings are already solved there (آسياسيل = Asiacell). Positions are a new entity type (an office independent of its holder), and 14 Iraqi offices are seeded.
   - **The registry grows by itself:** every 6 hours a Sonnet 5.5 call decides on names seen 5+ times. It adds them, folds them into a known entity as a spelling, or rejects them (common words, private people). It sees two masked examples and the three most similar known entities for each name.
   - **Each decision is recorded** with the deciding model, and people can reverse it (`entities` commands).
   - **Private people are never added:** when in doubt about whether someone is a public figure, the model rejects them.
4. **Bot detection is behavioural, daily, explainable:**
   - **Score:** a rule-based score over 30 days from nine signals, with stored reasons and two levels (suspected ≥45, likely ≥70).
   - **Who is scored:** authors with 5+ items, and smaller accounts (2+ items) that took part in coordination, meaning the same long text as 4+ other accounts on the same day. This catches networks of accounts that each post a little.
   - **Formulaic texts** never count as repetition or coordination.
   - **Corrections, with a log:** an authorised person marks an account `human` (not a bot) or `bot`. Every decision is recorded with who, when, why and the level before and after (`author_overrides`). The corrections become labels for a later model.
   - **Flags on items:** every item takes its author's current level through the views; results carry the level at analysis time; `AuthorFlag` events announce changes; rollups count bot items.

5. **Generic names are ambiguous aliases.** Bare titles and ministry names ("رئيس الوزراء", "وزارة الصحة") are hints the model resolves from context. They never link by name and never make an item urgent; qualified forms link directly.

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
  - a batch answer that names a topic merged meanwhile loses that topic (rare: changes come at most every 6 hours).
- **False flags have a cost:** bot flags (super-fans, giveaway hunters) and breach flags (sarcasm, quotes) both produce false positives. Both are measured or reviewed before they are shown to clients:
  - **Reference labels:** there is no human labeller (owner's decision). Opus 5.5 labels the evaluation sample (`sb-eval silver`), and the production model is measured against it, including ads and breaches.
  - **Agreement, not accuracy:** this measures agreement with a stronger model, not accuracy. Human labels can still be added later.
  - **Bot flags are shown at once,** and corrections go to the log.
- **Bot scoring scans 30 days of items daily in Postgres:** minutes at pilot scale. It moves to ClickHouse at hundreds of millions of items a year.
- **Flagged for the lawyer:** bot scores are profiling and breach flags are allegations. Religious-offense flags are the most sensitive: if they ever leave the company, the people flagged could face prosecution.
