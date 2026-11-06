# sb-analysis: adopting contract v1.0.0 (REQ-022)

*The record of REQ-022, kept current for the master session and the owner. Times are Iraq time. Started 6 November 2026.*

- **The contract:** the Go module `github.com/abdullah95k/securebrand-contracts`, tag **v1.0.0**, pinned in `go.mod`, as REQ-022 says. The master asks for v1.2.0 (question 1 below); that waits for the owner.
- **Production stays off** (`values-production.yaml`, replicas 0) until every part below is merged.

## Progress

| Part | What | State |
|---|---|---|
| A | Contract in: posts and comments (`content.post.v1`, `content.comment.v1`) | **Built** (step 1, this pull request) |
| B | Contract out: `securebrand.analysis.v1` from the contract; delete our own protos | **Built** (step 1) |
| C | The media hand-off (`content.media.ready.v1`, `.processed.v1`, `.deleted.v1`) | Next |
| D | Bots keyed by the author's account ID; hidden authors left out | Mostly built in step 1: scoring is keyed by `author.account.id`, hidden authors are left out, and `AuthorFlag` carries `author_id`. Still to do: the owner's `authors` commands take and show the account ID (today a hex key). |
| E | Captions used as the transcript (`transcript_source`) | To do |
| F | The full end-to-end test: every kind produced, a deletion and an estimate | Step 1 covers posts, comments, replies and a deletion; the rest comes with C and E |

## Step 1: contract in and out

### What it reads

`analysis.inputs: [content]` now means the platform's contract. Before, it meant this repository's draft, which no collector ever published.

- **Posts** (`content.post.v1`):
  - the title, text and description are joined in that order, with a blank line between them;
  - the account's display name (or handle) from `Post.account` is the source's name, used for per-entity sentiment;
  - the author is `Post.author`, or the account itself when there is none.
- **Comments and replies** (`content.comment.v1`): a reply is any comment with depth 1 or more, and keeps `parent_comment_id`. The author is `author.account.id`.
- **Hidden authors** (`author.hidden`): no author ID and no author key, so they are never scored as bots and never published as an author.
- **Mention spans:** converted from their own unit (UTF-16, code points or bytes) to positions in the joined text. Mentioned names are masked before text leaves, as before (D-010). A span that doesn't fit its text is dropped, never guessed.
- **Partial events** (`envelope.partial`) are never edits:
  - a partial event saying the item is no longer visible marks it deleted; the stored text and every analysis stay (the owner keeps all data);
  - any other partial event (counts, one field) is ignored.
- **The version:** `envelope.version` is stored with the item (`content_version`). An edit never lowers it.
- **A record without `envelope.id`** is rejected and logged.

### What it publishes

The contract's `securebrand.analysis.v1`, plain protobuf, with the headers `sb-type` (the message name) and `sb-contract` (`1.0.0`).

| Topic | Message | Key | Published for |
|---|---|---|---|
| `analysis.result.v1` | `ItemAnalysis` | content ID | items read from the contract |
| `analysis.media.v1` | `MediaAnalysis` | media ID | media announced through the contract (part C) |
| `analysis.author.v1` | `AuthorFlag` | the author's account ID | authors known by a contract account ID |
| `analysis.topic.v1` | `TopicUpdate` | topic ID | every change to the topic list |

- **`ItemAnalysis` carries:**
  - `content_version`, the analysed item's `envelope.version`;
  - the contract's IDs: `account_id`, `post_id`, `parent_comment_id` (replies), `author_id`;
  - `routing`: `urgent_source` (a hot account), `tracked_entity` (names an entity a client tracks), or `normal`, with `matched_entity_ids`;
  - the provenance: `producer` (`sb-analysis`), `producer_version`, the model, prompt, route, lane and the input's hash.
- **What sb-analysis doesn't do:** it creates no topic and registers no Schema Registry subject. The platform does both.

### fbs's own `fb.*` topics

Results of items read from `fb.*` (`analysis.inputs: [fb]`) stay in Postgres. They have no contract IDs and no envelope version, so they would fail the contract's checks. fbs's media acks still go to `fb.media.processed.v1`.

This is also what the proposed "early-running switch" needed. **Whether production runs on `fb.*` before REQ-022 is finished is still the owner's decision**; production stays at 0 replicas until then.

### Deleted (REQ-022 asked for it)

- This repository's draft `securebrand.content.v1` and its copy of `securebrand.analysis.v1`: `api/proto/`, `gen/go/`, `buf.yaml`, `buf.gen.yaml`.
- The draft topics `content.item.v1` and `content.erasure.v1` from the topic list for local and test brokers (`configs/topics.yaml`). No shared Kafka ever had them from this service, and nothing produced them.
- CI's `buf` steps and `make generate`. CI now fails if protos of our own come back.

### Changed (for the owner: nothing anyone consumes)

- **The `analysis.*` messages are now the contract's.** The draft's are wire-incompatible with them (for example, field 3 was an enum and is now a string). Nobody consumed the draft's, and production never published any.
- **Headers:** `sb-type` and `sb-contract` on the contract's topics, in place of `fbs-type`. fbs's topics keep `fbs-type`.
- **Migration `00004`** adds three columns to `analysis.content_items`: `content_version`, `depth`, `author_id`. Nothing removed.

### Evidence

- **`TestContractToAnalysisEndToEnd`** (Postgres and Redpanda; runs in CI's integration job). A fake platform publishes a post, a comment naming a tracked brand, a reply and a comment by a hidden author, with the contract's headers. Then:
  - **every `analysis.result.v1` record passes `conformance.ItemAnalysis` with `Options{Production: true}`**, carries `sb-type` and `sb-contract: 1.0.0`, is keyed by its content ID, and has the item's `content_version`;
  - the comment's result has its post, account and author IDs, `routing: tracked_entity` and `matched_entity_ids: [asiacell]`;
  - the reply has `depth: 1` and `parent_comment_id`;
  - the hidden author's comment has no `author_id`, and no author key is stored;
  - a partial update changes nothing; a partial deletion marks the comment deleted and keeps its text and analysis.
- **`TestFacebookToAnalysisEndToEnd`:** fbs's items are analysed and stored; nothing of theirs is queued for `analysis.result.v1`.
- **`TestContractSamples`:** the contract's own samples (`post.tt.json`, `comment.yt.json`, `comment.fb.deleted.json`) decode and map correctly.
- **Span units, partial events and hidden authors:** `TestJoinTextsAndSpanUnits`, `TestContractPartialAndHiddenAuthor`.
- **A bug the end-to-end test caught:** a partial deletion has no publish time, and ingest dropped items without one. It now uses the observation time; the stored item's own time is used when it is marked.
- All unit tests with race detection, all integration tests, and golangci-lint pass.

### Questions for the master

1. **Conformance for the other kinds:** v1.0.0's `conformance` package checks `ItemAnalysis` but not `MediaAnalysis`, `AuthorFlag`, `TopicUpdate` or `MediaProcessed`.
   - **The master's answer (6 Nov):** contract **v1.2.0** (commit `30153b4`) adds those checks, plus `MediaDeleted`, with a sample each. The master asks to pin v1.2.0 instead of v1.0.0.
   - **Checked here:** the analysis, content and media protos are identical in v1.0.0 and v1.2.0. The only proto change is two additions to discovery's `Candidate`, which sb-analysis doesn't use.
   - **Waiting for the owner:** REQ-022 names v1.0.0, so the switch is the owner's decision. Recommended: yes. If agreed, it's a small pull request of its own: the new pin, plus the five checks in CI.
