package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
)

type fakeAPI struct {
	mu     sync.Mutex
	bodies []map[string]any
	paths  []string
	status int
	stop   string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	// SDK v2 sends the key as a bearer token (older versions: X-Api-Key).
	if r.Header.Get("Authorization") != "Bearer test-key" && r.Header.Get("X-Api-Key") != "test-key" {
		http.Error(w, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`, http.StatusUnauthorized)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"nope"}}`)
		return
	}
	var body map[string]any
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
	}
	f.bodies = append(f.bodies, body)
	w.Header().Set("Content-Type", "application/json")
	msg := func(model string) string {
		stop := f.stop
		if stop == "" {
			stop = "end_turn"
		}
		return `{"id":"msg_1","type":"message","role":"assistant","model":"` + model + `","stop_reason":"` + stop + `",
			"content":[{"type":"thinking","thinking":"","signature":"s"},{"type":"text","text":"{\"items\":[]}"}],
			"usage":{"input_tokens":120,"output_tokens":40,"cache_read_input_tokens":2000,"cache_creation_input_tokens":0}}`
	}
	switch {
	case r.Method == "POST" && r.URL.Path == "/v1/messages":
		_, _ = io.WriteString(w, msg(body["model"].(string)))
	case r.Method == "POST" && r.URL.Path == "/v1/messages/batches":
		_, _ = io.WriteString(w, `{"id":"msgbatch_1","type":"message_batch","processing_status":"in_progress"}`)
	case r.Method == "GET" && r.URL.Path == "/v1/messages/batches/msgbatch_1":
		_, _ = io.WriteString(w, `{"id":"msgbatch_1","type":"message_batch","processing_status":"ended"}`)
	case r.Method == "GET" && r.URL.Path == "/v1/messages/batches/msgbatch_1/results":
		w.Header().Set("Content-Type", "application/x-jsonl")
		lines := []string{
			`{"custom_id":"r1","result":{"type":"succeeded","message":` + strings.ReplaceAll(msg("claude-haiku-5-5"), "\n", "") + `}}`,
			`{"custom_id":"r2","result":{"type":"errored","error":{"type":"error","error":{"type":"overloaded_error","message":"busy"}}}}`,
			`{"custom_id":"r3","result":{"type":"expired"}}`,
		}
		_, _ = io.WriteString(w, strings.Join(lines, "\n")+"\n")
	default:
		http.NotFound(w, r)
	}
}

func newTest(t *testing.T) (*Provider, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return New(Config{APIKey: "test-key", BaseURL: srv.URL, Timeout: 5 * time.Second, MaxRetries: 0}), api
}

func req(model, thinking string) provider.Request {
	return provider.Request{ID: "r1", Model: model, System: "SYSTEM", User: "USER", MaxTokens: 1000, Effort: "low", Thinking: thinking,
		Schema: map[string]any{"type": "object"}, Images: []provider.Image{{MediaType: "image/jpeg", Data: []byte{1, 2, 3}}}}
}

func TestCompleteRequestShape(t *testing.T) {
	p, api := newTest(t)
	resp, err := p.Complete(context.Background(), req("claude-haiku-5-5", "off"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"items":[]}` || resp.Usage.InputTokens != 120 || resp.Usage.CacheReadTokens != 2000 || resp.Refused || resp.Model != "claude-haiku-5-5" {
		t.Fatalf("response %+v", resp)
	}
	b := api.bodies[0]
	sys := b["system"].([]any)[0].(map[string]any)
	if sys["text"] != "SYSTEM" || sys["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Fatalf("system %v", sys)
	}
	oc := b["output_config"].(map[string]any)
	if oc["effort"] != "low" || oc["format"].(map[string]any)["type"] != "json_schema" {
		t.Fatalf("output_config %v", oc)
	}
	if b["thinking"].(map[string]any)["type"] != "disabled" {
		t.Fatalf("thinking %v", b["thinking"])
	}
	content := b["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if content[0].(map[string]any)["type"] != "image" || content[1].(map[string]any)["text"] != "USER" {
		t.Fatalf("content %v", content)
	}
	if _, ok := b["temperature"]; ok {
		t.Fatal("sampling parameters must not be sent")
	}
}

func TestThinkingPerModel(t *testing.T) {
	for _, c := range []struct{ model, mode, want string }{
		{"claude-haiku-5-5", "off", "disabled"},
		{"claude-sonnet-5-5", "off", "between_tools"},
		{"claude-opus-5-5", "off", ""}, // cannot be disabled: left to effort
		{"claude-opus-5-5", "adaptive", "adaptive"},
		{"claude-haiku-5-5", "", ""},
	} {
		p, api := newTest(t)
		if _, err := p.Complete(context.Background(), req(c.model, c.mode)); err != nil {
			t.Fatal(err)
		}
		got := ""
		if th, ok := api.bodies[0]["thinking"].(map[string]any); ok {
			got, _ = th["type"].(string)
		}
		if got != c.want {
			t.Errorf("%s %s: thinking %q, want %q", c.model, c.mode, got, c.want)
		}
	}
}

func TestRefusalAndErrors(t *testing.T) {
	p, api := newTest(t)
	api.stop = "refusal"
	resp, err := p.Complete(context.Background(), req("claude-haiku-5-5", "off"))
	if err != nil || !resp.Refused {
		t.Fatalf("refusal %+v %v", resp, err)
	}
	api.stop = "max_tokens"
	if resp, _ := p.Complete(context.Background(), req("claude-haiku-5-5", "off")); !resp.Truncated {
		t.Fatal("truncation")
	}
	api.status = 400
	_, err = p.Complete(context.Background(), req("claude-haiku-5-5", "off"))
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Status != 400 || pe.Retryable {
		t.Fatalf("400 must be permanent: %v", err)
	}
	api.status = 529
	if _, err := p.Complete(context.Background(), req("claude-haiku-5-5", "off")); !provider.Retryable(err) {
		t.Fatalf("529 must be retryable: %v", err)
	}
}

func TestBatchFlow(t *testing.T) {
	p, api := newTest(t)
	ctx := context.Background()
	r2 := req("claude-haiku-5-5", "off")
	r2.ID = "r2"
	id, err := p.SubmitBatch(ctx, []provider.Request{req("claude-haiku-5-5", "off"), r2})
	if err != nil || id != "msgbatch_1" {
		t.Fatalf("submit %s %v", id, err)
	}
	reqs := api.bodies[0]["requests"].([]any)
	first := reqs[0].(map[string]any)
	if first["custom_id"] != "r1" || first["params"].(map[string]any)["output_config"] == nil {
		t.Fatalf("batch request %v", first)
	}
	done, err := p.BatchDone(ctx, id)
	if err != nil || !done {
		t.Fatalf("done %v %v", done, err)
	}
	res, err := p.BatchResults(ctx, id)
	if err != nil || len(res) != 3 {
		t.Fatalf("results %d %v", len(res), err)
	}
	if res[0].ID != "r1" || res[0].Response == nil || res[0].Response.Usage.OutputTokens != 40 || res[0].Err != nil {
		t.Fatalf("succeeded %+v", res[0])
	}
	if res[1].Err == nil || !provider.Retryable(res[1].Err) || res[2].Err == nil {
		t.Fatalf("errored/expired %+v %+v", res[1], res[2])
	}
}
