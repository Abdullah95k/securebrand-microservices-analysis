package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// TestTranscribePoolOneVideoPerServer: with a pool of servers (whisper
// replicas), each transcribes one video at a time; a third video waits for
// a free server, and a caller that gives up while waiting leaves at once.
func TestTranscribePoolOneVideoPerServer(t *testing.T) {
	release := make(chan struct{})
	var busy [2]atomic.Int32
	var most [2]atomic.Int32
	var calls atomic.Int32
	server := func(i int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			n := busy[i].Add(1)
			defer busy[i].Add(-1)
			for {
				m := most[i].Load()
				if n <= m || most[i].CompareAndSwap(m, n) {
					break
				}
			}
			<-release
			_, _ = io.WriteString(w, `{"text":"ok"}`)
		}))
	}
	a, b := server(0), server(1)
	defer a.Close()
	defer b.Close()
	p := New(Config{BaseURLs: []string{a.URL + "/", b.URL}, TranscribeModel: "whisper-1"})

	done := make(chan error, 3)
	for range 3 {
		go func() {
			_, err := p.Transcribe(context.Background(), []byte("AUDIO"), "a.mp3", "ar")
			done <- err
		}()
	}
	// Two reach a server each; the third waits for one of them.
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 2 || busy[0].Load() != 1 || busy[1].Load() != 1 {
		t.Fatalf("%d calls, busy %d and %d: want one video on each server, the third waiting", got, busy[0].Load(), busy[1].Load())
	}
	// A caller that gives up while every server is busy doesn't wait.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Transcribe(ctx, []byte("AUDIO"), "a.mp3", "ar"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting caller's error %v, want its deadline", err)
	}
	close(release)
	for range 3 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if most[0].Load() != 1 || most[1].Load() != 1 || calls.Load() != 3 {
		t.Fatalf("at most %d and %d at once, %d calls: want 1 per server, 3 calls", most[0].Load(), most[1].Load(), calls.Load())
	}
}

// TestTranscribePoolPassesOverADeadServer: a server that refuses
// connections (down, or no ready pod behind its Service) leaves the pool, so
// every video goes to the live one and none fails; it rejoins once it
// accepts connections again.
func TestTranscribePoolPassesOverADeadServer(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"text":"ok"}`) })
	alive := httptest.NewServer(ok)
	defer alive.Close()
	dead := httptest.NewServer(ok)
	deadAddr := dead.Listener.Addr().String()
	dead.Close() // refuses connections from now on
	p := New(Config{BaseURLs: []string{"http://" + deadAddr, alive.URL}, TranscribeModel: "whisper-1", ProbeEvery: 20 * time.Millisecond})
	for range 4 {
		if got, err := p.Transcribe(context.Background(), []byte("AUDIO"), "a.mp3", "ar"); err != nil || got != "ok" {
			t.Fatalf("with one server dead: %q %v", got, err)
		}
	}
	if len(p.servers) != 1 {
		t.Fatalf("%d servers in the pool, want only the live one", len(p.servers))
	}
	// The dead server comes back on its address: it rejoins the pool.
	l, err := net.Listen("tcp", deadAddr)
	if err != nil {
		t.Skipf("address not free again: %v", err)
	}
	back := &httptest.Server{Listener: l, Config: &http.Server{Handler: ok, ReadHeaderTimeout: time.Second}}
	back.Start()
	defer back.Close()
	deadline := time.Now().Add(5 * time.Second)
	for len(p.servers) != 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(p.servers) != 2 {
		t.Fatalf("%d servers in the pool after the dead one came back, want 2", len(p.servers))
	}
}

// TestTranscribeUnreachableIsUnavailable: with no server reachable, a
// transcription ends with ErrUnavailable (the caller tries again later
// without counting it against the file): after ServerWait for a pool, at
// once for a single server; two callers at once both return.
func TestTranscribeUnreachableIsUnavailable(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	if _, err := New(Config{BaseURL: deadURL, TranscribeModel: "whisper-1"}).
		Transcribe(context.Background(), []byte("AUDIO"), "a.mp3", "ar"); !errors.Is(err, provider.ErrUnavailable) || !unreachable(err) {
		t.Fatalf("single server dead: %v, want ErrUnavailable with the connection error", err)
	}
	all := New(Config{BaseURLs: []string{deadURL, deadURL}, TranscribeModel: "whisper-1", ServerWait: 100 * time.Millisecond})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := all.Transcribe(context.Background(), []byte("AUDIO"), "a.mp3", "ar")
			errs <- err
		}()
	}
	for range 2 {
		select {
		case err := <-errs:
			if !errors.Is(err, provider.ErrUnavailable) {
				t.Fatalf("every server dead: %v, want ErrUnavailable", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("callers stuck with every server dead")
		}
	}
	// An HTTP error from a reachable server is not "unavailable", and it
	// names the server that answered so (the pool's servers are separate
	// pods).
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}))
	defer bad.Close()
	_, err := New(Config{BaseURLs: []string{bad.URL}, TranscribeModel: "whisper-1"}).Transcribe(context.Background(), []byte("AUDIO"), "a.mp3", "ar")
	var perr *provider.Error
	if err == nil || errors.Is(err, provider.ErrUnavailable) || !strings.Contains(err.Error(), "server 127.0.0.1: ") ||
		!errors.As(err, &perr) || perr.Status != http.StatusInternalServerError {
		t.Fatalf("a server's 500: %v, want an error naming the server, with its status, that isn't ErrUnavailable", err)
	}
}
