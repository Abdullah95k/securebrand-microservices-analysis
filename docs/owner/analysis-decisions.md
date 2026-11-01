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

## Your decisions (answered 1 November 2026)

| # | Question | Your answer | What the system does |
|---|---|---|---|
| 1 | Send comments to Anthropic? | Yes | Analysis uses Claude. Phone numbers, e-mails and author names never leave. Your lawyer should still check the data-processing terms. |
| 2 | Daily spending limit | Set after the test | $5 a day on the pilot until then. |
| 3 | Everything within minutes? | No, within hours is fine | Bulk at half price; brand mentions within a minute. |
| 4 | Topic list | Fine, if missing topics are added automatically | New topics are proposed by the AI, checked for duplicates, and approved by a second AI every 6 hours once 5 posts use them. You can step in at any time. |
| 5 | Brand list | Fine, if missing ones are added automatically | New brands, organisations, public figures, positions and places found 5+ times are approved by the AI every 6 hours; spelling variants are merged into the known one. |
| 6 | People | Public accounts stored | Public figures and public accounts are stored automatically. Private people are never stored. When the AI is unsure whether someone is public, it does not store them. |
| 7 | Two copies of comments | Keep both for now | Both kept. |
| 8-9 | Labeller and quality bar | No labeller; rely on the AI | The strongest AI (Opus) labels 1,200 real items once, about $3-5, and Haiku is measured against it (step 6 below). Production keeps checking 1 in 5,000 items with Opus. |
| 10 | Backup AI company | You asked me to choose | **Google Gemini** (reasons below). It is already set up on the pilot as the backup, and starts working as soon as you add its key (step 5). |
| 11 | Legal flags | Religion: always flag criticism | Insults, mockery or attacks on any religion, sect, sacred text, symbol or religious figure are always flagged. A respectful question or disagreement, someone stating their own belief, and criticism of a cleric's *political* actions are not flagged on their own. Iraqi law punishes insulting religion, not disagreement, and many politicians are clerics. Peaceful political criticism and a person's sexuality are not flagged on their own; hate speech, threats, defamation, misinformation and the rest always are. |
| 12 | Bot flags | Show them; users can mark "not a bot", with a log | Bot flags are shown on every item of the account. Anyone you allow can mark an account "not a bot" (or "is a bot"). Each decision is logged with who, when, why and the before/after. |
| 13 | New topics | An AI approves; a person only steps in if needed | As in 4. |
| 14 | Very large posts | Every comment | Every comment is analysed. |

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

### Step 2. Go to the project and check the pilot is running

```bash
cd ~/securebrand-microservices
```

```bash
docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/pilot.yml ps
```

You should see `crawler`, `persister`, `scheduler` and the other services as **running** (or **Up**). If not, tell me before going on.

Install `tmux`, so long steps keep running if your connection drops:

```bash
apt-get install -y tmux
```

```bash
tmux new -s work
```

If you get disconnected later, reconnect (step 1) and type `tmux attach -t work` to return to where you were.

### Step 3. Get this work and build it

This version has the latest collector code plus the analysis service.

```bash
git fetch https://github.com/Abdullah95k/securebrand-microservices-analysis claude/jolly-bell-mb5sct
```

```bash
git checkout FETCH_HEAD
```

```bash
make docker
```

The build takes 5-15 minutes and ends with `naming to docker.io/library/fbs:dev`. It does not restart the running collector.

### Step 4. Measure the data (counts only, safe to share)

```bash
make pilot-stats
```

Copy everything it prints and send it to me. It contains only numbers.

### Step 5. Put the API keys on the server

```bash
nano deploy/compose/analysis.env
```

An editor opens. Type these lines, replacing the parts after `=` with your keys (leave out the second line if you have no Gemini key):

```
FBS_ANALYSIS__PROVIDERS__ANTHROPIC__API_KEY=sk-ant-your-key
FBS_ANALYSIS__PROVIDERS__GEMINI__API_KEY=your-gemini-key
```

Save with **Ctrl+O**, then **Enter**, and close with **Ctrl+X**. Then make the file private:

```bash
chmod 600 deploy/compose/analysis.env
```

### Step 6. The automatic quality check (about $3-5 and 30-45 minutes)

**1.** Create a private folder for the test data. It stays on this server and never goes to GitHub.

```bash
mkdir -p /srv/securebrand/gold && chown 65532:65532 /srv/securebrand/gold && chmod 700 /srv/securebrand/gold
```

**2.** Draw 1,000 comments and 200 posts from the pilot data:

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
make pilot-analysis-up
```

It prints `analysis running`. From now on, comments are analysed within hours (brand mentions within a minute), up to $5 a day.

To check progress and spending (in dollars per Iraq day), at any time:

```bash
make pilot-analysis-status
```

To switch it off:

```bash
make pilot-analysis-down
```

### Everyday commands (optional)

**Accounts flagged as bots, with the reasons:**

```bash
make pilot-analysis-cmd ARGS="authors list"
```

**Mark an account as not a bot** (copy its key from the list; your name and the reason go into the log):

```bash
make pilot-analysis-cmd ARGS='authors mark --key PASTE_KEY --as human --by "Your name" --note "why"'
```

**The log of those decisions:**

```bash
make pilot-analysis-cmd ARGS="authors log"
```

**Find a topic and count its posts** (English or Arabic):

```bash
make pilot-analysis-cmd ARGS='topics find "electricity cuts"'
```

**Topics the system added:**

```bash
make pilot-analysis-cmd ARGS="topics list --status active"
```

### Later

When this work is merged into the main repository, return the server to the main branch:

```bash
git checkout main && git pull --ff-only origin main
```
