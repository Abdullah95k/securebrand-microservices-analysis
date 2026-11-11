// Package openaicompat is a provider for any OpenAI-compatible endpoint:
// OpenAI itself, Gemini's OpenAI-compatible endpoint, and self-hosted
// servers (vLLM serving Gemma or a distilled model; Whisper servers for
// speech-to-text). Chat completions with a strict JSON-schema response
// format, and /audio/transcriptions.
package openaicompat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
)

// Provider calls an OpenAI-compatible API.
type Provider struct {
	name            string
	baseURL         string
	apiKey          string
	http            *http.Client
	maxRetries      int
	transcribeModel string
	// servers holds the free speech-to-text servers of a pool (BaseURLs),
	// nil without one.
	servers chan string
}

// Config configures the provider.
type Config struct {
	Name    string
	BaseURL string
	// BaseURLs, when set, is a pool of identical speech-to-text servers
	// (self-hosted Whisper replicas): each takes one transcription at a
	// time, and a transcription waits for a free one. Chat completions use
	// BaseURL, or the first of them.
	BaseURLs        []string
	APIKey          string
	Timeout         time.Duration
	MaxRetries      int
	TranscribeModel string
	HTTP            *http.Client
}

// New creates the provider.
func New(cfg Config) *Provider {
	hc := cfg.HTTP
	if hc == nil {
		t := cfg.Timeout
		if t <= 0 {
			t = 120 * time.Second
		}
		hc = &http.Client{Timeout: t}
	}
	p := &Provider{name: cfg.Name, baseURL: strings.TrimRight(cfg.BaseURL, "/"), apiKey: cfg.APIKey, http: hc,
		maxRetries: cfg.MaxRetries, transcribeModel: cfg.TranscribeModel}
	if len(cfg.BaseURLs) > 0 {
		p.servers = make(chan string, len(cfg.BaseURLs))
		for _, u := range cfg.BaseURLs {
			p.servers <- strings.TrimRight(u, "/")
		}
		if p.baseURL == "" {
			p.baseURL = strings.TrimRight(cfg.BaseURLs[0], "/")
		}
	}
	return p
}

// Name is the configured provider name.
func (p *Provider) Name() string { return p.name }

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type chatRequest struct {
	Model               string         `json:"model"`
	Messages            []message      `json:"messages"`
	ResponseFormat      map[string]any `json:"response_format,omitempty"`
	MaxCompletionTokens int            `json:"max_completion_tokens,omitempty"`
	ReasoningEffort     string         `json:"reasoning_effort,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content *string `json:"content"`
			Refusal *string `json:"refusal"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// Complete runs one chat completion with a JSON-schema response format.
func (p *Provider) Complete(ctx context.Context, req provider.Request) (*provider.Response, error) {
	user := []contentPart{}
	for _, img := range req.Images {
		user = append(user, contentPart{Type: "image_url", ImageURL: &imageURL{
			URL: "data:" + img.MediaType + ";base64," + base64.StdEncoding.EncodeToString(img.Data)}})
	}
	user = append(user, contentPart{Type: "text", Text: req.User})
	body := chatRequest{
		Model: req.Model,
		Messages: []message{
			{Role: "system", Content: req.System},
			{Role: "user", Content: user},
		},
		MaxCompletionTokens: req.MaxTokens,
		ReasoningEffort:     req.Effort,
	}
	if req.Schema != nil {
		name := req.SchemaName
		if name == "" {
			name = "analysis"
		}
		body.ResponseFormat = map[string]any{"type": "json_schema",
			"json_schema": map[string]any{"name": name, "schema": req.Schema, "strict": true}}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	var cr chatResponse
	if err := p.do(ctx, p.baseURL, "/chat/completions", "application/json", func() io.Reader { return bytes.NewReader(b) }, &cr); err != nil {
		return nil, err
	}
	if len(cr.Choices) == 0 {
		return nil, &provider.Error{Retryable: true, Err: errors.New("no choices in response")}
	}
	ch := cr.Choices[0]
	r := &provider.Response{
		ID:      req.ID,
		Model:   cr.Model,
		Latency: time.Since(start),
		Usage: provider.Usage{
			InputTokens:     cr.Usage.PromptTokens - cr.Usage.PromptTokensDetails.CachedTokens,
			OutputTokens:    cr.Usage.CompletionTokens,
			CacheReadTokens: cr.Usage.PromptTokensDetails.CachedTokens,
		},
		StopReason: ch.FinishReason,
		Truncated:  ch.FinishReason == "length",
	}
	if ch.Message.Content != nil {
		r.Text = *ch.Message.Content
	}
	if (ch.Message.Refusal != nil && *ch.Message.Refusal != "") || ch.FinishReason == "content_filter" {
		r.Refused = true
	}
	return r, nil
}

// Transcribe sends audio to /audio/transcriptions.
func (p *Provider) Transcribe(ctx context.Context, audio []byte, filename, language string) (string, error) {
	if p.transcribeModel == "" {
		return "", errors.New("openaicompat: no transcribe_model configured")
	}
	var buf bytes.Buffer
	ct := ""
	{
		w := multipart.NewWriter(&buf)
		fw, err := w.CreateFormFile("file", filename)
		if err != nil {
			return "", err
		}
		if _, err := fw.Write(audio); err != nil {
			return "", err
		}
		_ = w.WriteField("model", p.transcribeModel)
		if language != "" {
			_ = w.WriteField("language", language)
		}
		_ = w.WriteField("response_format", "json")
		if err := w.Close(); err != nil {
			return "", err
		}
		ct = w.FormDataContentType()
	}
	body := buf.Bytes()
	var out struct {
		Text string `json:"text"`
	}
	send := func(base string) error {
		return p.do(ctx, base, "/audio/transcriptions", ct, func() io.Reader { return bytes.NewReader(body) }, &out)
	}
	if p.servers == nil {
		if err := send(p.baseURL); err != nil {
			return "", err
		}
		return out.Text, nil
	}
	// A pool: wait for a free server, so each transcribes one video at a
	// time (a Service balances connections, not requests, and connections
	// are reused, so two videos could otherwise land on one server). A
	// server that can't be reached (down, or not ready) is passed over for
	// the next free one, so a dead server doesn't fail half the videos.
	// It only moves to a server that is free right now: waiting while
	// holding a dead one could leave two callers each holding one.
	var base string
	select {
	case base = <-p.servers:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	var down []string
	defer func() {
		for _, b := range append(down, base) {
			p.servers <- b
		}
	}()
	for {
		err := send(base)
		if err == nil {
			return out.Text, nil
		}
		if !unreachable(err) {
			return "", err
		}
		select {
		case next := <-p.servers:
			down, base = append(down, base), next
		default:
			return "", err
		}
	}
}

// unreachable: the request never reached the server (refused, no route,
// no endpoint behind its Service), so another server can take it.
func unreachable(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// do posts with retries on retryable statuses and network errors.
func (p *Provider) do(ctx context.Context, base, path, contentType string, body func() io.Reader, out any) error {
	var last error
	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<(attempt-1)) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, body())
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", contentType)
		if p.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.apiKey)
		}
		resp, err := p.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			last = &provider.Error{Retryable: true, Err: err}
			continue
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		_ = resp.Body.Close()
		if rerr != nil {
			last = &provider.Error{Retryable: true, Err: rerr}
			continue
		}
		if resp.StatusCode/100 != 2 {
			last = &provider.Error{Status: resp.StatusCode, Retryable: provider.StatusRetryable(resp.StatusCode),
				Err: fmt.Errorf("%s: %s", path, errorMessage(data))}
			if !provider.StatusRetryable(resp.StatusCode) {
				return last
			}
			continue
		}
		if err := json.Unmarshal(data, out); err != nil {
			return &provider.Error{Retryable: true, Err: fmt.Errorf("%s: decode: %w", path, err)}
		}
		return nil
	}
	return last
}

// errorMessage extracts the API's error message; it never echoes the
// request (which holds content).
func errorMessage(data []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	if len(data) > 200 {
		data = data[:200]
	}
	return string(data)
}
