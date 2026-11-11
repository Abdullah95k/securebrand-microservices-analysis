# Content analysis: what you need to decide, and what to run

*For the owner. Plain language. All times are Iraq time.*

## What was built

A new service, **sb-analysis**, reads every post and comment the collector saves and works out:
- the language and dialect;
- whether the author is positive, negative, neutral or mixed;
- which brands, organisations, public people, positions (such as "the Prime Minister") and places are mentioned, and what the author thinks of each one;
- every topic it is about, said directly or only implied ("الوطنية طافية" is about electricity). Each topic has one id, so it works the same in every language, and topics have sub-topics ("Electricity cuts" under "Electricity supply"): asking for electricity also finds the cuts. `sb-analysis topics find "electrical cuts"` (or in Arabic) shows the topics and how many items carry them. **The topic list grows by itself:** when content is about something missing from the list, the AI proposes it, the system checks it is not a duplicate (in Arabic and English), and once a day new topics join the list. Brands, people, positions and places become topics automatically too;
- whether it is a complaint, a question, praise, spam, toxic, or urgent;
- **whether a post is an ad:** affiliate marketing (links or codes that earn a commission), a brand promoting itself, a disclosed paid ad, a hidden ad (an influencer promoting without saying so), or a private sale;
- **possible legal or policy breaches**, for a person to review: hate speech, threats, incitement, terrorism, child abuse, sexual content, graphic violence, self-harm, drugs, weapons, gambling, restricted goods, scams, unlicensed finance, fake goods, privacy violations, defamation (officials included), misinformation, extortion, and other crimes under Iraqi law (trafficking, bribery, vote buying, selling exam questions...).

Separately, once a day, every account with 5 or more comments in the last 30 days is scored for **bot or fake-account behaviour** (posting in bursts, copying the same text everywhere, the same text as many other accounts on the same day, replying within seconds, posting day and night, mostly spam). Small accounts with only 2 or 3 comments are flagged too when they post the same text as a group of other accounts on the same day, which is how networks of fake accounts work. Flagged accounts, and everything they ever posted, carry the flag.

**Generic titles are checked for the country.** "رئيس الوزراء" alone could be any country's prime minister. The system links it to Iraq's Prime Minister only when the context is Iraqi; "رئيس الوزراء العراقي" always links, "رئيس الوزراء البريطاني" never does. The same applies to ministers, the speaker, the central bank and ministries.

It does the same for images and videos (logos, text in the picture, what is shown, and speech turned into text). The files are deleted afterwards; only the results are kept.

It runs on Claude (Anthropic's AI). Most comments are sent in bulk at half price and come back within hours. Comments that mention a brand you track are analysed within a minute.

**It is built and tested, but it is switched off on the pilot server** until you decide question 1 below.

## Cost

At list prices, checked 1 November 2026. Estimates until the test with your labeller measures the real numbers.

| Text items per year | Recommended setup | First estimate |
|---|---|---|
| 100 million | about **$3,900** | $5,000 |
| 300 million | about **$9,700** | $15,000 |
| 1 billion | about **$28,000** | $50,000 |

This includes the ads, legal flags, growing topics and bot detection (about 5% of the total).

**How it became cheaper, without losing quality or speed:**
- **Shorter AI answers.** We pay mostly for what the AI writes back. It now writes short codes instead of words, and only the warnings that apply. Same information, about 30% cheaper.
- **Free work is done by our own code.** Which alphabet a comment uses, and a comment's keywords, are worked out by our code, not paid for.
- **Identical comments are analysed once.** Contest entries ("تم") and prayers under the same post get one analysis, copied to all of them.
- **Fewer quality checks by the most expensive AI:** 1 in 5,000 items instead of 1 in 1,000. That is still about 165 checks a day, enough to notice if quality drops.
- **Speech in videos:** above about 150 million items a year, our own speech-to-text server (about $2,000 a year) is cheaper than paying per minute. We use it only if the test shows it understands Iraqi dialect well enough.
- **The daily spending limit now includes speech-to-text,** so the limit really holds.

The "stronger setup" (a more expensive AI, Sonnet, for posts, images and videos) would now cost about $43,000 a year at 300 million. It is used only if the test shows the cheaper AI is not good enough there.

Server and storage costs come on top: roughly 330 GB of database storage a year at 300 million items.

## Your decisions (answered 1 November 2026, and later as dated)

| # | Question | Your answer | What the system does |
|---|---|---|---|
| 1 | Send comments to Anthropic? | Yes | Analysis uses Claude. Phone numbers, e-mails and author names never leave. Your lawyer should still check the data-processing terms. |
| 2 | Daily spending limit | Set after the test; then, 7 Nov: $10 a day; 11 Nov: up to $60 | Production stops spending at $60 a day (see 21 and 23). The pilot kept $5 a day. |
| 3 | Everything within minutes? | No, within hours is fine | Bulk at half price; brand mentions within a minute. |
| 4 | Topic list | Fine, if missing topics are added automatically | New topics are proposed by the AI, checked for duplicates, and approved by a second AI every 6 hours once 5 posts use them. You can step in at any time. |
| 5 | Brand list | Fine, if missing ones are added automatically | New brands, organisations, public figures, positions and places found 5+ times are approved by the AI every 6 hours; spelling variants are merged into the known one. |
| 6 | People | Public accounts stored | Public figures and public accounts are stored automatically. Private people are never stored. When the AI is unsure whether someone is public, it does not store them. |
| 7 | Two copies of comments | Keep both for now | Both kept. |
| 8-9 | Labeller and quality bar | No labeller; rely on the AI | The strongest AI (Opus) labels 1,200 real items once, about $3-5, and Haiku is measured against it (step 6 below). Since 7 Nov (see 19), production no longer checks 1 in 5,000 items with Opus. |
| 10 | Backup AI company | You asked me to choose | **Google Gemini** (reasons below). It is already set up on the pilot as the backup, and starts working as soon as you add its key (step 5). |
| 11 | Legal flags | Religion: always flag criticism | Insults, mockery or attacks on any religion, sect, sacred text, symbol or religious figure are always flagged. A respectful question or disagreement, someone stating their own belief, and criticism of a cleric's *political* actions are not flagged on their own. Iraqi law punishes insulting religion, not disagreement, and many politicians are clerics. Peaceful political criticism and a person's sexuality are not flagged on their own; hate speech, threats, defamation, misinformation and the rest always are. |
| 12 | Bot flags | Show them; users can mark "not a bot", with a log | Bot flags are shown on every item of the account. Anyone you allow can mark an account "not a bot" (or "is a bot"). Each decision is logged with who, when, why and the before/after. |
| 13 | New topics | An AI approves; a person only steps in if needed | As in 4. |
| 14 | Very large posts | Every comment | Every comment is analysed. |
| 15 | Names of tagged people (6 Nov) | Hide them from the AI | When a commenter tags someone, that person's name is written as "@user" before anything is sent to the AI. Tagged brands and public figures stay readable, and the saved text is unchanged. |
| 16 | Deleting data on an erasure request (6 Nov) | Never without your order | An erasure request from the collector is only recorded; nothing is deleted. A recorded request is deleted only when you order it, with `make analysis-cmd ARGS='erasures apply --request <id> --yes-delete'`. `make analysis-cmd ARGS='erasures list'` shows the requests received. |
| 17 | Where production reads from (7 Nov) | fbs, tks, yts, igs, and the contract | Production reads the contract's topics (`content.*`), which every collector (fbs, tks, yts, igs) publishes as it adopts the contract, and fbs's own `fb.*` topics until fbs does. Results from `fb.*` stay in the analysis database; everything else is also published to the platform. When fbs moves to the contract (from 13 Nov), an item already analysed from `fb.*` is not analysed or paid for again: its stored result is published to the platform then. |
| 18 | Contract version (7 Nov) | v1.2.0 | Pinned. The test that runs in CI checks every kind of result with the contract's own checks. |
| 19 | Which AI (7 Nov) | Use Haiku | Every AI call uses Claude Haiku: the text, image and video analysis, and the topic and brand list upkeep. The second opinion from a stronger model on unsure brand mentions, and the Opus quality sample, are switched off. Spending is still measured (`sb_analysis_cost_usd_total`). |
| 20 | Speech in videos (7 Nov) | Yes, with our own Whisper server | Pictures in images and video frames are analysed by Haiku. Speech is transcribed by our own Whisper server inside the cluster (Whisper large-v3-turbo, on the existing machines, no graphics card), so the audio never leaves our systems and there is no per-minute fee. The platform's captions are used first; Whisper takes the videos without them, one at a time, the first 10 minutes at most. If it can't keep up, the options come to you with their costs. |
| 21 | Daily cap (7 Nov, D-031) | $10 a day, a hard stop | Once a day's spending reaches $10 (Iraq time), every paid AI call waits until midnight: urgent items, images and videos, and the topic and brand list upkeep too. An alert fires when that happens. Our own Whisper server costs nothing per video, so it isn't limited. Waiting items are analysed the next day; a media file whose deadline passes first is lost for analysis. |
| 22 | The quality check (7 Nov) | Just Haiku for now | The one-off check that had Claude Opus label 1,200 real items (about $3–5) to measure Haiku against doesn't run: no Opus, not even once. Haiku's quality stays unmeasured for now. It can still be measured at no AI cost if a person labels the sample with the labelling tool (`docs/analysis/evaluation.md`, section 2), or later if you allow the Opus labels. |
| 23 | Daily cap raised (11 Nov, D-052) | Up to $60 a day; YouTube thumbnails: not now | The cap is $60 a day, still a hard stop. Within it, comments and posts always keep at least $30 (about 230,000 items a day), and images and videos always keep at least $18: images and videos together stop at $30, comments and posts together at $42. While tks is catching up, its media alone would need about $67 a day, so the files with the nearest deadlines go first and some older ones may be deleted before they are analysed. tks now downloads videos only for posts up to 7 days old, which shrinks that wave. YouTube thumbnails stay unanalysed. |

**Why Google Gemini as the backup:**
- its prices are confirmed on Google's official page (OpenAI's could not be checked);
- Gemini 3.1 Flash-Lite is the cheapest capable backup ($0.25 / $1.50 per million tokens), and it handles Arabic well;
- it is a different company with different servers, so an Anthropic outage does not affect it.

## For your lawyer (I flag these, I do not decide them)

- **Outside AI companies** process the comments: you need their data-processing agreements and their data-retention terms (Anthropic can offer zero retention on request).
- **Keeping comments forever** is hard to justify under data-protection rules for private people's comments. You also have two copies.
- **Opinions about politicians and religion,** linked to a (pseudonymous) author, are "special category" data in GDPR terms. You need a data-protection impact assessment.
- **People mentioned in comments:** we keep only public figures.
- **Iraq** has no data-protection law in force yet. Your clients' countries may apply theirs.
- **Bot scores are automated profiling of people** (even with hidden ids). A wrong "bot" label shown to a client is an accusation about a real person: keep it reviewed and correctable.
- **Legal flags are allegations, not findings.** Do not publish them or report users to authorities based on them without legal advice.

## Steps on the pilot server

Everything below happens on the DigitalOcean server that runs the pilot. The collector keeps running the whole time.

### What you need before you start

- Your DigitalOcean login.
- **An Anthropic API key:**
  1. Go to console.anthropic.com and open **API keys**.
  2. Click **Create key** and copy it (it starts with `sk-ant-`).
  3. Also set a monthly spend limit under **Limits**.
- **A Google Gemini API key** (optional, for the backup): go to aistudio.google.com, click **Get API key**, and use a paid project.
- Never paste keys in chat or e-mail. You type them only into the file in step 5.

### Step 1. Connect to the server

**Easiest, in the browser:**
1. Go to cloud.digitalocean.com and open **Droplets**.
2. Click the pilot droplet.
3. In the left menu, click **Access**, then **Launch Droplet Console**. A black terminal window opens, logged in as root.

**Or from your computer:**
1. On a Mac, open **Terminal**. On Windows, open **PowerShell**.
2. Type the line below, replacing `SERVER_IP` with the address shown on the droplet's page as **ipv4**. The first time, answer `yes`.

```bash
ssh root@SERVER_IP
```

To paste a command into the terminal: right-click (Windows/PowerShell) or Cmd+V (Mac). Run the commands one at a time, and wait for each one to finish (the prompt comes back).

### How the analysis sits next to the collector

The collector (fbs) and the analysis are separate:
- **Separate folders:** the collector lives in `~/securebrand-microservices` and is managed by the collector work. The analysis lives in `~/securebrand-analysis`.
- **Separate images:** the collector runs `fbs:dev`, the analysis runs `sb-analysis:dev`.
- **Separate containers:** restarting or updating the collector never touches the analysis, and the analysis never touches the collector.
- **Shared data:** the analysis reads what the collector saves, and keeps its own tables next to the collector's in the same database.

Never run `make pilot-...` commands in `~/securebrand-analysis`. Those manage the collector, from its own folder.

### Step 2. Check the collector is running and there is disk space

These only read:

```bash
docker ps --format '{{.Names}}  {{.Status}}'
```

The `fbs-...` services should show **Up**.

```bash
df -h /
```

The **Avail** column should show at least **3G**. If it shows less, stop and tell me. The fix is a bigger disk, never deleting data.

Install `tmux`, so long steps keep running if your connection drops:

```bash
apt-get install -y tmux
```

```bash
tmux new -s work
```

If you get disconnected, reconnect (step 1) and type `tmux attach -t work`.

### Step 3. Get the analysis into its own folder and build it

```bash
git clone --depth 1 --branch claude/jolly-bell-mb5sct https://github.com/Abdullah95k/securebrand-microservices-analysis ~/securebrand-analysis
```

```bash
cd ~/securebrand-analysis
```

```bash
make analysis-image
```

The build takes 5-10 minutes and ends with `naming to docker.io/library/sb-analysis:dev`. It doesn't stop or change the collector.

From now on, every command below runs in `~/securebrand-analysis`. After a reconnect, type `cd ~/securebrand-analysis` first.

### Step 4. Measure the data (counts only, safe to share)

```bash
make analysis-stats > ~/pilot-stats.txt 2>&1
```

Then, on your Mac, in a new Terminal window (**Cmd+N**), don't type `ssh`:

```bash
scp root@SERVER_IP:pilot-stats.txt ~/Desktop/ && open ~/Desktop/pilot-stats.txt
```

Copy everything (**Cmd+A**, **Cmd+C**) and paste it to me. It contains only numbers.

### Step 5. Put the API keys on the server

```bash
nano deploy/analysis/analysis.env
```

An editor opens. Type these lines, replacing the parts after `=` with your keys (leave out the second line if you have no Gemini key):

```
SB_ANALYSIS_ANALYSIS__PROVIDERS__ANTHROPIC__API_KEY=sk-ant-your-key
SB_ANALYSIS_ANALYSIS__PROVIDERS__GEMINI__API_KEY=your-gemini-key
```

Save with **Ctrl+O**, then **Enter**, and close with **Ctrl+X**. Then make the file private:

```bash
chmod 600 deploy/analysis/analysis.env
```

### Step 6. The automatic quality check (about $3-5 and 30-45 minutes)

**1.** Create a private folder for the test data. It stays on this server and never goes to GitHub.

```bash
mkdir -p /srv/securebrand/gold && chown 65532:65532 /srv/securebrand/gold && chmod 700 /srv/securebrand/gold
```

**2.** Draw 1,000 comments and 200 posts from the collected data:

```bash
make eval-sample EVAL_ARGS="--repeat 0"
```

**3.** The strongest AI (Opus) labels them (15-30 minutes):

```bash
make eval-silver
```

**4.** The everyday AI (Haiku) analyses the same items (a few minutes):

```bash
make eval-run MODEL=claude-haiku-5-5
```

**5.** Compare them:

```bash
make eval-report
```

Send me the tables it prints (numbers only). I then tell you whether Haiku is good enough or which part needs the stronger AI.

### Step 7. Switch the analysis on

```bash
make analysis-up
```

It prints `analysis running`. From now on, comments are analysed within hours (brand mentions within a minute), up to $5 a day.

To check progress and spending (in dollars per Iraq day), at any time:

```bash
make analysis-status
```

To switch it off (the collector keeps running):

```bash
make analysis-down
```

### Everyday commands (optional)

**Accounts flagged as bots, with the reasons:**

```bash
make analysis-cmd ARGS="authors list"
```

**Mark an account as not a bot** (copy its key from the list; your name and the reason go into the log):

```bash
make analysis-cmd ARGS='authors mark --key PASTE_KEY --as human --by "Your name" --note "why"'
```

**The log of those decisions:**

```bash
make analysis-cmd ARGS="authors log"
```

**Find a topic and count its posts** (English or Arabic):

```bash
make analysis-cmd ARGS='topics find "electricity cuts"'
```

**Topics the system added:**

```bash
make analysis-cmd ARGS="topics list --status active"
```

### Updating the analysis later

```bash
cd ~/securebrand-analysis && git pull && make analysis-image
```

Then restart it on the new version:

```bash
make analysis-up
```
