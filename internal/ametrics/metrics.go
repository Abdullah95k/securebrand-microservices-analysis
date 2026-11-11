// Package ametrics re-exports sb-analysis's Prometheus metrics
// (sb_analysis_*), defined in internal/platform/metrics/analysis.go.
package ametrics

import "github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/metrics"

var (
	Ingested          = metrics.AnalysisIngested
	Undecodable       = metrics.AnalysisUndecodable
	IngestPaused      = metrics.AnalysisIngestPaused
	IngestPostsPaused = metrics.AnalysisIngestPostsPaused
	Analyzed          = metrics.AnalysisAnalyzed
	ItemsFailed       = metrics.AnalysisItemsFailed
	Requests          = metrics.AnalysisRequests
	RequestSecs       = metrics.AnalysisRequestSecs
	Tokens            = metrics.AnalysisTokens
	CostUSD           = metrics.AnalysisCostUSD
	Fallbacks         = metrics.AnalysisFallbacks
	Repairs           = metrics.AnalysisRepairs
	Refusals          = metrics.AnalysisRefusals
	RateFallback      = metrics.AnalysisRateFallback
	FollowUps         = metrics.AnalysisFollowUps
	ToppedUp          = metrics.AnalysisToppedUp
	Deduplicated      = metrics.AnalysisDeduplicated
	DeadLetters       = metrics.AnalysisDeadLetters
	QueueDepth        = metrics.AnalysisQueueDepth
	QueueAge          = metrics.AnalysisQueueAge
	SpentToday        = metrics.AnalysisSpentToday
	BudgetUSD         = metrics.AnalysisBudgetUSD
	BudgetPaused      = metrics.AnalysisBudgetPaused
	OpenBatches       = metrics.AnalysisOpenBatches
	Media             = metrics.AnalysisMedia
	MediaAtRisk       = metrics.AnalysisMediaAtRisk
	MediaPending      = metrics.AnalysisMediaPending
	Transcribed       = metrics.AnalysisTranscriptions
	STTDeferred       = metrics.AnalysisSTTDeferred
	OutboxBacklog     = metrics.AnalysisOutboxBacklog
	Published         = metrics.AnalysisPublished
	PublishFailed     = metrics.AnalysisPublishFailed
	OutboxHeld        = metrics.AnalysisOutboxHeld
	Erasures          = metrics.AnalysisErasures
	RollupDays        = metrics.AnalysisRollupDays
)
