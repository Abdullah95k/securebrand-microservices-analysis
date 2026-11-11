package media_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/abdullah95k/securebrand-contracts/conformance"
	analysisv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/analysis/v1"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
)

// TestWorkerNoSpeechServerLosesNoTry: with no speech-to-text server
// reachable (down, or a new replica not ready yet), a video goes back to the
// queue for stt_retry_after without counting a try and isn't acknowledged;
// once a server answers, it is analysed with its transcript.
func TestWorkerNoSpeechServerLosesNoTry(t *testing.T) {
	video := testVideo(t)
	b, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	st, w, fp, objs := setupWith(t, func(c *config.Analysis) { c.Media.Video.STTRetryAfter = time.Hour })
	ctx := context.Background()
	objs["b/v"] = b
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{job("v", "video")}, "analysis"); err != nil {
		t.Fatal(err)
	}
	down := true
	fp.Transcribing = func(context.Context) error {
		if down {
			return fmt.Errorf("%w: dial tcp: connection refused", provider.ErrUnavailable)
		}
		return nil
	}
	for range 3 {
		if _, err := w.Once(ctx); err != nil {
			t.Fatal(err)
		}
		state, tries, in := due(t, st, "v")
		if state != "pending" || tries != 0 || in < 50*time.Minute {
			t.Fatalf("no server reachable: %s, %d tries, due in %v; want pending, no try, back in about an hour", state, tries, in)
		}
		if _, ok := acks(t, st)["v"]; ok {
			t.Fatal("a video waiting for a speech server was acknowledged")
		}
		if _, err := st.DB.Exec(ctx, `UPDATE analysis.media_jobs SET not_before = now() WHERE media_id = 'v'`); err != nil {
			t.Fatal(err)
		}
	}
	down = false
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	var transcript string
	if err := st.DB.QueryRow(ctx, `SELECT coalesce(transcript, '') FROM analysis.media_analyses WHERE media_id = 'v'`).Scan(&transcript); err != nil ||
		transcript == "" {
		t.Fatalf("transcript %q %v once a server answers", transcript, err)
	}
}

// TestWorkerShutdownLosesNoTry: a job cut by the worker's shutdown (its
// context ends mid-job) goes back to the queue at once, without losing a
// try and without an acknowledgement.
func TestWorkerShutdownLosesNoTry(t *testing.T) {
	video := testVideo(t)
	b, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	st, w, fp, objs := setupWith(t, nil)
	objs["b/v"] = b
	if _, err := st.InsertMediaJobs(context.Background(), []*core.MediaJob{job("v", "video")}, "analysis"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	fp.Transcribing = func(c context.Context) error {
		cancel() // the pod is stopping
		<-c.Done()
		return c.Err()
	}
	if _, err := w.Once(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	state, tries, in := due(t, st, "v")
	if state != "pending" || tries != 0 || in > time.Second {
		t.Fatalf("cut by a shutdown: %s, %d tries, due in %v; want pending, no try, due now", state, tries, in)
	}
	if _, ok := acks(t, st)["v"]; ok {
		t.Fatal("a job cut by a shutdown was acknowledged")
	}
}

// TestWorkerLastTryWithoutSpeech: while the speech-to-text server answers
// with an error (HTTP 500), a video fails and is tried again as usual; on
// its last allowed try its frames are analysed without a transcript
// (transcript_source "none") instead of the whole video failing, and the
// result passes the contract's checks.
func TestWorkerLastTryWithoutSpeech(t *testing.T) {
	video := testVideo(t)
	b, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	st, w, fp, objs := setupWith(t, func(c *config.Analysis) { c.Media.MaxTries = 2 })
	ctx := context.Background()
	objs["b/tt:media:v"] = b
	v := contractJob("tt:media:v", "content", "", 72*time.Hour)
	v.Kind = "video"
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{v}, "analysis"); err != nil {
		t.Fatal(err)
	}
	fp.Transcribing = func(context.Context) error {
		return fmt.Errorf("server sba-whisper: %w", &provider.Error{Status: 500, Retryable: true, Err: errors.New("Internal Server Error")})
	}
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if state, tries, in := due(t, st, "tt:media:v"); state != "pending" || tries != 1 || in <= 0 {
		t.Fatalf("first try: %s, %d tries, due in %v; want retried later", state, tries, in)
	}
	if _, err := st.DB.Exec(ctx, `UPDATE analysis.media_jobs SET not_before = now() WHERE media_id = 'tt:media:v'`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := due(t, st, "tt:media:v"); state != "done" {
		t.Fatalf("last try: %s, want analysed from its frames", state)
	}
	var transcript, source string
	if err := st.DB.QueryRow(ctx, `SELECT coalesce(transcript, ''), coalesce(result->>'transcript_source', '') FROM analysis.media_analyses
		WHERE media_id = 'tt:media:v'`).Scan(&transcript, &source); err != nil {
		t.Fatal(err)
	}
	if transcript != "" || source != "none" {
		t.Fatalf("transcript %q from %q, want none", transcript, source)
	}
	var payload []byte
	if err := st.DB.QueryRow(ctx, `SELECT payload FROM analysis.outbox WHERE topic = 'analysis.media.v1' AND key = 'tt:media:v'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var m analysisv1.MediaAnalysis
	if err := proto.Unmarshal(payload, &m); err != nil {
		t.Fatal(err)
	}
	if m.TranscriptSource != "none" || m.Transcript != "" {
		t.Fatalf("published transcript %q from %q", m.Transcript, m.TranscriptSource)
	}
	if err := conformance.MediaAnalysis(&m, conformance.Options{Production: true}); err != nil {
		t.Fatalf("fails the contract's checks: %v", err)
	}
	// It is acknowledged as processed: the collector may delete the file.
	if !strings.Contains(fmt.Sprint(contractAcks(t, st)["tt:media:v"]), "PROCESSED") {
		t.Fatalf("acknowledgements %v, want PROCESSED", contractAcks(t, st)["tt:media:v"])
	}
}
