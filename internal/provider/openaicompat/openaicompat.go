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
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
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
	// servers holds the free, reachable speech-to-text servers of a pool
	// (BaseURLs), nil without one. A server that can't be reached is out
	// of it until it accepts connections again.
	servers    chan string
	serverWait time.Duration
	probeEvery time.Duration
}

// Config configures the provider.
type Config struct {
	Name    string
	BaseURL string
	// BaseURLs, when set, is a pool of identical speech-to-text servers
	// (self-hosted Whisper replicas): each takes one transcription at a
	// time, and a transcription waits for a free one. Chat completions use
	// BaseURL, or the first of them.
	BaseURLs []string
	// ServerWait is how long a transcription waits for a free, reachable
	// server of the pool (1 minute by default); ProbeEvery, how often a
	// server that couldn't be reached is tried again (10 s by default).
	ServerWait      time.Duration
	ProbeEvery      time.Duration
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
	p.serverWait, p.probeEvery = cfg.ServerWait, cfg.ProbeEvery
	if p.serverWait <= 0 {
		p.serverWait = time.Minute
	}
	if p.probeEvery <= 0 {
		p.probeEvery = 10 * time.Second
	}
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
			if unreachable(err) {
				return "", fmt.Errorf("%w: %w", provider.ErrUnavailable, err)
			}
			return "", err
		}
		return out.Text, nil
	}
	// A pool: wait for a free server, so each transcribes one video at a
	// time (a Service balances connections, not requests, and connections
	// are reused, so two videos could otherwise land on one server). A
	// server that can't be reached (down, or not ready yet) leaves the pool
	// until it accepts connections again, and the video waits for another:
	// at most serverWait in all, then ErrUnavailable, so the caller tries
	// again later without counting it against the file.
	deadline := time.Now().Add(p.serverWait)
	for {
		base, err := p.take(ctx, deadline)
		if err != nil {
			return "", err
		}
		err = send(base)
		if err != nil && unreachable(err) {
			p.sideline(base, err)
			continue
		}
		p.servers <- base
		if err != nil {
			return "", err
		}
		return out.Text, nil
	}
}

// take waits for a free, reachable server of the pool until deadline.
func (p *Provider) take(ctx context.Context, deadline time.Time) (string, error) {
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case b := <-p.servers:
		return b, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-t.C:
		return "", fmt.Errorf("%w: no speech-to-text server free and reachable within %v", provider.ErrUnavailable, p.serverWait)
	}
}

// sideline takes a server that couldn't be reached out of the pool and
// tries it every probeEvery; it rejoins once it accepts a connection.
func (p *Provider) sideline(base string, cause error) {
	slog.Warn("analysis: speech-to-text server unreachable, out of the pool until it answers", "provider", p.name, "server", base, "err", cause)
	addr := hostPort(base)
	go func() {
		t := time.NewTicker(p.probeEvery)
		defer t.Stop()
		for range t.C {
			c, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err != nil {
				continue
			}
			_ = c.Close()
			slog.Info("analysis: speech-to-text server answers again, back in the pool", "provider", p.name, "server", base)
			p.servers <- base
			return
		}
	}()
}

// hostPort is a base URL's host:port, the scheme's port when none is given.
func hostPort(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	if u.Port() != "" {
		return u.Host
	}
	if u.Scheme == "https" {
		return net.JoinHostPort(u.Hostname(), "443")
	}
	return net.JoinHostPort(u.Hostname(), "80")
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
