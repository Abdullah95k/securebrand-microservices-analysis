# Evaluating models on Iraqi content (sb-eval)

No model is chosen without measurement. `sb-eval` runs any configured provider and model on a gold set, labelled by an Iraqi native speaker or, as the owner chose for the pilot, by the strongest model (Claude Opus 5.5, "silver" labels; section 2b). It uses the **same prompt, output schema and validation code as production**, and reports:
- **per-task accuracy:**
  - sentiment macro-F1 with 95% bootstrap intervals;
  - aspect sentiment (overall and on tracked brands);
  - entities and linking to the registry;
  - topics; intent, emotion, toxicity, spam, sarcasm, urgency; language and dialect;
  - ads (is it an ad, and which type) and possible legal or policy breaches (any, and by category);
- **measured cost** per 1,000 items from real token counts (Arabic tokenizes differently from English);
- **latency, refusals and errors.**

The gold set is real personal data. It lives in `/srv/securebrand/gold` on the pilot server (mode 700), never in git. `sb-eval sample` refuses to write inside a git checkout, `.gitignore` blocks `*.labels.jsonl`, and the repository holds only `testdata/analysis/synthetic-gold-example.jsonl` (invented texts).

Every command below runs **on the pilot server** (the DigitalOcean droplet), in the analysis's own checkout `~/securebrand-analysis`, from the image that `make analysis-image` builds (`sb-analysis:dev`). Go is not needed, and fbs's folder and containers are not touched.

## 1. Draw the gold set (once)

```bash
mkdir -p /srv/securebrand/gold && chown 65532:65532 /srv/securebrand/gold && chmod 700 /srv/securebrand/gold
```

```bash
make eval-sample
```

This writes `/srv/securebrand/gold/gold.labels.jsonl` with:
- **1,000 comments**, spread over pages and lengths, with brand mentions (25%) and Kurdish or Latin-script comments (10%) oversampled;
- **200 posts**, spread over pages and post types;
- **10% repeated copies** at the end, labelled again blind to measure consistency.

Items carry opaque ids, their text and their context (page name, post, parent comment). No author data is included. Change sizes with `make eval-sample EVAL_ARGS="--comments 1500 --posts 300"`.

**Images (optional, ~100):** the pilot downloads no media. To add images, put JPEG/PNG files in `/srv/securebrand/gold/images/` and add lines like this to the file:
```
{"id":"img-001","kind":"image","image_path":"/gold/images/001.jpg","post":"optional post text"}
```

## 2. Label

On the server:
```bash
make eval-label
```

On the labeller's computer, open a tunnel. The tool only listens on the server's own 127.0.0.1:
```bash
ssh -N -L 8099:127.0.0.1:8099 root@<server-ip>
```

Then open http://127.0.0.1:8099 in a browser. The page shows one item at a time; every **Save and next** writes the file. Stop with Ctrl+C and start again later: it continues at the first unlabelled item.
- **The guide for the labeller:** [labelling-guide.md](labelling-guide.md).
- **Giving the labeller access without full shell access:** create a user whose key allows only port forwarding (`restrict,port-forwarding,permitopen="127.0.0.1:8099"` in `authorized_keys`).

**Consistency,** once the repeated items at the end are done:
```bash
make eval-agreement
```

A sentiment kappa above 0.6 is good and above 0.8 very good. Lower means the guide needs clarifying before the labels are trusted.

## 2b. Labels from the strongest model instead (the owner's choice)

Without a labeller, Claude Opus 5.5 (adaptive thinking, effort medium) labels the sample. Every other model is then measured by its agreement with Opus:

```bash
make eval-silver
```

- **Cost and time:** about $3-5 and 15-30 minutes for 1,200 items (4 requests at a time).
- **Labels:** each label records `labeller: model:claude-opus-5-5`.
- **What it measures:** agreement with Opus, not truth. It cannot show where Opus itself is wrong on Iraqi dialect (sarcasm, slang, local brands). A cheaper model scoring close to Opus is still a strong sign it is good enough. Production keeps checking the same way: the 0.02% audit sample is Opus too.
- **Unanswered items:** items Opus refused or missed stay unlabelled and are left out of the scores.

## 3. Run models

API keys go in `deploy/analysis/analysis.env` (see [the owner's steps](../owner/analysis-decisions.md)). Each run appends to `/srv/securebrand/gold/results.jsonl`:

```bash
make eval-run MODEL=claude-haiku-5-5
```

```bash
make eval-run MODEL=claude-sonnet-5-5
```

```bash
make eval-run MODEL=claude-opus-5-5 EVAL_ARGS="--thinking adaptive --effort medium"
```

```bash
make eval-run PROVIDER=gemini MODEL=gemini-3.1-flash-lite EVAL_ARGS="--effort low"
```

```bash
make eval-run PROVIDER=openai MODEL=gpt-5.4-nano EVAL_ARGS="--effort low"
```

Useful options (in `EVAL_ARGS`):
- `--group 10|25|40`: comments per request. Larger is cheaper; measure whether accuracy drops.
- `--thinking off|adaptive`, `--effort low|medium|high`: Haiku 5.5 thinks by default; off is the production default for comments.
- `--batch`: through the Batch API at half price. Results can take up to 24 hours, so let the command run, e.g. in `tmux`.
- `--limit 100`: a cheap first check.

**Run cost:** a full run of 1,200 items costs cents on Haiku and under ~$2 on Opus at list prices.

## 4. Report and choose

```bash
make eval-report
```

The report prints one row per configuration with every score (in %), its confidence interval, the measured cost per 1,000 items, tokens per item, latency, refusals and errors.

**How to choose, route by route:**
1. Find the best sentiment macro-F1 and its 95% interval.
2. Among the configurations whose sentiment score falls inside that interval and that pass the bar (proposed: macro-F1 ≥ 75, tracked aspect F1 ≥ 65, refusals < 1%), take the **cheapest**.
3. Check aspect F1 on tracked brands and linking: these matter most for brand monitoring.
4. Put the choice in `analysis.routes` and record the reason in the PR.

**Yearly cost with the measured tokens:**
```bash
make eval-cost MODEL=claude-haiku-5-5
```

It prints every mix, including `lean` (speech on a per-minute API) and `lean+whisper` (speech on an own server), and the cost by line. Send me the output together with the share of duplicate comments from `make pilot-stats`.

**Calibration:** fit the confidence map that drives escalation, and copy the output into `analysis.calibration_dir`:
```bash
docker run --rm -v /srv/securebrand/gold:/gold --entrypoint /usr/local/bin/sb-eval sb-analysis:dev calibrate --gold /gold/gold.labels.jsonl --model claude-haiku-5-5 /gold/results.jsonl
```

## Results (to be filled in after the runs)

| Route | Model | Sentiment F1 (CI) | Tracked aspect F1 | Topic F1 | USD / 1k items | Chosen |
|---|---|---|---|---|---|---|
| comment | | | | | | |
| post | | | | | | |
| image | | | | | | |

The recommendation, with the measured yearly cost at 100M / 300M / 1B items, goes in the design document (section 16) once these numbers exist.

## Speech-to-text (videos)

Iraqi-dialect transcription quality is unknown, so measure it before choosing:
1. Collect 20-30 short public videos with speech.
2. Write reference transcripts.
3. Compare the word error rate of `gpt-4o-mini-transcribe` (OpenAI-compatible), Gemini 3.5 Transcribe, and an own Whisper large-v3-turbo server (faster-whisper with an OpenAI-compatible API; provider `whisper` in `configs/default.yaml`).

Above ~150M text items a year the own server is the cheaper option (~$2k a year against ~$3.9k on Gemini 3.5 Transcribe at 300M items, ~$13k at 1B; design section 9). It is chosen only if its word error rate is close to the best API's. This harness does not automate the comparison yet.
