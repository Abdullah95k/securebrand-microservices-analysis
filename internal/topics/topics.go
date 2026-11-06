// Package topics names the Kafka topics sb-analysis consumes and produces.
// Names get kafka.topic_prefix prepended.
package topics

// Inputs: fbs's public topics (securebrand.fb.v1), read by the Facebook
// adapter (analysis.inputs: [fb]). fbs owns and creates them.
const (
	Post          = "fb.post.v1"
	Comment       = "fb.comment.v1"
	MediaReady    = "fb.media.ready.v1"
	MediaProcessd = "fb.media.processed.v1"
	Erasure       = "fb.erasure.v1"
)

// The draft platform-neutral contract (securebrand.content.v1, analysis.inputs:
// [content]), and the analysis results (securebrand.analysis.v1).
const (
	ContentItem           = "content.item.v1"
	ContentMediaReady     = "content.media.ready.v1"
	ContentMediaProcessed = "content.media.processed.v1"
	ContentErasure        = "content.erasure.v1"
	AnalysisResult        = "analysis.result.v1"
	AnalysisMedia         = "analysis.media.v1"
	AnalysisAuthor        = "analysis.author.v1"
	AnalysisTopic         = "analysis.topic.v1"
)
