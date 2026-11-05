# sb-analysis: review of the contract draft v0.1 (REQ-015)

*For the master session, from sb-analysis.*
- **Reviewed:** `Abdullah95k/securebrand-contracts` at commit `3a75c8b` (pull request #1, branch `claude/securebrand-platform-integration-4iiwlr`), on 6 November 2026 (Iraq time).
- **Checked against:** sb-analysis at commit `7b17d03`.
- **This is a review only:** no code was changed for it. Examples are synthetic.

**Summary:**
- **The draft fits sb-analysis well.** Every field marked **N** (needed) in my Part A field table is in the contract, with one exception: **the account's display name on posts.**
- **My requests:**
  - add the account (an `AccountRef`, with its display name) to `Post`;
  - agree the media consumer's name;
  - keep the media hand-off's retry rule written down;
  - move `analysis.proto` into the contract repository, so the platform registers it with the rest.
- **Adoption effort:** about **6–7 working days**, not 1–2. Section 3 explains why.

## 1. Mapping

**Key:**
- **Agree:** maps as is.
- **Change:** a change I propose, in the contract or on my side as marked.
- **Question:** needs an answer.

### `content.post.v1` (`Post`) → `core.Item` (kind `post`)

| sb-analysis field | Contract field | Verdict |
|---|---|---|
| `ContentID` | `envelope.id` (`fb:post:…`) | **Agree** |
| `Platform` | `envelope.platform` (`fb`, `ig`, `tt`, `yt`) | **Agree.** sb-analysis switches its internal names (`facebook`…) to the codes. |
| `Kind` | message type = post | **Agree** |
| `SourceID` | `account_id` (`fb:account:…`) | **Agree.** Replaces my `fb:page:…`; no stored data to migrate. |
| `SourceName` (needed for per-entity sentiment) | **none on `Post`** | **Change:** see question 3. Add the account's `AccountRef` to `Post`. |
| `AuthorIsSource` | implied (the account publishes the post) | **Agree** |
| `CreatedAt` | `published_at.at`, with `precision` | **Agree.** sb-analysis uses times coarser than a minute for storage and rollups only, never for bot timing. |
| `ObservedAt` | `envelope.observed_at` | **Agree** |
| (ordering of observations) | `envelope.version` | **Agree:** the highest wins, replacing my `observed_at` comparison |
| `Text` | `title.text`, `text.text`, `description.text`, plus the first link title not already in them | **Change (mine):** the adapter joins title, text and description in that order. YouTube's title carries the topic, and a Facebook link post's title often does too. |
| `RemoveSpans` (tagged people, hidden as `@user` before text leaves) | `text.spans` with `type = MENTION`, `unit`, `account.type` | **Agree.** Converted per `unit` (UTF-16, code point, byte). A mention whose account is a page, or an unambiguous registry entity, stays readable; everything else is hidden. |
| `LangHint` | `text.platform_language` | **Agree** |
| `Hashtags` | `hashtags` | **Agree** |
| `MediaIDs` | `media[].media_id` (role `CONTENT`) | **Agree** |
| `Status` | `status.availability` (`DELETED` → deleted) | **Agree** |
| (deletions and other partial events) | `envelope.partial` | **Agree.** A partial event that isn't a deletion is never treated as an edit: its unset text means "unchanged", not "empty". |
| (not used) | `counts`, `relevance`, `coverage`, `created_in_country`, `reshare`, `audio`, `playlists`, `poll`, `category`, `platform_topics`, `extras` | Agree (not needed by the analyses) |
| (ad detection hint, not used today) | `labels.paid_partnership`, `labels.ad` | **Agree.** Useful later: a platform-declared paid partnership can set `ad_type = paid` without asking the model. |
| (video transcript, not consumed today) | `captions[]` | **Agree** (question 5) |

### `content.comment.v1` (`Comment`) → `core.Item` (kind `comment` or `reply`)

| sb-analysis field | Contract field | Verdict |
|---|---|---|
| `ContentID` | `envelope.id` (`fb:comment:…`, replies too) | **Agree** |
| `Kind` | `depth`: 0 is a comment, ≥1 a reply | **Agree** |
| `ThreadID` | `post_id` | **Agree** |
| `ParentID` | `parent_comment_id` for replies; `post_id` for depth 0 | **Agree** |
| `SourceID` | `account_id` (the post's account) | **Agree** |
| `AuthorKey` (bot detection) | `author.account.id`; none when `author.hidden` | **Change (mine):** see question 2 |
| `AuthorIsSource` | `flags.by_post_owner` | **Agree.** tks must set it (the field matrix already lists that gap). |
| (author type, helpful for bots) | `author.account.type` | **Agree** (unspecified on Instagram and TikTok; the bot rules work without it) |
| `CreatedAt` | `published_at.at` + `precision` | **Agree,** with the same precision rule as posts. YouTube's "a year ago" comments are kept out of the timing signals. |
| `ObservedAt`, ordering | `envelope.observed_at`, `envelope.version` | **Agree** |
| `Text`, `RemoveSpans`, `LangHint` | `text.text`, `text.spans`, `text.platform_language` | **Agree** (as for posts) |
| `MediaIDs` | `attachments[].media_id` | **Agree** |
| `Status` | `status.availability`, `envelope.partial` | **Agree** |
| (never sent to an AI provider: D-010) | `author.account.display_name`, `handle`, `picture_url` | **Agree:** sb-analysis reads none of them. |
| (not used) | `root_comment_id`, `links`, `counts`, `relevance`, `flags.pinned`, `flags.liked_by_creator`, `extras` | Agree. `links` would help affiliate-link detection in comments later. |

### `content.media.ready.v1` (`MediaReady`) → `core.MediaJob`

| sb-analysis field | Contract field | Verdict |
|---|---|---|
| `MediaID` | `media_id` | **Agree** |
| `OwnerContentID` | `owner_id` + `owner_kind` | **Agree.** Only `post` and `comment` owners are analysed; `account` and `audio` owners are acknowledged `SKIPPED`. |
| `Platform` | `platform` | **Agree** |
| `SourceID` | `account_id` | **Agree** |
| `Kind` (my policy keys: photo, video, reel, comment_photo, comment_video, cover, thumbnail…) | `type` + `role` | **Change (mine):** the media policy is re-expressed as (type, role) pairs:<br>• IMAGE + CONTENT or COVER: analysed;<br>• VIDEO + CONTENT: analysed;<br>• IMAGE or VIDEO + ATTACHMENT: analysed;<br>• THUMBNAIL: analysed only when the video itself isn't announced;<br>• PROFILE_PICTURE, STICKER, GIF: skipped. |
| `Bucket`, `Key`, `Size`, `ContentType` | `object.bucket`, `key`, `size_bytes`, `content_type` | **Agree** |
| (de-duplication, not built) | `object.sha256`, `perceptual_hash` | **Agree.** With `sha256`, the same file posted twice can be analysed once (a cost saving I'd add). |
| `DurationMS` | `duration_ms` | **Agree** |
| `AltText` | `alt_text` | **Agree** |
| `DeleteAfter` | `delete_after` | **Agree** (question 4) |
| (must I acknowledge?) | `required_consumers` | **Question:** the consumer's name (question 4) |
| (not used) | `variant`, `width`, `height`, `stored_at`, `provenance` | Agree |

### `content.media.processed.v1` (`MediaProcessed`), produced by sb-analysis

| sb-analysis field | Contract field | Verdict |
|---|---|---|
| `event_id` = `<media_id>:<consumer>` | `event_id` (unique per event) | **Change (mine):** mine repeats when a `FAILED_RETRYABLE` is later followed by `PROCESSED`. It becomes `<media_id>:<consumer>:<status>:<processed_at µs>`. |
| `media_id` | `media_id` | **Agree** |
| `consumer` = `analysis` | `consumer` (the comment says `sb-analysis`) | **Question:** one name, used by every producer's `required_consumers` (question 4) |
| `status`: processed, failed permanent, failed retryable | `status`, plus the new `SKIPPED` | **Agree.** Media skipped by policy (stickers, GIFs, profile pictures) moves from `PROCESSED` to `SKIPPED`. |
| `processed_at` | `processed_at` | **Agree** |
| `error` (up to 300 characters; never content) | `error` | **Agree** |
| (not set today) | `platform` | **Agree:** sb-analysis will set it |

## 2. Gaps: what an analysis needs that the contract doesn't carry

| Gap | Why it matters | Proposal |
|---|---|---|
| **The account's display name on `Post`** | Per-entity sentiment: "your network is down again" on a telecom's page means the page's brand. Also needed by the alert engine and dashboards. | Add `AccountRef account = 33` to `Post` (additive; `account_id` stays). Question 3. |
| **Mention spans on Instagram and YouTube** (the field matrix lists them as missing) | Without spans, sb-analysis can't hide tagged people's names before sending text to its AI provider (D-010's spirit; the owner approved hiding tagged names on 6 Nov). | Collector side: yts extracts them (REQ-010, fix 20). sb-analysis side (needs the owner's approval): hide `@handle` tokens that aren't registry entities when a text has no spans. |
| A written retry rule for media | After `FAILED_RETRYABLE`, does the producer announce the file again, and how often? fbs re-announces (`max_retryable_failures: 3`). | State it in the contract. Optionally add `attempt` (int32) to `MediaReady`, so a consumer knows a retry from a duplicate. |
| A minimum media lifetime | sb-analysis needs a file for minutes normally, and hours under a backlog or provider outage | State that `delete_after` is at least 24 hours after `stored_at` (fbs keeps 72 hours today) |
| A skip reason | `SKIPPED` and failures share `error` | Optional: a `reason` field (`policy:sticker`, `too_large`, `undecodable`) beside `error` |
| `analysis.*` in the contract | ClickHouse, the alert engine and the client app consume them, and the platform registers subjects (collectors never do) | Move `analysis.proto` into the contract repository as `securebrand.analysis.v1`, and add its four topics to `registry/topics.yaml` (question 6) |

No gap found for:
- the thread's post text (joined by `post_id`);
- `by_post_owner` (tks to fill);
- link titles (Facebook only; the others have none in posts);
- captions;
- the erasure path. `lifecycle.erasure.v1` with `subject_id` and `owner_order_ref` maps onto sb-analysis's new `record` mode, which stores a request and deletes nothing until the owner orders it.

## 3. Effort to adopt (updated)

| Work | Days |
|---|---|
| Adapter: `Post`, `Comment` and `MediaReady` into `core.Item` and `core.MediaJob`. Covers: joining title, text and description; span conversion in all three units; partial events; platform codes; media (type, role) policy; `SKIPPED` and the new event ID on `MediaProcessed`. | 2 |
| Bot detection on the author's account ID instead of `author_key` (question 2): a migration, the scoring SQL, `AuthorFlag`, erasure by `subject_id` | 1 |
| `analysis.proto` re-keyed and moved to the contract repository (question 6); the relay's headers become `sb-type` and `sb-contract` | 1 |
| Captions used as the transcript when a post has them (question 5) | 0.5 |
| Depend on the contract's Go module at a tag; delete this repository's `securebrand.content.v1` and its topic and schema entries | 0.5 |
| Tests: the conformance kit on `MediaProcessed`, the contract's samples through the adapter, an end-to-end run | 1–1.5 |
| The account's display name: **0** if `Post` carries it; **+0.5–1** if sb-analysis must join `content.account.v1` itself | 0–1 |
| **Total** | **about 6–7** |

**Why it's more than 1–2 days:** 1–2 days was the adapter alone. Four things weren't in that estimate:
- the switch from `author_key` to account IDs (owner D-003);
- re-keying sb-analysis's own outputs to the contract's IDs and codes;
- captions;
- the media policy re-expressed in the contract's terms.

## 4. Answers to the questions

### Q1. The adapter against my field table

Every field marked **N** in Part A (`analysis-contract-notes.md`, section 5) has a contract field, except one:

| Field marked N | Contract field |
|---|---|
| id, platform, kind | `envelope.id`, `envelope.platform`, the message type and `depth` |
| text in full | `text.text` (+ `title` and `description` on posts), with `truncated` telling when it isn't full |
| account id | `account_id` |
| **account display name** | **missing on `Post`**: question 3 |
| post id on comments; parent comment id on replies | `post_id`; `parent_comment_id` |
| published_at, observed_at, version, status | `published_at` (with precision), `envelope.observed_at`, `envelope.version`, `status`, `envelope.partial` |
| author key, is_by_owner (bots) | `author.account.id` (question 2); `flags.by_post_owner` |
| post text for comments | the post's own event, joined by `post_id` |
| media: id, owner, kind, storage object, deadline | `media_id`, `owner_id` and `owner_kind`, `type` and `role`, `object`, `delete_after` |

The **H** (helpful) fields are all there too: mention spans with type and unit, links with titles, hashtags, the platform's language, alt text, captions. The only exception is where a platform doesn't serve a field logged out.

### Q2. `author_key` is gone: can bot detection key on `author.account.id`?

**Yes.** What changes:
- **The bot state is keyed by (platform, author account ID).** Today `analysis.authors` is keyed by the HMAC from fbs. A migration adds `author_id text` and re-keys the tables (`authors`, `author_overrides`, the author column of `analyses`). There's no production data, so nothing has to be converted.
- **The scoring rules don't change.** They need a stable key per author, which an account ID is, and stabler than a handle. A comment whose author is hidden (`author.hidden`, no ID) is left out of scoring, as today.
- **Outputs:**
  - `AuthorFlag` gets `author_id` (the account ID) and is keyed by it on `analysis.author.v1`;
  - `ItemAnalysis` gets `author_id` beside `author_key`;
  - `author_key` stays in the proto, unused: fields are never removed.
- **The HMAC secret shared with fbs is no longer needed.** My REQ-006 answer suggested sb-analysis compute its own HMAC. With D-003 that's unnecessary.
- **What never changes:** author IDs and names are still never sent to an AI provider (D-010). The model's input never had them.
- **Effort:** about 1 day.

### Q3. The account's display name: join it, or carry it on posts?

**Preferred: posts carry it**, as `AccountRef account` on `Post` (id, handle, display name, type, verified, at the time of the observation). Why:
1. **It's needed at analysis time, often within a minute** (real-time lane). With a join, sb-analysis would keep its own copy of `content.account.v1` and wait for it. Accounts are refreshed about daily, and a newly discovered account's first posts can arrive before its account event.
2. **The name at the time is what the comment refers to.** Pages rename; a joined current name can be wrong for older items.
3. **Every consumer gains:** the alert engine, ClickHouse and the client app all show the account with the post.
4. **It costs a few bytes per post, and is additive.**

**Fallback if the contract doesn't add it:** sb-analysis consumes `content.account.v1` into a small cache of display names by account ID. Posts whose account isn't known yet are analysed without it, which loses some per-entity sentiment. That's 0.5–1 day.

### Q4. The media hand-off

**It fits.** `required_consumers`, `delete_after`, the storage object with SHA-256, `SKIPPED` and `MediaDeleted` are what sb-analysis needs. Points:
- **The consumer's name:** fbs's `required_consumers` uses `analysis` today, and sb-analysis acknowledges as `analysis` (`analysis.media.consumer`). The proto comment says `sb-analysis`. **Pick one.** I suggest `sb-analysis`, set in each producer's config when it adopts the contract.
- **Retries:** write down what a producer does after `FAILED_RETRYABLE` (re-announce, how many times), or add `attempt` to `MediaReady`.
- **Lifetime:** state a minimum of 24 hours between `stored_at` and `delete_after`.
- **`MediaDeleted`:** sb-analysis will consume it, to drop pending jobs whose file is already gone instead of failing them.
- **`sha256`:** sb-analysis will reuse a result for an identical file (a saving).
- **Optional:** a `reason` beside `error` for `SKIPPED` and failures.

Nothing else is missing.

### Q5. Captions

**`Post.captions` fits what I'd use.** sb-analysis would take one track as the video's transcript instead of paid speech-to-text, at no cost per minute:
- **which track:** a non-automatic track in the post's language first, else an automatic one;
- **where it goes:** the text goes into the vision call;
- **recording the source:** `MediaAnalysis` gets a transcript source (`platform_captions` or the speech-to-text model) and its language.

Long captions are truncated by sb-analysis, so the contract needs no limit.

**The reserved `Transcript` message** (segments with timing) isn't needed by the analyses: frames are sampled independently of the speech. Keeping it reserved is fine.

`VideoInfo.caption_languages` also helps: when a track exists but wasn't collected, sb-analysis can skip speech-to-text and wait for it.

### Q6. sb-analysis's outputs (proposed changes to `analysis.proto`)

`securebrand.analysis.v1` has never been registered or consumed. **I propose revising it once, before its first registration**, together with the contract's v1.0.0, rather than carrying dead fields from day one. If the master prefers strict additions only, the same can be done by adding fields and leaving the old ones unused.

**Common to all four messages:**
- **Platform as a text code** (`fb`, `ig`, `tt`, `yt`), not this repository's `content.v1` enum. Drop the import of my retired `content.proto`.
- **Contract IDs everywhere:**
  - `source_id` → `account_id` (`<p>:account:…`);
  - `thread_id` → `post_id`;
  - add `parent_comment_id`.
- **Kind** as the contract's entity kind (`post` or `comment`), with `depth`, instead of my `ContentKind` enum (post, comment, reply).
- **Media type and role:** the contract's `MediaType` and `MediaRole`, imported from `securebrand.content.v1` (the contract's).
- **Headers:** `sb-type` and `sb-contract`, like `content.*` (mine are `fbs-type` and `content-type` today).

**`ItemAnalysis`:**
- `author_id` (the author's account ID; question 2);
- **`content_version`**: the `envelope.version` of the item that was analysed, so a result can be tied to the exact version of an edited item;
- `producer` and `producer_version` (standard E4);
- `routing` (`tracked_entity`, `urgent_source`, `normal`) and the matched entity IDs, for the alert engine (REQ-006, question 5).

**`MediaAnalysis`:**
- a real `analysis_version`;
- `input_hash` = the file's `sha256`;
- `transcript_lang` filled;
- `transcript_source` (`platform_captions` or the speech-to-text model).

**`AuthorFlag`:** `author_id`, and keyed by it.

**`TopicUpdate`:** no change.

**Add to the contract repository:**
- `analysis.proto` as `securebrand.analysis.v1`;
- its topics in `registry/topics.yaml`:

  | Topic | Message | Key |
  |---|---|---|
  | `analysis.result.v1` | `ItemAnalysis` | content ID |
  | `analysis.media.v1` | `MediaAnalysis` | media ID |
  | `analysis.author.v1` | `AuthorFlag` | author ID |
  | `analysis.topic.v1` | `TopicUpdate` | topic ID |

- samples for each, and the conformance kit run on them in sb-analysis's CI.

**The schema subjects already on the pilot's registry:** the 4 `content.*` value subjects, `securebrand/content/v1/content.proto` and the 4 `analysis.*` subjects, from this repository's copy around 1 November. They must be deleted (with the owner's approval) before the platform registers v1 under the same names (`analysis-contract-notes.md`, section 2).
