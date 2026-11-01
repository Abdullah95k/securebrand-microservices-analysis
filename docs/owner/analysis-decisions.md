# Content analysis: what you need to decide, and what to run

*For the owner. Plain language. All times are Iraq time.*

## What was built

A new service, **sb-analysis**, reads every post and comment the collector saves and works out:
- the language and dialect;
- whether the author is positive, negative, neutral or mixed;
- which brands, organisations, public people and places are mentioned, and what the author thinks of each one;
- the topics;
- whether it is a complaint, a question, praise, spam, toxic, or urgent.

It does the same for images and videos (logos, text in the picture, what is shown, and speech turned into text). The files are deleted afterwards; only the results are kept.

It runs on Claude (Anthropic's AI). Most comments are sent in bulk at half price and come back within hours. Comments that mention a brand you track are analysed within a minute.

**It is built and tested, but it is switched off on the pilot server** until you decide question 1 below.

## Cost

At list prices, checked 1 November 2026. Estimates until the test with your labeller measures the real numbers.

| Text items per year | Cheapest setup (Haiku everywhere) | Stronger setup (Sonnet for posts, images, videos) |
|---|---|---|
| 100 million | about $5,000 | about $16,000 |
| 300 million | about $15,000 | about $48,000 |
| 1 billion | about $50,000 | about $160,000 |

Server and storage costs come on top: roughly 330 GB of database storage a year at 300 million items.

## Your decisions

Each has a default. If you do not answer, I continue with the default.

1. **May comments be sent to an outside AI company (Anthropic)?**
   - *Default:* yes, after your lawyer agrees. Only paid accounts are used. Phone numbers and e-mails are hidden, and no author names or ids are sent.
   - *Alternative:* run our own AI servers, about $44,000+ a year plus an engineer, and less accurate.
2. **Daily spending limit.**
   - *Default:* $5 a day on the pilot (about $150 a month). The service stops non-urgent work at the limit and continues the next day at midnight.
   - *For production:* set after the test.
3. **Speed.**
   - *Default:* most comments within hours (half price), brand mentions within a minute. Do you need everything within minutes? That doubles the price.
4. **Topic list.**
   - *Default:* the list I proposed: 20 areas and 87 topics, in Arabic and English, in `configs/analysis/taxonomy.v1.yaml` (telecom, electricity, prices, health, religion, sports...).
   - Send me changes or your own list.
5. **Brand list.**
   - *Default:* 45 public Iraqi companies, ministries and governorates, in `configs/analysis/entities.seed.yaml`.
   - Please send each client's brands and competitors, with every spelling you know (Arabic, English, and Arabizi such as "asiacell", "asia cell", "آسيا سيل").
6. **People.**
   - *Default:* public figures (politicians, officials, celebrities) are tracked after you approve them. Private people, such as friends tagged in comments, are never stored.
7. **Keeping comments forever.**
   - Done as you decided: the collector now keeps comments forever.
   - Note that there are now **two copies** of every comment kept forever: the collector's and the analysis service's.
   - *Default:* keep both for now. *I recommend* keeping only the analysis copy later. Your call, with your lawyer.
8. **The labeller.**
   - Who, and when can they start? About 3-4 working days for 1,000 comments, 200 posts and some images.
9. **Quality bar.**
   - *Default:* the AI must agree with the labeller on at least 75% of sentiment (balanced score) and 65% of the opinion about tracked brands. The cheapest model that passes is chosen.
10. **Backup AI company.**
    - *Default:* none until you add a second key.
    - *I recommend* adding Google (Gemini) or OpenAI, so analysis continues if Anthropic has an outage.

## For your lawyer (I flag these, I do not decide them)

- **Outside AI companies** process the comments: you need their data-processing agreements and their data-retention terms (Anthropic can offer zero retention on request).
- **Keeping comments forever** is hard to justify under data-protection rules for private people's comments. You also have two copies.
- **Opinions about politicians and religion,** linked to a (pseudonymous) author, are "special category" data in GDPR terms. You need a data-protection impact assessment.
- **People mentioned in comments:** we keep only public figures.
- **Iraq** has no data-protection law in force yet. Your clients' countries may apply theirs.

## Steps on the pilot server

Run these on the DigitalOcean server, in the project folder:

```bash
cd ~/securebrand-microservices
```

First get this work onto the server (it is in the `securebrand-microservices-analysis` repository on GitHub, branch `claude/jolly-bell-mb5sct`):

```bash
git fetch https://github.com/Abdullah95k/securebrand-microservices-analysis claude/jolly-bell-mb5sct && git checkout FETCH_HEAD
```

Then build the image with the new tools. This does **not** restart or change the running pilot:

```bash
make docker
```

(Once this work is merged into the main repository, go back with `git checkout main && git pull --ff-only origin main`.)

**Step 1. Measure the pilot data (safe: counts only, no text).** Send me what it prints. It tells me how much Kurdish, Arabizi and duplicate text there is, and how long comments are.

```bash
make pilot-stats
```

**Step 2. Answer the decisions above** (a short reply is enough, e.g. "1 yes after lawyer, 2 $5, 3 ok...").

**Step 3. Put the API key on the server.** Never paste it in chat or e-mail.
1. In the Anthropic Console (console.anthropic.com → API keys), create a key, and set a monthly spend limit there too.
2. On the server, create the secret file. When the editor opens, type one line: `FBS_ANALYSIS__PROVIDERS__ANTHROPIC__API_KEY=` followed by your key, then save with Ctrl+O, Enter, and exit with Ctrl+X:

```bash
nano deploy/compose/analysis.env
```

3. Make the file private (only you can read it):

```bash
chmod 600 deploy/compose/analysis.env
```

For a backup provider, add a second line in the same file: `FBS_ANALYSIS__PROVIDERS__GEMINI__API_KEY=...` or `FBS_ANALYSIS__PROVIDERS__OPENAI__API_KEY=...`.

**Step 4. The labeller's test** (when the labeller is ready). Follow [docs/analysis/evaluation.md](../analysis/evaluation.md). In short:

```bash
mkdir -p /srv/securebrand/gold && chown 65532:65532 /srv/securebrand/gold && chmod 700 /srv/securebrand/gold
```

```bash
make eval-sample
```

```bash
make eval-label
```

The labeller opens the page through an SSH tunnel; the guide for them is [docs/analysis/labelling-guide.md](../analysis/labelling-guide.md). Afterwards I give you the commands to compare the AI models. Their report contains only numbers, which you send me.

**Step 5. Switch the analysis on** (only after your answer to question 1):

```bash
make pilot-analysis-up
```

```bash
make pilot-analysis-status
```

The status shows how many items are analysed and what was spent per day (in dollars, Iraq dates). To switch it off:

```bash
make pilot-analysis-down
```
