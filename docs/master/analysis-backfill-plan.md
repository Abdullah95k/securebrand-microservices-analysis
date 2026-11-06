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
- **Until the platform's writer has filled ClickHouse,** a backfill reads nothing, says so in its log and its result, and exits cleanly.
- **Media is text-only for history:** collectors delete files after every consumer has acknowledged them, or at their deadline. A backfill analyses the text of posts and comments, and the metadata of their media, but not old images or videos.

## How it works

**1. A command, run as a one-off Kubernetes Job from the chart (`backfill.enabled`, its arguments in values):**

```bash
sb-analysis backfill --from 2026-10-01 --to 2026-11-01 --account fb:page:123 --kinds post,comment --max-usd 20 --dry-run
```

- **Filters:** a time range (required), accounts, platforms, kinds, and "not analysed with the current model and prompt version".
- **`--dry-run`:** counts the items and estimates the cost at batch prices, without analysing anything. `--max-usd` refuses a run whose estimate is higher.

**2. Reading:**
- **Pages:** in order of creation time and content id, each page continuing from the last row of the one before (no offsets), a few thousand rows at a time.
- **Latest version:** only the latest version of each item. The platform's writer keeps one table per message type, each with a `<table>_latest` view, in database `securebrand` (the master, 6 Nov); the backfill reads the posts' and comments' `_latest` views.
- **Columns:** only those the analysis needs: ids, text, title and description, spans, language hints, account, post and parent ids, times, and media references.

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

## Questions for the master

1. **Columns:** the column names of the posts', comments' and media tables (their `_latest` views). The master answered the rest of this question: one table per message type, a `<table>_latest` view each, and analysis results in `item_analysis`, all in database `securebrand`.
2. **Sort key:** what are those tables ordered and partitioned by? A backfill reads by time range and account; reading in the tables' own order keeps it cheap for ClickHouse.
3. **The writer:** when will the platform's writer fill ClickHouse? Until then a backfill reads nothing.
4. **Limits:** may a backfill read at, say, 5,000 rows a second from the read-only user, or should it go slower?

## For the owner

Approving this plan approves building it after REQ-022. A backfill run itself spends AI budget. Each run shows its dry-run estimate first and stays within the daily cap, so the owner (or the master, by the owner's rule) starts each one on purpose.
