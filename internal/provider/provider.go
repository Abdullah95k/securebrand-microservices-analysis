// Package provider abstracts LLM providers: one request returns one JSON
// answer validated against a schema. Implementations: anthropic (official
// Go SDK, Message Batches), openaicompat (OpenAI, Gemini's compatible
// endpoint, self-hosted vLLM or Whisper servers) and fake (deterministic,
// for tests and the local stack).
package provider

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Image is an image attached to a request.
type Image struct {
	MediaType string // image/jpeg, image/png, ...
	Data      []byte
}

// Request is one model call.
type Request struct {
	// ID identifies the request (the custom id inside a batch).
	ID    string
	Model string
	// System is the static instruction prefix; providers cache it.
	System     string
	User       string
	Images     []Image
	Schema     map[string]any
	SchemaName string
	MaxTokens  int
	// Effort: low | medium | high | "" (provider default).
	Effort string
	// Thinking: off | adaptive.
	Thinking string
}

// Usage is the provider-reported token usage.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

// Add sums usages.
func (u Usage) Add(o Usage) Usage {
	return Usage{u.InputTokens + o.InputTokens, u.OutputTokens + o.OutputTokens,
		u.CacheReadTokens + o.CacheReadTokens, u.CacheWriteTokens + o.CacheWriteTokens}
}

// Response is one model answer.
type Response struct {
	ID         string
	Text       string
	Usage      Usage
	StopReason string
	// Refused: the model declined (safety classifiers).
	Refused bool
	// Truncated: the answer hit max_tokens.
	Truncated bool
	Model     string
	Latency   time.Duration
}

// Provider runs real-time requests.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (*Response, error)
}

// BatchResult is one request's outcome inside a finished batch. Err is set
// when the request errored, expired or was cancelled: retry it.
type BatchResult struct {
	ID       string
	Response *Response
	Err      error
}

// Batcher runs requests through a provider's batch API (cheaper, slower).
type Batcher interface {
	SubmitBatch(ctx context.Context, reqs []Request) (string, error)
	// BatchDone reports whether the batch finished processing.
	BatchDone(ctx context.Context, batchID string) (bool, error)
	BatchResults(ctx context.Context, batchID string) ([]BatchResult, error)
}

// BatchStatus is a provider batch's state as the provider reports it: no
// content, only counts and times.
type BatchStatus struct {
	Status                                            string
	Processing, Succeeded, Errored, Canceled, Expired int64
	CreatedAt, EndedAt, ExpiresAt                     time.Time
}

// BatchStatuser reports a batch's state (sb-analysis batches).
type BatchStatuser interface {
	BatchStatus(ctx context.Context, batchID string) (BatchStatus, error)
}

// Transcriber turns speech into text.
type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte, filename, language string) (string, error)
}

// Error classifies provider failures for retries and circuit breakers.
type Error struct {
	Status int
	// Retryable: rate limits, overload, server and network errors.
	Retryable bool
	Err       error
}

func (e *Error) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("provider: status %d: %v", e.Status, e.Err)
	}
	return fmt.Sprintf("provider: %v", e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// Retryable reports whether err is worth retrying later. Unknown errors
// count as retryable (network trouble); only a provider's explicit
// rejection of the request (4xx other than 408/409/429) is not.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Retryable
	}
	return !errors.Is(err, context.Canceled)
}

// StatusRetryable classifies an HTTP status.
func StatusRetryable(status int) bool {
	return status == 408 || status == 409 || status == 429 || status >= 500
}
