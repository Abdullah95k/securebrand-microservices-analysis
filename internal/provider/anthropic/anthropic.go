// Package anthropic is the Claude provider, on the official Go SDK: Messages
// with JSON-schema structured output and a cached system prompt, and the
// Message Batches API for the batch lane.
package anthropic

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
)

// Provider calls Claude.
type Provider struct {
	name   string
	client sdk.Client
}

// Config configures the client. APIKey "" falls back to the SDK's own
// credential resolution (ANTHROPIC_API_KEY and friends).
type Config struct {
	Name       string
	APIKey     string
	BaseURL    string
	Timeout    time.Duration
	MaxRetries int
}

// New creates the provider.
func New(cfg Config) *Provider {
	opts := []option.RequestOption{option.WithMaxRetries(cfg.MaxRetries)}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.Timeout > 0 {
		opts = append(opts, option.WithRequestTimeout(cfg.Timeout))
	}
	name := cfg.Name
	if name == "" {
		name = "anthropic"
	}
	return &Provider{name: name, client: sdk.NewClient(opts...)}
}

// Name is the configured provider name.
func (p *Provider) Name() string { return p.name }

// parts are the pieces shared by real-time and batch requests.
type parts struct {
	system   []sdk.TextBlockParam
	messages []sdk.MessageParam
	output   sdk.OutputConfigParam
	thinking sdk.ThinkingConfigParamUnion
}

func build(req provider.Request) parts {
	blocks := make([]sdk.ContentBlockParamUnion, 0, len(req.Images)+1)
	for _, img := range req.Images {
		blocks = append(blocks, sdk.NewImageBlockBase64(img.MediaType, base64.StdEncoding.EncodeToString(img.Data)))
	}
	blocks = append(blocks, sdk.NewTextBlock(req.User))
	out := sdk.OutputConfigParam{}
	if req.Schema != nil {
		out.Format = sdk.JSONOutputFormatParam{Schema: req.Schema}
	}
	if req.Effort != "" {
		out.Effort = sdk.OutputConfigEffort(req.Effort)
	}
	return parts{
		// The system prompt is identical across requests: cache it.
		system:   []sdk.TextBlockParam{{Text: req.System, CacheControl: sdk.NewCacheControlEphemeralParam()}},
		messages: []sdk.MessageParam{sdk.NewUserMessage(blocks...)},
		output:   out,
		thinking: Thinking(req.Model, req.Thinking),
	}
}

// Thinking maps the route's thinking setting to what each model accepts.
// "off" is a disabled config on Haiku 5.5 and older models, between_tools
// (no thinking without tools) on Sonnet 5.5; Opus 5.5 and Fable models
// cannot disable thinking, so it is left to effort. "adaptive" is adaptive
// everywhere.
func Thinking(model, mode string) sdk.ThinkingConfigParamUnion {
	if mode == "adaptive" {
		return sdk.ThinkingConfigParamUnion{OfAdaptive: &sdk.ThinkingConfigAdaptiveParam{}}
	}
	if mode != "off" {
		return sdk.ThinkingConfigParamUnion{}
	}
	switch {
	case strings.Contains(model, "sonnet-5-5"):
		b := sdk.NewThinkingConfigBetweenToolsParam()
		return sdk.ThinkingConfigParamUnion{OfBetweenTools: &b}
	case strings.Contains(model, "opus-5-5"), strings.Contains(model, "fable"), strings.Contains(model, "mythos"):
		return sdk.ThinkingConfigParamUnion{}
	}
	return sdk.ThinkingConfigParamUnion{OfDisabled: &sdk.ThinkingConfigDisabledParam{}}
}

func maxTokens(req provider.Request) int64 {
	if req.MaxTokens > 0 {
		return int64(req.MaxTokens)
	}
	return 4096
}

// Complete runs one request.
func (p *Provider) Complete(ctx context.Context, req provider.Request) (*provider.Response, error) {
	b := build(req)
	start := time.Now()
	msg, err := p.client.Messages.New(ctx, sdk.MessageNewParams{
		Model:        req.Model,
		MaxTokens:    maxTokens(req),
		System:       b.system,
		Messages:     b.messages,
		OutputConfig: b.output,
		Thinking:     b.thinking,
	})
	if err != nil {
		return nil, classify(err)
	}
	r := response(msg)
	r.ID, r.Latency = req.ID, time.Since(start)
	return r, nil
}

func response(m *sdk.Message) *provider.Response {
	var text strings.Builder
	for _, block := range m.Content {
		if t, ok := block.AsAny().(sdk.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	return &provider.Response{
		Text: text.String(),
		Usage: provider.Usage{
			InputTokens:      int(m.Usage.InputTokens),
			OutputTokens:     int(m.Usage.OutputTokens),
			CacheReadTokens:  int(m.Usage.CacheReadInputTokens),
			CacheWriteTokens: int(m.Usage.CacheCreationInputTokens),
		},
		StopReason: string(m.StopReason),
		Refused:    m.StopReason == sdk.StopReasonRefusal,
		Truncated:  m.StopReason == sdk.StopReasonMaxTokens,
		Model:      m.Model,
	}
}

func classify(err error) error {
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		return &provider.Error{Status: apiErr.StatusCode, Retryable: provider.StatusRetryable(apiErr.StatusCode), Err: err}
	}
	return &provider.Error{Retryable: !errors.Is(err, context.Canceled), Err: err}
}

// SubmitBatch creates a Message Batch; request ids become custom ids.
func (p *Provider) SubmitBatch(ctx context.Context, reqs []provider.Request) (string, error) {
	items := make([]sdk.MessageBatchNewParamsRequest, len(reqs))
	for i, req := range reqs {
		b := build(req)
		items[i] = sdk.MessageBatchNewParamsRequest{
			CustomID: req.ID,
			Params: sdk.MessageBatchNewParamsRequestParams{
				Model:        req.Model,
				MaxTokens:    maxTokens(req),
				System:       b.system,
				Messages:     b.messages,
				OutputConfig: b.output,
				Thinking:     b.thinking,
			},
		}
	}
	batch, err := p.client.Messages.Batches.New(ctx, sdk.MessageBatchNewParams{Requests: items})
	if err != nil {
		return "", classify(err)
	}
	return batch.ID, nil
}

// BatchDone reports whether the batch ended.
func (p *Provider) BatchDone(ctx context.Context, id string) (bool, error) {
	b, err := p.client.Messages.Batches.Get(ctx, id, sdk.MessageBatchGetParams{})
	if err != nil {
		return false, classify(err)
	}
	return b.ProcessingStatus == sdk.MessageBatchProcessingStatusEnded, nil
}

// BatchResults streams a finished batch's results.
func (p *Provider) BatchResults(ctx context.Context, id string) ([]provider.BatchResult, error) {
	stream := p.client.Messages.Batches.ResultsStreaming(ctx, id, sdk.MessageBatchResultsParams{})
	defer func() { _ = stream.Close() }()
	var out []provider.BatchResult
	for stream.Next() {
		r := stream.Current()
		br := provider.BatchResult{ID: r.CustomID}
		switch r.Result.Type {
		case "succeeded":
			m := r.Result.Message
			br.Response = response(&m)
			br.Response.ID = r.CustomID
		case "errored":
			br.Err = &provider.Error{Retryable: true, Err: fmt.Errorf("batch request errored: %s", r.Result.Error.Error.Message)}
		default: // canceled, expired
			br.Err = &provider.Error{Retryable: true, Err: fmt.Errorf("batch request %s", r.Result.Type)}
		}
		out = append(out, br)
	}
	if err := stream.Err(); err != nil {
		return nil, classify(err)
	}
	return out, nil
}
