# sb-analysis: adopting contract v1.0.0 (REQ-022)

*The record of REQ-022, kept current for the master session and the owner. Times are Iraq time. Started 6 November 2026.*

- **The contract:** the Go module `github.com/abdullah95k/securebrand-contracts`, pinned in `go.mod`:
  - REQ-022 was built on **v1.0.0**;
  - since 7 November, **v1.2.0**, on the owner's decision (question 1 below). The messages are identical; v1.2.0 adds the checks.
- **Done** (6 November, `fe0dc52`): every part below is merged. Production stays off (`values-production.yaml`, replicas 0) until the owner decides to switch it on ("Switching on", below).

## Progress

| Part | What | State |
|---|---|---|
| A | Contract in: posts and comments (`content.post.v1`, `content.comment.v1`) | **Merged** (step 1, `cbf46fd`) |
| B | Contract out: `securebrand.analysis.v1` from the contract; delete our own protos | **Merged** (step 1, `cbf46fd`) |
| C | The media hand-off (`content.media.ready.v1`, `.processed.v1`, `.deleted.v1`) | **Merged** (step 2, `a0d6a6f`) |
| D | Bots keyed by the author's account ID; hidden authors left out | **Merged** (steps 1 and 3, `fe0dc52`) |
| E | Captions used as the transcript (`transcript_source`) | **Merged** (step 3, `fe0dc52`) |
| F | The full end-to-end test: every kind produced, a deletion and an estimate | **Merged** (step 4, `fe0dc52`) |

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

## Step 3: authors by account ID, and captions

### Part D: the owner's `authors` commands

- **Since step 1:**
  - bot scoring of contract items is keyed by the author's account ID (`author.account.id`; for a post without an author, its account);
  - authors the platform hides are never scored;
  - `AuthorFlag` is published only for authors with an account ID, with `author_id` set and keyed by it.
- **Now:** `sb-analysis authors list` shows the account ID for those authors (fbs's authors keep their hex key). `authors mark --key` and `authors log --key` take either form, and `mark` takes the platform from the account ID.

### Part E: captions as the transcript

- **Which captions:** a contract post's caption track is stored with the post (`content_items.captions`, with its language and source).
  - The uploader's own captions come first, then the platform's speech-to-text captions.
  - Among equals, the post's language comes first.
  - A machine translation is never used: it isn't what was said.
- **How they're used:** a video belonging to that post uses the captions as its transcript, and no speech-to-text is bought. The captions leave masked like any transcript: phone numbers and e-mails are hidden (D-010).
- **What's published:** `MediaAnalysis` carries `transcript_lang`, and `transcript_source`, which is `platform_captions` or the speech-to-text model's name.
- **Edits:** a re-scrape with captions added or changed updates them, even when the post's text is the same.

### Changed (for the owner)

- **Migration `00006`** adds `captions`, `captions_lang` and `captions_source` to `analysis.content_items`. Nothing removed.
- **The `authors` output:** its first column is now headed AUTHOR, not AUTHOR KEY, and shows account IDs as text.
- **Stored media results** gain `transcript_lang` and `transcript_source`.

### Evidence

- **`TestWorkerVideoUsesCaptions`** (Postgres and ffmpeg): a TikTok video whose post has captions.
  - No speech-to-text is bought.
  - The transcript is the captions, with `platform_captions` and `ar`, stored and published.
  - The caption's phone number never reaches the provider.
- **`TestWorkerVideoSpeechCost`:** without captions, the transcript's source is the speech-to-text model.
- **`TestBestCaptions`:** the order of choice, and never a translation. The contract's TikTok sample gives its ASR captions.
- **`TestAuthorKeysAndLabels`:** the commands read and print account IDs and hex keys.
- All unit tests with race detection, all integration tests and golangci-lint pass.

## Step 4: every kind, end to end

**`TestContractEveryKindEndToEnd`** (Postgres and Redpanda; runs in CI's integration job). A fake platform publishes, with the contract's headers:
- a post whose time is an **estimate**: "1d", precision DAY, at 00:00 Iraq time, as D-013 says;
- 32 copies of one comment from one account across four posts, so bot scoring flags it;
- a normal comment, later **deleted by a partial event**;
- three files: an image to analyse, a sticker (skipped by policy), and an image the platform **deletes** before analysis.

sb-analysis runs ingest, pipeline, media and relay. Then the owner's two commands run: bot scoring, and the approval of a new topic.

**Every kind sb-analysis produces then comes off Kafka** with `sb-type` and `sb-contract: 1.0.0`, and passes the contract's checks:

| Kind | Topic | Check |
|---|---|---|
| `ItemAnalysis` (34 items) | `analysis.result.v1` | `conformance.ItemAnalysis`, production mode |
| `MediaAnalysis` | `analysis.media.v1` | the rules of v1.2.0's `conformance.MediaAnalysis` |
| `AuthorFlag` (the copying account) | `analysis.author.v1` | the rules of v1.2.0's `conformance.AuthorFlag` |
| `TopicUpdate` (the approved topic) | `analysis.topic.v1` | the rules of v1.2.0's `conformance.TopicUpdate` |
| `MediaProcessed` (`PROCESSED`, `SKIPPED`) | `content.media.processed.v1` | the rules of v1.2.0's `conformance.MediaProcessed` |

Also checked:
- the estimated time is kept as the post's time, and published as its `created_at`;
- the deleted comment is marked deleted;
- the deleted file is never fetched or acknowledged.
- **The test catches a broken message:** with the `reason` removed from acknowledgements on purpose, it failed on the sticker's `SKIPPED`.

**Why "the rules of v1.2.0":** v1.0.0's `conformance` package has no checks for those four kinds, so the test first applied v1.2.0's rules itself. **Since 7 November** (v1.2.0 pinned), it uses the package's own `conformance.MediaAnalysis`, `AuthorFlag`, `TopicUpdate` and `MediaProcessed`, in production mode.

**Two fixes the checks led to** (merged with step 3):
- a transcript always carries its language (`und` when unknown) and its source;
- a `document` keeps its type.

## Switching on (the owner, 7 November 2026)

The owner's answers:
- **Inputs:** every collector. The contract's topics, which fbs, tks, yts and igs publish as each adopts the contract, and fbs's own `fb.*` until fbs does: `analysis.inputs: [content, fb]`. **Since 11 Nov, 21:45 Iraq time** fbs publishes its posts and comments on `content.*`, and the inputs are `[content, fb-media]`: of fbs's own topics only the media hand-off and erasures.
- **Contract:** v1.2.0.
- **Budget:** no daily cap; Claude Haiku for everything.

**Applied in `values-production.yaml`:**
- both workloads at 1 replica;
- `inputs: [content, fb]`;
- `budget: {daily_usd: 0, allow_unlimited: true}`;
- escalation and audit off;
- topic and entity curation on Haiku;
- speech-to-text off (as before), so transcripts come from captions only.

`TestProductionConfig` loads production's configuration as the chart builds it and fails if any of this drifts.

**What remains is the master's:** pin the commit, and give `analysis-kafka` write access to `analysis.*` again in the same change.

### Questions for the master

1. **Conformance for the other kinds (answered: v1.2.0, pinned 7 Nov):** v1.0.0's `conformance` package checks `ItemAnalysis` but not `MediaAnalysis`, `AuthorFlag`, `TopicUpdate` or `MediaProcessed`.
   - **The master's answer (6 Nov):** contract **v1.2.0** (commit `30153b4`) adds those checks, plus `MediaDeleted`, with a sample each. The master asks to pin v1.2.0 instead of v1.0.0.
   - **Checked here:** the analysis, content and media protos are identical in v1.0.0 and v1.2.0. The only proto change is two additions to discovery's `Candidate`, which sb-analysis doesn't use.
   - **Waiting for the owner:** REQ-022 names v1.0.0, so the switch is the owner's decision. Recommended: yes. If agreed, it's a small pull request of its own: the new pin, plus the five checks in CI.
