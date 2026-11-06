# sb-analysis: adopting contract v1.0.0 (REQ-022)

*The record of REQ-022, kept current for the master session and the owner. Times are Iraq time. Started 6 November 2026.*

- **The contract:** the Go module `github.com/abdullah95k/securebrand-contracts`, tag **v1.0.0**, pinned in `go.mod`, as REQ-022 says. The master asks for v1.2.0 (question 1 below); that waits for the owner.
- **Production stays off** (`values-production.yaml`, replicas 0) until every part below is merged.

## Progress

| Part | What | State |
|---|---|---|
| A | Contract in: posts and comments (`content.post.v1`, `content.comment.v1`) | **Merged** (step 1, `cbf46fd`) |
| B | Contract out: `securebrand.analysis.v1` from the contract; delete our own protos | **Merged** (step 1, `cbf46fd`) |
| C | The media hand-off (`content.media.ready.v1`, `.processed.v1`, `.deleted.v1`) | **Built** (step 2, in review) |
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

## Step 2: the media hand-off

### What it reads

With `analysis.inputs: [content]`, sb-analysis also reads:
- **`content.media.ready.v1`:** each announced file becomes a media job. The job keeps the media's type and role, the file's SHA-256, the owner (post or comment) and its account, the deadline (`delete_after`), and the announcement's `attempt`.
- **`content.media.deleted.v1`:** a job still waiting for a file that has been deleted is closed (`gone`). Nothing is fetched, and no acknowledgement is sent: the platform has stopped waiting.

Media announcements and deletions keep flowing while ingest is paused for a full queue (fix 6), as fbs's media did.

### The media policy: (type, role) pairs

`analysis.media.contract_kinds` (new setting) lists the pairs analysed.
- **The default:** `image/content`, `image/attachment`, `image/cover`, `video/content`, `video/attachment`.
- **Skipped by default:** stickers, GIFs, a video's thumbnail (the video itself is analysed), profile pictures, banners, audio and documents.
- **Each skipped file** is acknowledged as `SKIPPED` with the reason `policy:<type>/<role>`.
- **A wrong entry is refused at start:** a pair must use the contract's own type and role names.

### The acknowledgements (`content.media.processed.v1`)

- **Consumer `sb-analysis`:** the contract's registry name. fbs's acks keep `analysis`, as fbs expects.
- **A unique `event_id`:** every acknowledgement gets a fresh one, so a retried file's second acknowledgement is a new event.
- **The status:**
  - `PROCESSED`: analysed, or served from an identical file;
  - `SKIPPED`: by policy, an erased subject, a disabled route, or a refusal;
  - `FAILED_PERMANENT`: too large, undecodable, gone, or no ffmpeg;
  - `FAILED_RETRYABLE`: out of tries on a temporary error.
- **A short `reason`** on every skip and failure, with the details in `error`. The codes are listed in the runbook.
- **The platform code** in `platform`.
- **Results** go to `analysis.media.v1` (the contract's `MediaAnalysis`), for analysed and reused files only.

### Identical files are analysed once

When an announced file's SHA-256 matches a file already analysed under another media ID, that analysis is reused. The file isn't downloaded and no provider is called.
- The copy is stored for the new media ID, and `media_analyses.reused_from` names the source.
- It is published on `analysis.media.v1` and acknowledged `PROCESSED`.
- Its logos and entities count as mentions of the new media, under its own owner and day.

### Changed (for the owner)

- **Migration `00005`** adds `role`, `sha256` and `attempt` to `analysis.media_jobs`, and `sha256` and `reused_from` to `analysis.media_analyses`, plus an index. Nothing removed.
- **New setting:** `analysis.media.contract_kinds`.
- **fbs's acknowledgements** (`fb.media.processed.v1`) are unchanged: same consumer, same statuses, same error text.
- **The stored note on a skipped job** (`media_jobs.last_error`) is now a short code (`route_disabled:image`, `refused:<model>`) instead of a sentence. It is internal only.

### Evidence

- **`TestContractMediaHandOff`** (Postgres). It runs five contract files through the worker:
  - an image is analysed: one provider call, published on `analysis.media.v1`, acknowledged `PROCESSED`;
  - a second media ID with the same SHA-256 reuses that analysis: no second provider call, and no download (its file isn't even in storage), with `reused_from` set, published and acknowledged `PROCESSED`;
  - a sticker attachment is `SKIPPED` with `policy:sticker/attachment`;
  - an undecodable file is `FAILED_PERMANENT` with `undecodable` and an error detail;
  - a file deleted first is closed as `gone` and never acknowledged.
  - Every acknowledgement carries `sb-analysis`, the platform `tt` and its own `event_id`, and nothing goes to fbs's topic.
  - **The test catches a broken reuse:** with reuse switched off on purpose, it failed.
- **`TestContractMediaSample`:** the contract's own `media_ready.tt.json` maps correctly.
- **`TestDecodeContractMedia`:** decoding announcements and deletions, and refusing an announcement without `event_id`.
- **`TestContractKinds`:** the setting refuses a pair that isn't the contract's type and role.
- The existing media test still passes: fbs's acknowledgements are as before.
- All unit tests with race detection, all integration tests and golangci-lint pass.
- **Not yet:** conformance checks on `MediaProcessed` and `MediaAnalysis`. They need contract v1.2.0 (question 1), and come with part F.

### Questions for the master

1. **Conformance for the other kinds:** v1.0.0's `conformance` package checks `ItemAnalysis` but not `MediaAnalysis`, `AuthorFlag`, `TopicUpdate` or `MediaProcessed`.
   - **The master's answer (6 Nov):** contract **v1.2.0** (commit `30153b4`) adds those checks, plus `MediaDeleted`, with a sample each. The master asks to pin v1.2.0 instead of v1.0.0.
   - **Checked here:** the analysis, content and media protos are identical in v1.0.0 and v1.2.0. The only proto change is two additions to discovery's `Candidate`, which sb-analysis doesn't use.
   - **Waiting for the owner:** REQ-022 names v1.0.0, so the switch is the owner's decision. Recommended: yes. If agreed, it's a small pull request of its own: the new pin, plus the five checks in CI.
