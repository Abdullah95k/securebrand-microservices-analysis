package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
)

func TestChatCompletion(t *testing.T) {
	var body map[string]any
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "bad", 400)
			return
		}
		if n == 1 {
			w.WriteHeader(429) // retried
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = io.WriteString(w, `{"model":"gpt-5.4-nano","choices":[{"finish_reason":"stop","message":{"content":"{\"items\":[]}"}}],
			"usage":{"prompt_tokens":1000,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":800}}}`)
	}))
	defer srv.Close()
	p := New(Config{Name: "openai", BaseURL: srv.URL + "/v1/", APIKey: "k", MaxRetries: 2})
	resp, err := p.Complete(context.Background(), provider.Request{ID: "x", Model: "gpt-5.4-nano", System: "S", User: "U",
		Schema: map[string]any{"type": "object"}, MaxTokens: 500, Effort: "low", Images: []provider.Image{{MediaType: "image/png", Data: []byte("img")}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"items":[]}` || resp.Usage.InputTokens != 200 || resp.Usage.CacheReadTokens != 800 || resp.Usage.OutputTokens != 50 {
		t.Fatalf("response %+v", resp)
	}
	rf := body["response_format"].(map[string]any)
	js := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["strict"] != true || js["name"] != "analysis" {
		t.Fatalf("response_format %v", rf)
	}
	msgs := body["messages"].([]any)
	if msgs[0].(map[string]any)["content"] != "S" {
		t.Fatal("system message")
	}
	parts := msgs[1].(map[string]any)["content"].([]any)
	if !strings.HasPrefix(parts[0].(map[string]any)["image_url"].(map[string]any)["url"].(string), "data:image/png;base64,") {
		t.Fatal("image part")
	}
	if body["max_completion_tokens"].(float64) != 500 || body["reasoning_effort"] != "low" {
		t.Fatalf("limits %v %v", body["max_completion_tokens"], body["reasoning_effort"])
	}
	if p.Name() != "openai" || calls.Load() != 2 {
		t.Fatal("retry on 429")
	}
}

func TestRefusalAndPermanentErrors(t *testing.T) {
	mode := "refusal"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "refusal":
			_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"content":null,"refusal":"I can't help"}}]}`)
		case "filter":
			_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"content_filter","message":{"content":""}}]}`)
		case "empty":
			_, _ = io.WriteString(w, `{"choices":[]}`)
		default:
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"message":"bad schema"}}`)
		}
	}))
	defer srv.Close()
	p := New(Config{BaseURL: srv.URL, MaxRetries: 3})
	for _, m := range []string{"refusal", "filter"} {
		mode = m
		resp, err := p.Complete(context.Background(), provider.Request{Model: "m"})
		if err != nil || !resp.Refused {
			t.Fatalf("%s: %+v %v", m, resp, err)
		}
	}
	mode = "empty"
	if _, err := p.Complete(context.Background(), provider.Request{Model: "m"}); !provider.Retryable(err) {
		t.Fatal("empty choices should be retryable")
	}
	mode = "bad"
	_, err := p.Complete(context.Background(), provider.Request{Model: "m"})
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Status != 400 || pe.Retryable || !strings.Contains(err.Error(), "bad schema") {
		t.Fatalf("400: %v", err)
	}
}

func TestTranscribe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil || r.URL.Path != "/audio/transcriptions" {
			http.Error(w, "bad", 400)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "no file", 400)
			return
		}
		b, _ := io.ReadAll(f)
		if r.FormValue("model") != "whisper-1" || r.FormValue("language") != "ar" || string(b) != "AUDIO" {
			http.Error(w, "bad form", 400)
			return
		}
		_, _ = io.WriteString(w, `{"text":"مرحبا"}`)
	}))
	defer srv.Close()
	p := New(Config{BaseURL: srv.URL, TranscribeModel: "whisper-1"})
	got, err := p.Transcribe(context.Background(), []byte("AUDIO"), "a.mp3", "ar")
	if err != nil || got != "مرحبا" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := New(Config{BaseURL: srv.URL}).Transcribe(context.Background(), nil, "a", ""); err == nil {
		t.Fatal("transcription without a model accepted")
	}
}
