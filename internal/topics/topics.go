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

// The platform's contract (github.com/abdullah95k/securebrand-contracts,
// registry/topics.yaml): what sb-analysis reads (analysis.inputs:
// [content]) and the results it publishes. The platform creates them all.
const (
	ContentPost           = "content.post.v1"
	ContentComment        = "content.comment.v1"
	ContentMediaReady     = "content.media.ready.v1"
	ContentMediaProcessed = "content.media.processed.v1"
	ContentMediaDeleted   = "content.media.deleted.v1"
	AnalysisResult        = "analysis.result.v1"
	AnalysisMedia         = "analysis.media.v1"
	AnalysisAuthor        = "analysis.author.v1"
	AnalysisTopic         = "analysis.topic.v1"
)
