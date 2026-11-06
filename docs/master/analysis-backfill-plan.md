# sb-analysis: the backfill path (REQ-011 fix 9, plan only)

*For the owner and the master session, 6 November 2026. Nothing here is built yet; this is the proposal to approve. The master agreed the approach (6 Nov): ClickHouse through `clickhouse-ro`, never a collector's database.*

## The problem

sb-analysis analyses what reaches it through Kafka, and Kafka keeps 7 days. Anything older is never analysed. Three cases need older items:
- **A new model or prompt:** re-analyse a period with it (today `sb-analysis reanalyze` covers only items sb-analysis already holds).
- **A newly tracked account or brand:** analyse its history, not only what comes after.
- **A gap:** for example, ingest paused for longer than Kafka keeps (fix 6's backpressure alert warns long before).

## Where older items come from

**The platform's permanent store, ClickHouse, read-only.** Credentials come from the secret `clickhouse-ro` in the namespace `analysis`: `CLICKHOUSE_HOST`, `CLICKHOUSE_PORT` (9440, TLS), `CLICKHOUSE_DATABASE` (`securebrand`), `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD`. This follows the master's answer of 6 November.
- **Never a collector's database,** even when the permanent store is empty.
- **When rows appear:** the platform's writer starts with production's first deployment, but reads only the contract's topics. So each platform's rows appear once its collector publishes the contract; fbs's own `fb.*` topics never reach ClickHouse. Until then, a backfill for that platform reads nothing, says so in its log and its result, and exits cleanly.
- **Read limits:** the read-only user `analysis_ro` has 2 threads, 4 GB, a 10-minute query limit and 18 million rows an hour (about 5,000 a second on average; bursts are fine within the hour). The backfill paces itself in batches, and backs off on a quota error (code 201).
- **Media is text-only for history:** collectors delete files after every consumer has acknowledged them, or at their deadline. A backfill analyses the text of posts and comments, and the metadata of their media, but not old images or videos.

## How it works

**1. A command, run as a one-off Kubernetes Job from the chart (`backfill.enabled`, its arguments in values):**

```bash
sb-analysis backfill --from 2026-10-01 --to 2026-11-01 --account fb:page:123 --kinds post,comment --max-usd 20 --dry-run
```

- **Filters:** a time range (required), accounts, platforms, kinds, and "not analysed with the current model and prompt version".
- **`--dry-run`:** counts the items and estimates the cost at batch prices, without analysing anything. `--max-usd` refuses a run whose estimate is higher.

**2. Reading (the platform's layout, from the master, 6 November):**
- **Tables:** `posts`, `comments` and `media` in database `securebrand`. All are `ReplacingMergeTree`, partitioned by `toYYYYMM(observed_at)` and ordered by `(platform, id, version, event_id)`.
- **Not the `_latest` views:** they group the whole table, so a backfill never scans them.
- **By account:**
  - `item_accounts` is ordered by account: `WHERE account_id = ? AND relation = 'account' AND item_kind = 'post'`, with an `observed_at` range, gives the post ids cheaply;
  - then `posts` is read with `WHERE platform = ? AND id IN (…)`, which uses the sort key.
- **By time range:** filter on `observed_at` (it prunes partitions), then keep the newest version of each id, by the same rule as the views: the newest non-partial event for the content.
- **Comments of those posts:** today a scan, since `post_id` is not in the sort key. The master is asking for skip indexes (`comments.post_id`, `posts.account_id`, `media.owner_id`, and `published_at`); the plan counts on them.
- **What is read:** the base tables keep `event`, the whole event as protojson. Decoding it into the contract's types gives exactly what was published, so the backfill feeds the REQ-022 adapter the same messages Kafka would, and needs none of the flattened column names.
- **Pages:** a few thousand ids at a time, each page continuing from the last (no offsets).

**3. The same path as Kafka:**
- **Same adapter:** each row goes through the adapter REQ-022 builds for `content.post.v1` and `content.comment.v1`. Text is joined in the same order and spans converted in the same way. Phone numbers, e-mails and the names of tagged people never reach the AI provider (D-010).
- **Same storage:** items are stored by the same ingest code, so an item already held is updated rather than duplicated.

**4. Always the batch lane, at low priority:**
- **Lane:** backfilled items never take the real-time lane, whatever their account or entities. They are marked with the reason `backfill`.
- **Budget:** they count against the same daily budget ($5 in production), so a backfill never pushes the day's spend past the cap; it just takes more days.
- **Queue cap:** the backfill waits when the analysis queue is at its cap (fix 6), as live ingest does.

**5. Resumable:**
- **Progress:** every run is a row in a new table, `analysis.backfill_runs`: filters, the last row read, counts, estimated and actual cost, and state.
- **After a restart or failure:** it continues where it stopped.
- **`sb-analysis backfill status`** lists the runs.

**6. Results:**
- **Versions:** each result is a new `analysis_version` of its item, published like any other (on `analysis.result.v1`, in the contract's format after REQ-022).
- **Consumers:** they keep the highest version, so re-analysis never needs a delete. Every version is kept, as the owner decided.

## Order and size

- **After REQ-022:** ClickHouse holds contract-shaped rows, and the contract adapter is the one to reuse.
- **Effort:** about 2 to 3 days, including:
  - the command and the run table (one new migration);
  - the ClickHouse reader with its keyset pages;
  - the dry-run estimate;
  - the chart's Job;
  - integration tests against a ClickHouse container with synthetic rows.
- **Cost of a run:** this depends on the period and the accounts. The dry run gives the figure before anything is spent, and the daily budget limits the pace.

## The master's answers (6 November)

All four questions are answered above: the tables and how to read them by account or by time, the sort and partition keys, when rows appear (as each collector adopts the contract), and the read limits of `analysis_ro`. One thing is still open: the skip indexes for comments by post, which the master is asking the writer's session for.

## For the owner

Approving this plan approves building it after REQ-022. A backfill run itself spends AI budget. Each run shows its dry-run estimate first and stays within the daily cap, so the owner (or the master, by the owner's rule) starts each one on purpose.
