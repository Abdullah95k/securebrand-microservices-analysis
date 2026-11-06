# Message for the master session, from the analysis session (6 November 2026)

From the analysis session (sb-analysis, repository `Abdullah95k/securebrand-microservices-analysis`).

I've started REQ-011 (the fixes). REQ-022 (adopting contract v1.0.0) comes after the fixes REQ-011 lists before the canary. The owner decided today:

- the quality evaluation runs on the production cluster, once fbs collects there;
- the canary on production gets a $5 daily cap on AI spending.

I have three questions. None of them blocks fix 1 (a repository of its own), which I'm working on now.

1. **Images, chart and namespace.**
   - Where should sb-analysis publish its images (`sb-analysis:<version>` and the media image) and its Helm chart? For example `ghcr.io/abdullah95k/sb-analysis`, with the chart as an OCI artifact under `ghcr.io/abdullah95k/charts/`.
   - What is sb-analysis's namespace on the production cluster? The chart will read the secrets `platform-credentials` and `ai-provider` there, and it creates no topics and registers no schemas.

2. **Hot-tier accounts (REQ-011 fix 8, the real-time lane).**
   - Where does sb-analysis learn an account's *current* tier? `registry.account_request.v1` carries requests (add, update, pause, each with a tier), not each account's current tier. Is there, or will there be, a state topic or table for it?
   - Until sb-analysis adopts the contract, it reads `fb.*` from fbs. fbs's tiers (T0–T3) are set by how often a page posts, not by client interest, so they don't mean "hot". My proposal: until adoption, only items that mention a tracked brand or entity go to the real-time lane, and hot-tier accounts join it with the contract. Is that acceptable?

3. **Backfill (REQ-011 fix 9, plan only).** To re-analyse items older than Kafka's 7 days, which permanent store should sb-analysis read: ClickHouse, or the event archive in object storage?
   - If the archive: which bucket (`S3_BUCKET_RAW`?), what file layout and format, and is there a read-only role for it?
   - If ClickHouse: which tables, and a read-only user?

Please answer by messaging this session directly, or through the owner.
