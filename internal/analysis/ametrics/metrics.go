// Package ametrics re-exports the analysis service's Prometheus metrics
// (fbs_analysis_*), defined in internal/platform/metrics/analysis.go.
package ametrics

import "github.com/abdullah95k/securebrand-microservices/internal/platform/metrics"

var (
	Ingested      = metrics.AnalysisIngested
	Undecodable   = metrics.AnalysisUndecodable
	Analyzed      = metrics.AnalysisAnalyzed
	ItemsFailed   = metrics.AnalysisItemsFailed
	Requests      = metrics.AnalysisRequests
	RequestSecs   = metrics.AnalysisRequestSecs
	Tokens        = metrics.AnalysisTokens
	CostUSD       = metrics.AnalysisCostUSD
	Fallbacks     = metrics.AnalysisFallbacks
	Repairs       = metrics.AnalysisRepairs
	Refusals      = metrics.AnalysisRefusals
	FollowUps     = metrics.AnalysisFollowUps
	DeadLetters   = metrics.AnalysisDeadLetters
	QueueDepth    = metrics.AnalysisQueueDepth
	QueueAge      = metrics.AnalysisQueueAge
	SpentToday    = metrics.AnalysisSpentToday
	BudgetUSD     = metrics.AnalysisBudgetUSD
	BudgetPaused  = metrics.AnalysisBudgetPaused
	OpenBatches   = metrics.AnalysisOpenBatches
	Media         = metrics.AnalysisMedia
	MediaAtRisk   = metrics.AnalysisMediaAtRisk
	OutboxBacklog = metrics.AnalysisOutboxBacklog
	Published     = metrics.AnalysisPublished
	Erasures      = metrics.AnalysisErasures
	RollupDays    = metrics.AnalysisRollupDays
)
