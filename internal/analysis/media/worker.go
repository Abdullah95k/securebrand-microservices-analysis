package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/ametrics"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/pipeline"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/prompt"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/store"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/textnorm"
	"github.com/abdullah95k/securebrand-microservices/internal/config"
)

// Objects reads media files.
type Objects interface {
	Get(ctx context.Context, bucket, key string, maxBytes int64) ([]byte, error)
}

// ErrGone means the file no longer exists (deleted after its TTL).
var ErrGone = errors.New("media: object not found")

// ErrTooLarge means the file exceeds analysis.media.max_bytes.
var ErrTooLarge = errors.New("media: object too large")

// MinioObjects reads from S3-compatible storage.
type MinioObjects struct{ Client *minio.Client }

// Get reads an object, up to maxBytes.
func (m MinioObjects) Get(ctx context.Context, bucket, key string, maxBytes int64) ([]byte, error) {
	obj, err := m.Client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = obj.Close() }()
	b, err := io.ReadAll(io.LimitReader(obj, maxBytes+1))
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrGone
		}
		return nil, err
	}
	if int64(len(b)) > maxBytes {
		return nil, ErrTooLarge
	}
	return b, nil
}

// Worker analyses media jobs.
type Worker struct {
	P       *pipeline.Pipeline
	Objects Objects
	Cfg     config.AnalysisMedia
	kinds   map[string]bool
	once    sync.Once

	promptOnce sync.Once
	system     string
	schema     map[string]any
}

// prompts builds the static media prompt and schema once.
func (w *Worker) prompts() (string, map[string]any) {
	w.promptOnce.Do(func() {
		w.system, w.schema = prompt.MediaSystem(w.P.Tax), prompt.MediaSchema(w.P.Tax)
	})
	return w.system, w.schema
}

func (w *Worker) analysed(kind string) bool {
	w.once.Do(func() {
		w.kinds = map[string]bool{}
		for _, k := range w.Cfg.Kinds {
			w.kinds[k] = true
		}
	})
	return w.kinds[kind]
}

// Run processes jobs with analysis.media.workers goroutines until ctx ends.
func (w *Worker) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for i := 0; i < max(w.Cfg.Workers, 1); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				n, err := w.Once(ctx)
				if err != nil && ctx.Err() == nil {
					slog.Warn("analysis: media", "err", err)
				}
				if n == 0 {
					t := time.NewTimer(w.Cfg.Poll)
					select {
					case <-ctx.Done():
					case <-t.C:
					}
					t.Stop()
				}
			}
		}()
	}
	wg.Wait()
	return nil
}

// Once claims and processes one job; it returns how many it processed.
func (w *Worker) Once(ctx context.Context) (int, error) {
	jobs, err := w.P.Store.ClaimMediaJobs(ctx, 1, w.Cfg.Lease)
	if err != nil || len(jobs) == 0 {
		return 0, err
	}
	for _, j := range jobs {
		w.process(ctx, j)
	}
	return len(jobs), nil
}

func (w *Worker) process(ctx context.Context, j core.MediaJob) {
	consumer := w.Cfg.Consumer
	if !w.analysed(j.Kind) || (j.IsVideo() && !w.Cfg.Video.Enabled) {
		w.finish(ctx, store.MediaResult{Job: j, Consumer: consumer, SkipReason: "policy"}, "skipped")
		return
	}
	res, err := w.analyse(ctx, j)
	if err != nil {
		if errors.Is(err, errBudget) {
			// Not a failure: wait for budget (the TTL still applies).
			if rerr := w.P.Store.ReleaseMedia(context.WithoutCancel(ctx), j.MediaID, 10*time.Minute); rerr != nil {
				slog.Error("analysis: media job not released", "media", j.MediaID, "err", rerr)
			}
			return
		}
		permanent := errors.Is(err, ErrUndecodable) || errors.Is(err, ErrGone) || errors.Is(err, ErrTooLarge) ||
			errors.Is(err, ErrNoFFmpeg)
		outcome := "failed_retry"
		if permanent {
			outcome = "failed_permanent"
		}
		ametrics.Media.WithLabelValues(j.Kind, outcome).Inc()
		slog.Warn("analysis: media job failed", "media", j.MediaID, "kind", j.Kind, "permanent", permanent, "err", err)
		if ferr := w.P.Store.FailMedia(context.WithoutCancel(ctx), j, consumer, err.Error(), permanent, w.Cfg.MaxTries); ferr != nil {
			slog.Error("analysis: media failure not recorded", "media", j.MediaID, "err", ferr)
		}
		return
	}
	w.finish(ctx, *res, "analyzed")
}

func (w *Worker) finish(ctx context.Context, r store.MediaResult, outcome string) {
	if err := w.P.Store.FinishMedia(ctx, r); err != nil {
		slog.Error("analysis: media result not stored", "media", r.Job.MediaID, "err", err)
		return
	}
	ametrics.Media.WithLabelValues(r.Job.Kind, outcome).Inc()
}

// analyse fetches the file and runs the vision model (with frames and a
// transcript for videos).
func (w *Worker) analyse(ctx context.Context, j core.MediaJob) (*store.MediaResult, error) {
	route := core.RouteImage
	if j.IsVideo() {
		route = core.RouteVideo
	}
	rc, ok := w.P.Cfg.Routes[route]
	if !ok || !rc.On() {
		return &store.MediaResult{Job: j, Consumer: w.Cfg.Consumer, SkipReason: "route " + route + " disabled"}, nil
	}
	data, err := w.Objects.Get(ctx, j.Bucket, j.Key, w.Cfg.MaxBytes)
	if err != nil {
		return nil, err
	}
	ownerAt, postText, err := w.P.Store.OwnerContext(ctx, j.OwnerContentID)
	if err != nil {
		return nil, err
	}
	if ownerAt.IsZero() {
		ownerAt = time.Now().UTC()
	}
	var images []provider.Image
	var transcript string
	if j.IsVideo() {
		f, err := w.video(ctx, j, data)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		for _, img := range f.Images {
			images = append(images, provider.Image{MediaType: "image/jpeg", Data: img})
		}
		if len(f.Audio) > 0 && w.Cfg.Video.STTProvider != "" {
			if t, ok := w.P.Providers.Transcriber(w.Cfg.Video.STTProvider); ok {
				text, err := t.Transcribe(ctx, f.Audio, "audio.mp3", w.Cfg.Video.STTLanguage)
				if err != nil {
					return nil, fmt.Errorf("transcribe: %w", err)
				}
				transcript = textnorm.Truncate(text, 20000)
			}
		}
	} else {
		img, err := Resize(data, w.Cfg.ImageMaxSide)
		if err != nil {
			return nil, err
		}
		images = append(images, provider.Image{MediaType: "image/jpeg", Data: img})
	}
	mask := func(s string) string {
		if w.P.Cfg.Privacy.MaskPII {
			return textnorm.MaskPII(s)
		}
		return s
	}
	reg := w.P.Entities()
	hintText := postText + " " + j.AltText
	var hints []prompt.Hint
	for _, m := range reg.Find(hintText) {
		hints = append(hints, prompt.Hint{ID: m.EntityID, Name: m.Name})
	}
	system, schema := w.prompts()
	req := provider.Request{
		ID:         uuid.NewString(),
		Model:      rc.Model,
		System:     system,
		User:       prompt.MediaUser(mask(textnorm.Truncate(postText, 1000)), hints, mask(transcript), len(images)),
		Images:     images,
		Schema:     schema,
		SchemaName: "media",
		MaxTokens:  rc.MaxTokens,
		Effort:     rc.Effort,
		Thinking:   rc.Thinking,
	}
	est := w.P.Pricing.Estimate(rc.Model, len(req.System), len(req.User), len(images), 250, false)
	if ok, err := w.P.Budget.Allow(ctx, route, est, false); err != nil || !ok {
		if err == nil {
			err = errBudget
		}
		return nil, err
	}
	resp, prov, model, fallback, err := w.call(ctx, route, rc, req)
	if err != nil {
		return nil, err
	}
	cost := w.P.Pricing.Cost(model, resp.Usage, false)
	w.P.Budget.Add(route, cost)
	w.record(ctx, req.ID, route, prov, model, fallback, j.MediaID, resp, cost)
	if resp.Refused {
		return &store.MediaResult{Job: j, Consumer: w.Cfg.Consumer, SkipReason: "refused by " + model}, nil
	}
	mr, _, err := prompt.ParseMedia(resp.Text, w.P.Tax, reg)
	if err != nil {
		return nil, err
	}
	mr.Transcript = transcript
	return &store.MediaResult{Job: j, Result: mr, OwnerTime: ownerAt, Consumer: w.Cfg.Consumer, CostUSD: cost,
		Prov: core.Provenance{Provider: prov, Model: model, PromptVersion: prompt.PromptVersion, TaxonomyVersion: w.P.Tax.Version,
			SchemaVersion: prompt.SchemaVersion, Route: route, Lane: core.LaneRealtime, Fallback: fallback, AnalyzedAt: time.Now().UTC(),
			RequestID: req.ID}}, nil
}

var errBudget = errors.New("media: daily budget reached")

func (w *Worker) video(ctx context.Context, j core.MediaJob, data []byte) (*Frames, error) {
	tmp, err := os.CreateTemp(w.Cfg.Video.TempDir, "sb-media-*")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	vctx, cancel := context.WithTimeout(ctx, w.Cfg.Video.Timeout)
	defer cancel()
	v := w.Cfg.Video
	return ExtractFrames(vctx, v.FFmpeg, name, v.TempDir, j.DurationMS, v.MaxFrames, v.FrameSide, v.MaxSeconds)
}

// call sends the request to the route's provider, or its fallback when the
// primary is unavailable, fails or refuses.
func (w *Worker) call(ctx context.Context, route string, rc config.Route, req provider.Request) (*provider.Response, string, string, bool, error) {
	prov, model, fallback := rc.Provider, rc.Model, false
	if !w.P.Providers.Available(prov) && rc.FallbackProvider != "" {
		prov, model, fallback = rc.FallbackProvider, rc.FallbackModel, true
		req.Model = model
	}
	resp, err := w.P.Providers.Complete(ctx, prov, req)
	if !fallback && rc.FallbackProvider != "" && ((err != nil && provider.Retryable(err)) || (err == nil && resp.Refused)) {
		ametrics.Fallbacks.WithLabelValues(route).Inc()
		prov, model, fallback = rc.FallbackProvider, rc.FallbackModel, true
		req.Model = model
		resp, err = w.P.Providers.Complete(ctx, prov, req)
	}
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	ametrics.Requests.WithLabelValues(prov, model, core.LaneRealtime, outcome).Inc()
	return resp, prov, model, fallback, err
}

// record writes the provider request (for the budget and cost reports).
func (w *Worker) record(ctx context.Context, id, route, prov, model string, fallback bool, mediaID string, resp *provider.Response, cost float64) {
	rid, _ := uuid.Parse(id)
	if err := w.P.Store.StartRequest(ctx, store.RequestRow{ID: rid, Route: route, Lane: core.LaneRealtime, Provider: prov, Model: model,
		Fallback: fallback, ContentIDs: []string{"media:" + mediaID}, InputHashes: [][]byte{{}}, EstCostUSD: cost}); err != nil {
		slog.Warn("analysis: media request not recorded", "err", err)
		return
	}
	if _, err := w.P.Store.WriteResults(ctx, nil, store.RequestOutcome{ID: rid, Provider: prov, Model: model, Fallback: fallback,
		Usage:   [4]int{resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.CacheReadTokens, resp.Usage.CacheWriteTokens},
		CostUSD: cost, LatencyMS: int(resp.Latency.Milliseconds())}); err != nil {
		slog.Warn("analysis: media request not closed", "err", err)
	}
	ametrics.CostUSD.WithLabelValues(model, route).Add(cost)
	ametrics.Tokens.WithLabelValues(model, route, "input").Add(float64(resp.Usage.InputTokens))
	ametrics.Tokens.WithLabelValues(model, route, "output").Add(float64(resp.Usage.OutputTokens))
}
