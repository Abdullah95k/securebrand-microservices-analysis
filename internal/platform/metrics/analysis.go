package metrics

// The analysis's own metrics (sb_analysis_*); internal/ametrics re-exports
// them.

import "github.com/prometheus/client_golang/prometheus"

func analysisCounter(name, help string, labels ...string) *prometheus.CounterVec {
	return counterVec(name, help, labels...)
}

func analysisGaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	return gaugeVec(name, help, labels...)
}

func analysisGauge(name, help string) prometheus.Gauge { return gauge(name, help) }

func analysisHist(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	return histVec(name, help, buckets, labels...)
}

var (
	// Ingest.
	AnalysisIngested          = analysisCounter("items_ingested_total", "Items ingested by platform, kind and outcome (new, edited, unchanged, skipped, deleted, redacted, suppressed; adopted: analysed from fb.*, its first contract version published without a new AI call; not_sampled: a new comment outside analysis.comments.sample_share, counted, not stored; stale: a contract copy at or below the stored envelope.version, ignored).", "platform", "outcome")
	AnalysisUndecodable       = analysisCounter("records_undecodable_total", "Input records that could not be decoded.", "topic")
	AnalysisIngestPaused      = analysisGauge("ingest_paused", "1 while new comments wait in Kafka because the analysis queue is full (analysis.backpressure); posts keep flowing.")
	AnalysisIngestPostsPaused = analysisGauge("ingest_posts_paused", "1 while new posts wait in Kafka too, the queue being at its much higher post cap (analysis.backpressure.max_queue_posts).")

	// Analysis.
	AnalysisAnalyzed     = analysisCounter("items_analyzed_total", "Items with a stored analysis, by route, model and outcome (written, stale, duplicate).", "route", "model", "outcome")
	AnalysisItemsFailed  = analysisCounter("items_failed_total", "Items not analysed by a request, by route and reason (missing, invalid, refused, error).", "route", "reason")
	AnalysisRequests     = analysisCounter("provider_requests_total", "Provider requests by provider, model, lane and outcome.", "provider", "model", "lane", "outcome")
	AnalysisRequestSecs  = analysisHist("provider_request_seconds", "Real-time provider request latency.", prometheus.ExponentialBuckets(0.25, 2, 10), "provider", "model")
	AnalysisTokens       = analysisCounter("tokens_total", "Tokens by model, route and kind (input, output, cache_read, cache_write).", "model", "route", "kind")
	AnalysisCostUSD      = analysisCounter("cost_usd_total", "Spend in USD at list prices, by model and route.", "model", "route")
	AnalysisFallbacks    = analysisCounter("fallback_total", "Requests served by a route's fallback provider.", "route")
	AnalysisRepairs      = analysisCounter("output_repairs_total", "Values validation had to fix or drop (invalid_topic, private_person, fixed_value, duplicate_item, unexpected_item).", "route", "kind")
	AnalysisRefusals     = analysisCounter("refusals_total", "Requests the model refused.", "route", "model")
	AnalysisRateFallback = analysisCounter("rate_limit_fallback_total", "Requests limited per process because Valkey, which shares the limit, was unreachable.", "provider")
	AnalysisFollowUps    = analysisCounter("follow_ups_total", "Items queued for escalation or audit.", "route")
	AnalysisToppedUp     = analysisCounter("items_topped_up_total", "Batch items claimed to fill their thread's request (analysis.batch.top_up_items).", "route")
	AnalysisDeduplicated = analysisCounter("items_deduplicated_total", "Items that shared the answer of an identical item of the same thread and parent (not sent to the model).", "route")
	AnalysisDeadLetters  = analysisGauge("dead_letters", "Items given up after max attempts (analysis.dead_letters), waiting for replay-failed.")

	// Queue and budget.
	AnalysisQueueDepth   = analysisGaugeVec("queue_depth", "Queued items by lane and route.", "lane", "route")
	AnalysisQueueAge     = analysisGaugeVec("queue_oldest_seconds", "Age of the oldest queued item by lane and route.", "lane", "route")
	AnalysisSpentToday   = analysisGauge("spent_today_usd", "Spend today (analysis.timezone), actual where known, estimated in flight.")
	AnalysisBudgetUSD    = analysisGauge("budget_daily_usd", "Daily budget (0 = none).")
	AnalysisBudgetPaused = analysisGauge("budget_paused", "1 while the daily budget holds paid work back (urgent items too unless analysis.budget.urgent_exempt).")
	AnalysisBudgetHeld   = analysisCounter("budget_held_items_total", "Batch items a round claimed and gave back because the route's or the day's cap was reached.", "route")
	AnalysisOpenBatches  = analysisGauge("open_batches", "Provider batches in progress.")
	// The batch collector's liveness: when a poll last ran, and when a
	// batch was last collected (Unix seconds).
	AnalysisBatchLastPoll    = analysisGauge("batch_last_poll_timestamp_seconds", "When the batch collector last polled (Unix seconds).")
	AnalysisBatchLastCollect = analysisGauge("batch_last_collect_timestamp_seconds", "When a finished provider batch was last collected (Unix seconds).")

	// Media.
	AnalysisMedia          = analysisCounter("media_total", "Media jobs by kind and outcome (analyzed, reused, skipped, failed_retry, failed_permanent; waiting_for_post: put back until its post is stored; provider_unavailable: the provider's circuit was open, put back without a try; not_ready: not found within 15 minutes of its announcement, put back without a try; no_location: an announcement without a bucket or key, not queued).", "kind", "outcome")
	AnalysisMediaAtRisk    = analysisGauge("media_ttl_at_risk", "Pending media jobs within 6h of deletion.")
	AnalysisMediaPending   = analysisGaugeVec("media_pending", "Media jobs waiting to be analysed, by type (image, video).", "type")
	AnalysisTranscriptions = analysisCounter("transcriptions_total", "Speech-to-text calls by provider and outcome (ok, error; unavailable: no server reachable, the video comes back later without losing a try; frames_only: no slot or server near the video's deadline, analysed from its frames alone).", "provider", "outcome")
	AnalysisSTTDeferred    = analysisCounter("stt_deferred_total", "Videos put back for later because speech-to-text was busy (analysis.media.video.stt_concurrency) or no server could be reached.")

	// Outbox, erasure, rollups.
	AnalysisOutboxBacklog = analysisGauge("outbox_backlog", "Records waiting in the outbox.")
	AnalysisPublished     = analysisCounter("published_total", "Records published from the outbox, by topic.", "topic")
	AnalysisPublishFailed = analysisCounter("publish_failed_total", "Outbox records Kafka refused or didn't take, by topic; each waits and is tried again.", "topic")
	AnalysisOutboxHeld    = analysisGauge("outbox_held", "Outbox records waiting to be tried again after a failed publish.")
	AnalysisErasures      = analysisCounter("erasures_total", "Erasure requests processed by subject.", "subject")
	AnalysisRollupDays    = analysisCounter("rollup_days_total", "Rollup days recomputed.")
)
