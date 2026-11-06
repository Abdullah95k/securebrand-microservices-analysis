package media

import (
	"bytes"
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

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ametrics"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/pipeline"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/prompt"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
)

// Objects reads media files.
type Objects interface {
	// Fetch streams an object into dst, at most maxBytes (ErrTooLarge
	// beyond; ErrGone when it no longer exists). Nothing is held in memory:
	// videos go to a temporary file, images to a buffer bounded by
	// analysis.media.image_max_bytes.
	Fetch(ctx context.Context, bucket, key string, maxBytes int64, dst io.Writer) (int64, error)
}

// ErrGone means the file no longer exists (deleted after its TTL).
var ErrGone = errors.New("media: object not found")

// ErrTooLarge means the file exceeds analysis.media.max_bytes (videos) or
// image_max_bytes and image_max_pixels (images).
var ErrTooLarge = errors.New("media: object too large")

// MinioObjects reads from S3-compatible storage.
type MinioObjects struct{ Client *minio.Client }

// Fetch streams an object into dst, up to maxBytes.
func (m MinioObjects) Fetch(ctx context.Context, bucket, key string, maxBytes int64, dst io.Writer) (int64, error) {
	obj, err := m.Client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return 0, err
	}
	defer func() { _ = obj.Close() }()
	n, err := CopyLimited(dst, obj, maxBytes)
	if err != nil && minio.ToErrorResponse(err).Code == "NoSuchKey" {
		return n, ErrGone
	}
	return n, err
}

// CopyLimited copies src into dst through a small fixed buffer, at most
// maxBytes; a source longer than that is ErrTooLarge (after maxBytes+1
// bytes, never more).
func CopyLimited(dst io.Writer, src io.Reader, maxBytes int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, maxBytes+1))
	if err != nil {
		return n, err
	}
	if n > maxBytes {
		return n, ErrTooLarge
	}
	return n, nil
}

// Worker analyses media jobs.
type Worker struct {
	P       *pipeline.Pipeline
	Objects Objects
	Cfg     config.AnalysisMedia
	kinds   map[string]bool
	once    sync.Once
}

// policySkip is the reason a job is acknowledged without analysis, or ""
// when it is analysed: fbs's media by kind (analysis.media.kinds), the
// contract's by type and role (analysis.media.contract_kinds).
func (w *Worker) policySkip(j core.MediaJob) string {
	if j.AckFormat == core.AckContract {
		pair := j.Kind + "/" + j.Role
		if !w.analysed("contract:" + pair) {
			return "policy:" + pair
		}
	} else if !w.analysed(j.Kind) {
		return "policy"
	}
	if j.IsVideo() && !w.Cfg.Video.Enabled {
		return "policy:video_disabled"
	}
	return ""
}

// errCode is a failure's short reason, for the contract's acknowledgement.
func errCode(err error) string {
	switch {
	case errors.Is(err, ErrTooLarge):
		return "too_large"
	case errors.Is(err, ErrUndecodable):
		return "undecodable"
	case errors.Is(err, ErrGone):
		return "gone"
	case errors.Is(err, ErrNoFFmpeg):
		return "no_ffmpeg"
	}
	return "error"
}

// prompts returns the media prompt and schema of the topic list in use.
func (w *Worker) prompts() (string, map[string]any) {
	sys, schema, _ := w.P.MediaPrompt()
	return sys, schema
}

func (w *Worker) analysed(kind string) bool {
	w.once.Do(func() {
		w.kinds = map[string]bool{}
		for _, k := range w.Cfg.Kinds {
			w.kinds[k] = true
		}
		for _, k := range w.Cfg.ContractKinds {
			w.kinds["contract:"+k] = true
		}
	})
	return w.kinds[kind]
}

// Run processes jobs with analysis.media.workers goroutines until ctx ends.
func (w *Worker) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	n := FitWorkers(w.Cfg.Workers, ContainerMemoryLimit(cgroupRoot), int64(w.Cfg.WorkerMemoryMiB)<<20)
	if n < w.Cfg.Workers {
		slog.Warn("analysis: fewer media workers than configured, to fit the container's memory limit",
			"configured", w.Cfg.Workers, "running", n, "worker_memory_mib", w.Cfg.WorkerMemoryMiB)
	}
	for i := 0; i < n; i++ {
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
	if skip := w.policySkip(j); skip != "" {
		w.finish(ctx, store.MediaResult{Job: j, Consumer: consumer, SkipReason: skip}, "skipped")
		return
	}
	// The same file (SHA-256) analysed before, under another media ID: its
	// result is reused, without fetching the file or calling a provider.
	if res, ok := w.reuse(ctx, j); ok {
		w.finish(ctx, *res, "reused")
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
		if ferr := w.P.Store.FailMedia(context.WithoutCancel(ctx), j, consumer, errCode(err), err.Error(), permanent, w.Cfg.MaxTries); ferr != nil {
			slog.Error("analysis: media failure not recorded", "media", j.MediaID, "err", ferr)
		}
		return
	}
	w.finish(ctx, *res, "analyzed")
}

// reuse returns the stored analysis of an identical file, for this job.
func (w *Worker) reuse(ctx context.Context, j core.MediaJob) (*store.MediaResult, bool) {
	from, res, prov, ok, err := w.P.Store.MediaBySHA(ctx, j.Sha256, j.MediaID)
	if err != nil {
		slog.Warn("analysis: identical media not looked up; analysing", "media", j.MediaID, "err", err)
		return nil, false
	}
	if !ok {
		return nil, false
	}
	owner, err := w.P.Store.OwnerContext(ctx, j.OwnerContentID)
	ownerAt := owner.CreatedAt
	if err != nil || ownerAt.IsZero() {
		ownerAt = time.Now().UTC()
	}
	prov.Route, prov.Lane = core.RouteImage, core.LaneRealtime
	if j.IsVideo() {
		prov.Route = core.RouteVideo
	}
	prov.SchemaVersion = prompt.SchemaVersion
	return &store.MediaResult{Job: j, Result: res, Prov: prov, OwnerTime: ownerAt, Consumer: w.Cfg.Consumer, ReusedFrom: from}, true
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
		return &store.MediaResult{Job: j, Consumer: w.Cfg.Consumer, SkipReason: "route_disabled:" + route}, nil
	}
	// The file: a video streams to a temporary file for ffmpeg, an image
	// to a buffer bounded by image_max_bytes.
	var videoPath string
	var imageData []byte
	if j.IsVideo() {
		path, err := w.fetchVideo(ctx, j)
		if err != nil {
			return nil, err
		}
		defer func() { _ = os.Remove(path) }()
		videoPath = path
	} else {
		var buf bytes.Buffer
		if _, err := w.Objects.Fetch(ctx, j.Bucket, j.Key, w.Cfg.ImageMaxBytes, &buf); err != nil {
			return nil, err
		}
		imageData = buf.Bytes()
	}
	owner, err := w.P.Store.OwnerContext(ctx, j.OwnerContentID)
	if err != nil {
		return nil, err
	}
	ownerAt, postText, postTags := owner.CreatedAt, owner.Text, owner.Tags
	if ownerAt.IsZero() {
		ownerAt = time.Now().UTC()
	}
	var images []provider.Image
	var audio []byte
	if j.IsVideo() {
		f, err := w.video(ctx, j, videoPath)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		for _, img := range f.Images {
			images = append(images, provider.Image{MediaType: "image/jpeg", Data: img})
		}
		audio = f.Audio
	} else {
		img, err := Resize(imageData, w.Cfg.ImageMaxSide, w.Cfg.ImageMaxPixels)
		if err != nil {
			return nil, err
		}
		images = append(images, provider.Image{MediaType: "image/jpeg", Data: img})
	}
	// The video's transcript: the post's caption track when the platform
	// gave one (free), else speech-to-text, which is paid per minute and
	// counts toward the budget, so the check comes before transcribing.
	var transcript, transcriptLang, transcriptSource string
	var stt provider.Transcriber
	var sttModel string
	minutes := AudioMinutes(audio)
	if j.IsVideo() && owner.Captions != "" {
		transcript, transcriptLang, transcriptSource = textnorm.Truncate(owner.Captions, 20000), owner.CaptionsLang, "platform_captions"
	} else {
		stt, sttModel = w.transcriber(audio)
	}
	speechCost := 0.0
	if stt != nil {
		speechCost = minutes * w.P.Pricing.Price(sttModel).PerMinute
	}
	system, schema := w.prompts()
	spoken := int(minutes * 900) // characters a transcript of this audio will add
	if transcript != "" {
		spoken = len(transcript)
	}
	est := w.P.Pricing.Estimate(rc.Model, len(system), len(postText)+spoken, len(images), 250, false) + speechCost
	if ok, err := w.P.Budget.Allow(ctx, route, est, false); err != nil || !ok {
		if err == nil {
			err = errBudget
		}
		return nil, err
	}
	if stt != nil {
		start := time.Now()
		text, err := stt.Transcribe(ctx, audio, "audio.mp3", w.Cfg.Video.STTLanguage)
		if err != nil {
			return nil, fmt.Errorf("transcribe: %w", err)
		}
		transcript = textnorm.Truncate(text, 20000)
		transcriptLang, transcriptSource = w.Cfg.Video.STTLanguage, sttModel
		// Logged as a request of its own, so the day's spend (read back
		// from the request log) includes it.
		w.P.Budget.Add(route, speechCost)
		w.record(ctx, uuid.NewString(), route, w.Cfg.Video.STTProvider, sttModel, false, j.MediaID,
			&provider.Response{Latency: time.Since(start)}, speechCost)
	}
	mask := func(s string) string {
		if w.P.Cfg.Privacy.MaskPII {
			return textnorm.MaskPII(s)
		}
		return s
	}
	// The owner's text leaves as the text analysis sends it: tagged people
	// hidden, then cut, then contact details masked.
	ownerText := w.P.Outbound(postText, postTags, 1000)
	reg := w.P.Entities()
	hintText := postText + " " + j.AltText
	var hints []prompt.Hint
	for _, m := range reg.Find(hintText) {
		hints = append(hints, prompt.Hint{ID: m.EntityID, Name: m.Name, Ambiguous: m.Ambiguous})
	}
	req := provider.Request{
		ID:         uuid.NewString(),
		Model:      rc.Model,
		System:     system,
		User:       prompt.MediaUser(ownerText, hints, mask(transcript), len(images)),
		Images:     images,
		Schema:     schema,
		SchemaName: "media",
		MaxTokens:  rc.MaxTokens,
		Effort:     rc.Effort,
		Thinking:   rc.Thinking,
	}
	resp, prov, model, fallback, err := w.call(ctx, route, rc, req)
	if err != nil {
		return nil, err
	}
	cost := w.P.Pricing.Cost(model, resp.Usage, false)
	w.P.Budget.Add(route, cost)
	w.record(ctx, req.ID, route, prov, model, fallback, j.MediaID, resp, cost)
	if resp.Refused {
		return &store.MediaResult{Job: j, Consumer: w.Cfg.Consumer, SkipReason: "refused:" + model}, nil
	}
	tax := w.P.Taxonomy()
	mr, _, err := prompt.ParseMedia(resp.Text, tax, reg)
	if err != nil {
		return nil, err
	}
	mr.Transcript = transcript
	if transcript != "" {
		// The contract wants both with every transcript: "und"
		// (undetermined) when the language isn't known.
		mr.TranscriptLang, mr.TranscriptSource = orDefault(transcriptLang, "und"), orDefault(transcriptSource, w.Cfg.Video.STTProvider)
	}
	mr.Topics = pipelineTopics(mr.Topics, mr.Entities)
	return &store.MediaResult{Job: j, Result: mr, OwnerTime: ownerAt, Consumer: w.Cfg.Consumer, CostUSD: cost + speechCost,
		Prov: core.Provenance{Provider: prov, Model: model, PromptVersion: prompt.PromptVersion, TaxonomyVersion: tax.Version,
			SchemaVersion: prompt.SchemaVersion, Route: route, Lane: core.LaneRealtime, Fallback: fallback, AnalyzedAt: time.Now().UTC(),
			RequestID: req.ID}}, nil
}

var errBudget = errors.New("media: daily budget reached")

// pipelineTopics adds the "@<entity>" topics of the media's linked
// entities, as for text.
func pipelineTopics(topics []string, ents []core.Entity) []string {
	for _, t := range pipeline.EntityTopics(ents) {
		dup := false
		for _, x := range topics {
			dup = dup || x == t
		}
		if !dup {
			topics = append(topics, t)
		}
	}
	return topics
}

// transcriber returns the configured speech-to-text and its model, or nil
// when there is no audio or none is configured.
func (w *Worker) transcriber(audio []byte) (provider.Transcriber, string) {
	name := w.Cfg.Video.STTProvider
	if len(audio) == 0 || name == "" {
		return nil, ""
	}
	t, ok := w.P.Providers.Transcriber(name)
	if !ok {
		return nil, ""
	}
	return t, w.P.Cfg.Providers[name].TranscribeModel
}

// AudioMinutes is the length of the extracted audio (32 kbit/s MP3, see
// ExtractFrames), which speech-to-text APIs bill.
func AudioMinutes(mp3 []byte) float64 { return float64(len(mp3)) * 8 / 32000 / 60 }

// fetchVideo streams a video into a temporary file (analysis.media.video.
// temp_dir) and returns its path; the caller removes it.
func (w *Worker) fetchVideo(ctx context.Context, j core.MediaJob) (string, error) {
	tmp, err := os.CreateTemp(w.Cfg.Video.TempDir, "sb-media-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	_, err = w.Objects.Fetch(ctx, j.Bucket, j.Key, w.Cfg.MaxBytes, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func (w *Worker) video(ctx context.Context, j core.MediaJob, path string) (*Frames, error) {
	vctx, cancel := context.WithTimeout(ctx, w.Cfg.Video.Timeout)
	defer cancel()
	v := w.Cfg.Video
	return ExtractFrames(vctx, v.FFmpeg, path, v.TempDir, j.DurationMS, v.MaxFrames, v.FrameSide, v.MaxSeconds)
}

// call sends the request to the route's provider, or its fallback when the
// primary is unavailable, fails or refuses.
func (w *Worker) call(ctx context.Context, route string, rc config.Route, req provider.Request) (*provider.Response, string, string, bool, error) {
	prov, model, fallback := rc.Provider, rc.Model, false
	hasFallback := rc.FallbackProvider != "" && w.P.Providers.Has(rc.FallbackProvider)
	if !w.P.Providers.Available(prov) && hasFallback {
		prov, model, fallback = rc.FallbackProvider, rc.FallbackModel, true
		req.Model = model
	}
	resp, err := w.P.Providers.Complete(ctx, prov, req)
	if !fallback && hasFallback && ((err != nil && provider.Retryable(err)) || (err == nil && resp.Refused)) {
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

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
