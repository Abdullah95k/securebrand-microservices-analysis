// Package ametrics holds the analysis service's Prometheus metrics
// (fbs_analysis_*), registered on the process registry.
package ametrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/abdullah95k/securebrand-microservices/internal/platform/metrics"
)

func counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "fbs", Subsystem: "analysis", Name: name, Help: help}, labels)
	metrics.Registry.MustRegister(c)
	return c
}

func gaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "fbs", Subsystem: "analysis", Name: name, Help: help}, labels)
	metrics.Registry.MustRegister(g)
	return g
}

func gauge(name, help string) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "fbs", Subsystem: "analysis", Name: name, Help: help})
	metrics.Registry.MustRegister(g)
	return g
}

func histVec(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "fbs", Subsystem: "analysis", Name: name, Help: help, Buckets: buckets}, labels)
	metrics.Registry.MustRegister(h)
	return h
}

var (
	// Ingest.
	Ingested    = counterVec("items_ingested_total", "Items ingested by platform, kind and outcome (new, edited, unchanged, skipped, deleted, redacted, suppressed).", "platform", "outcome")
	Undecodable = counterVec("records_undecodable_total", "Input records that could not be decoded.", "topic")

	// Analysis.
	Analyzed    = counterVec("items_analyzed_total", "Items with a stored analysis, by route, model and outcome (written, stale, duplicate).", "route", "model", "outcome")
	ItemsFailed = counterVec("items_failed_total", "Items not analysed by a request, by route and reason (missing, invalid, refused, error).", "route", "reason")
	Requests    = counterVec("provider_requests_total", "Provider requests by provider, model, lane and outcome.", "provider", "model", "lane", "outcome")
	RequestSecs = histVec("provider_request_seconds", "Real-time provider request latency.", prometheus.ExponentialBuckets(0.25, 2, 10), "provider", "model")
	Tokens      = counterVec("tokens_total", "Tokens by model, route and kind (input, output, cache_read, cache_write).", "model", "route", "kind")
	CostUSD     = counterVec("cost_usd_total", "Spend in USD at list prices, by model and route.", "model", "route")
	Fallbacks   = counterVec("fallback_total", "Requests served by a route's fallback provider.", "route")
	Repairs     = counterVec("output_repairs_total", "Values validation had to fix or drop (invalid_topic, private_person, fixed_value, duplicate_item, unexpected_item).", "route", "kind")
	Refusals    = counterVec("refusals_total", "Requests the model refused.", "route", "model")
	FollowUps   = counterVec("follow_ups_total", "Items queued for escalation or audit.", "route")
	DeadLetters = counterVec("dead_letters_total", "Items given up after max attempts.", "route")

	// Queue and budget.
	QueueDepth   = gaugeVec("queue_depth", "Queued items by lane and route.", "lane", "route")
	QueueAge     = gaugeVec("queue_oldest_seconds", "Age of the oldest queued item by lane and route.", "lane", "route")
	SpentToday   = gauge("spent_today_usd", "Spend today (analysis.timezone), actual where known, estimated in flight.")
	BudgetUSD    = gauge("budget_daily_usd", "Daily budget (0 = none).")
	BudgetPaused = gauge("budget_paused", "1 while the budget holds non-urgent work back.")
	OpenBatches  = gauge("open_batches", "Provider batches in progress.")

	// Media.
	Media       = counterVec("media_total", "Media jobs by kind and outcome (analyzed, skipped, failed_retry, failed_permanent).", "kind", "outcome")
	MediaAtRisk = gauge("media_ttl_at_risk", "Pending media jobs within 6h of deletion.")

	// Outbox, erasure, rollups.
	OutboxBacklog = gauge("outbox_backlog", "Records waiting in the outbox.")
	Published     = counterVec("published_total", "Records published from the outbox, by topic.", "topic")
	Erasures      = counterVec("erasures_total", "Erasure requests processed by subject.", "subject")
	RollupDays    = counterVec("rollup_days_total", "Rollup days recomputed.")
)
