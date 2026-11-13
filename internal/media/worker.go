package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ametrics"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/pipeline"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/run"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/prompt"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider/registry"
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
	// stt holds a slot per transcription allowed at once
	// (analysis.media.video.stt_concurrency).
	stt     chan struct{}
	sttOnce sync.Once
	// claims counts this process's claims, for deadline_share's turns.
	claims atomic.Uint64
}

// errSTTBusy: every speech-to-text slot is taken; the video goes back to
// the queue for analysis.media.video.stt_retry_after.
var errSTTBusy = errors.New("media: speech-to-text busy")

// providerRetryAfter is how long a file waits when the provider's circuit
// is open: about the circuit's first cooldown.
const providerRetryAfter = 2 * time.Minute

// notReadYetRetry and notReadYetMargin: an image in a known format this
// service can't read yet (ErrNotReadYet: HEIC, AVIF...) goes back for
// notReadYetRetry without losing a try, while its deadline is more than
// notReadYetMargin away (or, without one, for two days after it arrived),
// so it can still be analysed once the format is read; then it fails for
// good as before, ahead of its deadline (13 Nov: about 67 an hour).
const (
	notReadYetRetry  = 6 * time.Hour
	notReadYetMargin = 18 * time.Hour
)

// canWait reports whether an image not read yet may go back to wait.
func canWait(j core.MediaJob) bool {
	if j.DeleteAfter.IsZero() {
		return !j.ReceivedAt.IsZero() && time.Since(j.ReceivedAt) < 48*time.Hour
	}
	return time.Until(j.DeleteAfter) > notReadYetMargin
}

// ownerWait: the file's post (or comment) isn't stored yet; the job waits
// for it in the queue (analysis.media.owner_wait), at most until Until.
type ownerWait struct{ Until time.Time }

func (e *ownerWait) Error() string { return "media: waiting for its post" }

// serverError: the speech-to-text server answered with an HTTP 5xx (it was
// reached, but couldn't transcribe).
func serverError(err error) bool {
	var perr *provider.Error
	return errors.As(err, &perr) && perr.Status >= 500
}

// ownerRecheck caps one wait for a post: a job whose wake was missed (a
// failed wake, or a race with the ingest that stored the post) looks again
// this often rather than only at the end of its wait.
const ownerRecheck = 6 * time.Hour

// waitForOwner returns the wait when the job's owner item isn't stored and
// the job may still wait for it: until owner_wait after it arrived or
// owner_margin before its deadline, whichever is first. Nil: it goes ahead.
//
// A comment outside the comment sample (analysis.comments.sample_share) is
// counted, not stored, so its files don't wait for it: they are analysed
// without its text.
func (w *Worker) waitForOwner(j core.MediaJob, owner store.Owner) error {
	if owner.Stored || j.OwnerContentID == "" || w.Cfg.OwnerWait <= 0 || !storedKind(j.OwnerContentID) {
		return nil
	}
	if isComment(j.OwnerContentID) && !core.InSample(j.OwnerContentID, w.commentShare()) {
		return nil
	}
	arrived := j.ReceivedAt
	if arrived.IsZero() {
		arrived = time.Now()
	}
	until := arrived.Add(w.Cfg.OwnerWait)
	if !j.DeleteAfter.IsZero() {
		if d := j.DeleteAfter.Add(-w.Cfg.OwnerMargin); d.Before(until) {
			until = d
		}
	}
	if !time.Now().Before(until) {
		return nil
	}
	return &ownerWait{Until: until}
}

// nearDeadline reports whether a video's deadline is within
// media.video.frames_only_within, so it goes ahead without speech-to-text
// rather than wait for a slot and expire.
func (w *Worker) nearDeadline(j core.MediaJob) bool {
	d := w.Cfg.Video.FramesOnlyWithin
	return d > 0 && !j.DeleteAfter.IsZero() && time.Until(j.DeleteAfter) < d
}

// monitoredOnly reports whether a contract file's role is analysed only
// for monitored accounts (media.monitored_roles).
func (w *Worker) monitoredOnly(j core.MediaJob) bool {
	if j.AckFormat != core.AckContract {
		return false
	}
	for _, r := range w.Cfg.MonitoredRoles {
		if r == j.Role {
			return true
		}
	}
	return false
}

// storedKind reports whether items of a content ID's kind are stored here
// (posts and comments), so a file can wait for its owner; an account's,
// audio's or playlist's files have no stored owner to wait for.
func storedKind(id string) bool {
	_, rest, ok := strings.Cut(id, ":")
	return ok && (strings.HasPrefix(rest, "post:") || strings.HasPrefix(rest, "comment:"))
}

// isComment reports whether a content ID names a comment
// ("tt:comment:123").
func isComment(id string) bool {
	_, rest, ok := strings.Cut(id, ":")
	return ok && strings.HasPrefix(rest, "comment:")
}

// commentShare is the comment sample's share (1 when unset: every comment
// stored).
func (w *Worker) commentShare() float64 {
	if s := w.P.Store.CommentShare; s > 0 {
		return s
	}
	return 1
}

// sttSlot takes a transcription slot, waiting at most stt_wait. The
// returned func gives it back.
func (w *Worker) sttSlot(ctx context.Context) (func(), error) {
	w.sttInit()
	t := time.NewTimer(w.Cfg.Video.STTWait)
	defer t.Stop()
	select {
	case w.stt <- struct{}{}:
		return func() { <-w.stt }, nil
	case <-t.C:
		return nil, errSTTBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *Worker) sttInit() {
	w.sttOnce.Do(func() { w.stt = make(chan struct{}, max(w.Cfg.Video.STTConcurrency, 1)) })
}

// sttFull reports whether every speech-to-text slot is taken: the next
// claim then takes images only (on 12-13 Nov, with videos first by
// deadline, the other workers kept claiming videos, waiting for the slot
// and putting them back, and images barely got through).
func (w *Worker) sttFull() bool {
	if !w.Cfg.Video.Enabled || w.Cfg.Video.STTProvider == "" {
		return false
	}
	w.sttInit()
	return len(w.stt) >= cap(w.stt)
}

// policySkip is the reason a job is acknowledged without analysis, or ""
// when it is analysed: fbs's media by kind (analysis.media.kinds), the
// contract's by type and role (analysis.media.contract_kinds).
func (w *Worker) policySkip(j core.MediaJob) string {
	if j.AckFormat == core.AckContract {
		pair := j.Kind + "/" + j.Role
		if !w.analysed("contract:"+pair) && !w.analysed("contract:"+j.Platform+":"+pair) {
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
	if n, err := w.P.Store.WakeCommentMedia(ctx); err != nil {
		slog.Warn("analysis: comments' media not woken", "err", err)
	} else if n > 0 {
		slog.Info("analysis: comments' media woken, to look at again", "files", n)
	}
	var wg sync.WaitGroup
	n := FitWorkers(w.Cfg.Workers, ContainerMemoryLimit(cgroupRoot), int64(w.Cfg.WorkerMemoryMiB)<<20)
	if n < w.Cfg.Workers {
		slog.Warn("analysis: fewer media workers than configured, to fit the container's memory limit",
			"configured", w.Cfg.Workers, "running", n, "worker_memory_mib", w.Cfg.WorkerMemoryMiB)
	}
	// While the queue is empty one worker polls, less and less often up to
	// media.idle_poll (run.Idle): the database is shared.
	idle := &run.Idle{Min: w.Cfg.Poll, Max: w.Cfg.IdlePoll}
	if idle.Min <= 0 {
		idle.Min = time.Second
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
				if n > 0 {
					idle.Busy()
					continue
				}
				idle.Wait(ctx)
			}
		}()
	}
	wg.Wait()
	return nil
}

// Once claims and processes one job; it returns how many it processed.
func (w *Worker) Once(ctx context.Context) (int, error) {
	jobs, err := w.claim(ctx)
	if err != nil || len(jobs) == 0 {
		return 0, err
	}
	for _, j := range jobs {
		w.process(ctx, j)
	}
	return len(jobs), nil
}

// claim leases the next job. Nearest deadline first, unless
// media.recent_first: then the newest of the files announced within it,
// and after them the rest by deadline; deadline_share of the claims go
// first to the files due within deadline_within (the owner's D-060 with
// the master's rule, 13 Nov: a file in its last hours isn't starved by new
// ones while the budget has room).
func (w *Worker) claim(ctx context.Context) ([]core.MediaJob, error) {
	imagesOnly, framesDue := w.sttFull(), w.Cfg.Video.FramesOnlyWithin
	get := func(o store.MediaOrder) ([]core.MediaJob, error) {
		return w.P.Store.ClaimMediaJobs(ctx, 1, w.Cfg.Lease, imagesOnly, framesDue, o)
	}
	if w.Cfg.RecentFirst <= 0 {
		return get(store.MediaOrder{})
	}
	if w.deadlineTurn() {
		if jobs, err := get(store.MediaOrder{DueWithin: w.Cfg.DeadlineWithin}); err != nil || len(jobs) > 0 {
			return jobs, err
		}
	}
	if jobs, err := get(store.MediaOrder{Newest: w.Cfg.RecentFirst}); err != nil || len(jobs) > 0 {
		return jobs, err
	}
	return get(store.MediaOrder{})
}

// deadlineTurn reports whether this claim is one of deadline_share's, as
// close to that share of the claims as whole claims allow (a quarter: the
// 1st, 5th, 9th...).
func (w *Worker) deadlineTurn() bool {
	share := w.Cfg.DeadlineShare
	if share <= 0 || w.Cfg.DeadlineWithin <= 0 {
		return false
	}
	n := float64(w.claims.Add(1) - 1)
	return math.Ceil((n+1)*share) > math.Ceil(n*share)
}

func (w *Worker) process(ctx context.Context, j core.MediaJob) {
	consumer := w.Cfg.Consumer
	if skip := w.policySkip(j); skip != "" {
		w.finish(ctx, store.MediaResult{Job: j, Consumer: consumer, SkipReason: skip}, "skipped")
		return
	}
	// A profile picture or banner only of a monitored account (D-061).
	if w.monitoredOnly(j) {
		ok, err := w.P.Store.IsSource(ctx, j.SourceID)
		if err != nil {
			slog.Warn("analysis: media account not looked up; trying again later", "media", j.MediaID, "err", err)
			if rerr := w.P.Store.ReleaseMedia(context.WithoutCancel(ctx), j.MediaID, time.Minute); rerr != nil {
				slog.Error("analysis: media job not released", "media", j.MediaID, "err", rerr)
			}
			return
		}
		if !ok {
			w.finish(ctx, store.MediaResult{Job: j, Consumer: consumer, SkipReason: "policy:not_monitored"}, "skipped")
			return
		}
	}
	// The same file (SHA-256) analysed before, under another media ID: its
	// result is reused, without fetching the file or calling a provider.
	res, reused, err := w.reuse(ctx, j)
	if err == nil && reused {
		w.finish(ctx, *res, "reused")
		return
	}
	if err == nil {
		res, err = w.analyse(ctx, j)
	}
	if err != nil {
		if ctx.Err() != nil {
			// Shutting down: the job was cut, not failed. It comes back
			// without losing a try (the TTL still applies).
			if rerr := w.P.Store.ReleaseMedia(context.WithoutCancel(ctx), j.MediaID, 0); rerr != nil {
				slog.Error("analysis: media job not released", "media", j.MediaID, "err", rerr)
			}
			return
		}
		if wait := (*ownerWait)(nil); errors.As(err, &wait) {
			// Not a failure: its post isn't stored yet (ingest paused, or
			// the post comes later). Storing the post wakes it.
			ametrics.Media.WithLabelValues(j.Kind, "waiting_for_post").Inc()
			until := wait.Until
			if r := time.Now().Add(ownerRecheck); r.Before(until) {
				until = r
			}
			if rerr := w.P.Store.HoldMediaForOwner(context.WithoutCancel(ctx), j.MediaID, j.OwnerContentID, until); rerr != nil {
				slog.Error("analysis: media job not released", "media", j.MediaID, "err", rerr)
			}
			return
		}
		if errors.Is(err, errBudget) {
			// Not a failure: wait for budget (the TTL still applies).
			if rerr := w.P.Store.ReleaseMedia(context.WithoutCancel(ctx), j.MediaID, 10*time.Minute); rerr != nil {
				slog.Error("analysis: media job not released", "media", j.MediaID, "err", rerr)
			}
			return
		}
		if errors.Is(err, registry.ErrUnavailable) {
			// The provider's circuit is open (its calls have been failing):
			// not this file's fault. It comes back after the circuit's
			// cooldown without losing a try; before 13 Nov it counted one,
			// and an outage longer than a few cooldowns would have failed
			// files for good (the TTL still applies).
			ametrics.Media.WithLabelValues(j.Kind, "provider_unavailable").Inc()
			if rerr := w.P.Store.ReleaseMedia(context.WithoutCancel(ctx), j.MediaID, providerRetryAfter); rerr != nil {
				slog.Error("analysis: media job not released", "media", j.MediaID, "err", rerr)
			}
			return
		}
		if errors.Is(err, errSTTBusy) || errors.Is(err, provider.ErrUnavailable) {
			// Not a failure either: speech-to-text is busy with another
			// video, or no server can be reached (down, or not ready yet);
			// this one comes back later (the TTL still applies).
			ametrics.STTDeferred.WithLabelValues().Inc()
			if rerr := w.P.Store.ReleaseMedia(context.WithoutCancel(ctx), j.MediaID, w.Cfg.Video.STTRetryAfter); rerr != nil {
				slog.Error("analysis: media job not released", "media", j.MediaID, "err", rerr)
			}
			return
		}
		if errors.Is(err, ErrNotReadYet) && canWait(j) {
			ametrics.Media.WithLabelValues(j.Kind, "unknown_format").Inc()
			slog.Warn("analysis: media in a format not read yet; kept for later", "media", j.MediaID, "platform", j.Platform,
				"kind", j.Kind, "err", err)
			if rerr := w.P.Store.ReleaseMedia(context.WithoutCancel(ctx), j.MediaID, notReadYetRetry); rerr != nil {
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
		slog.Warn("analysis: media job failed", "media", j.MediaID, "platform", j.Platform, "kind", j.Kind, "permanent", permanent, "err", err)
		if ferr := w.P.Store.FailMedia(context.WithoutCancel(ctx), j, consumer, errCode(err), err.Error(), permanent, w.Cfg.MaxTries); ferr != nil {
			slog.Error("analysis: media failure not recorded", "media", j.MediaID, "err", ferr)
		}
		return
	}
	w.finish(ctx, *res, "analyzed")
}

// reuse returns the stored analysis of an identical file, for this job; an
// ownerWait error while its post isn't stored, so the reused result is
// dated by its own post.
func (w *Worker) reuse(ctx context.Context, j core.MediaJob) (*store.MediaResult, bool, error) {
	from, res, prov, ok, err := w.P.Store.MediaBySHA(ctx, j.Sha256, j.MediaID)
	if err != nil {
		slog.Warn("analysis: identical media not looked up; analysing", "media", j.MediaID, "err", err)
		return nil, false, nil
	}
	if !ok {
		return nil, false, nil
	}
	owner, err := w.P.Store.OwnerContext(ctx, j.OwnerContentID)
	if err == nil {
		if werr := w.waitForOwner(j, owner); werr != nil {
			return nil, false, werr
		}
	}
	ownerAt := owner.CreatedAt
	if err != nil || ownerAt.IsZero() {
		ownerAt = time.Now().UTC()
	}
	prov.Route, prov.Lane = core.RouteImage, core.LaneRealtime
	if j.IsVideo() {
		prov.Route = core.RouteVideo
	}
	prov.SchemaVersion = prompt.SchemaVersion
	return &store.MediaResult{Job: j, Result: res, Prov: prov, OwnerTime: ownerAt, Consumer: w.Cfg.Consumer, ReusedFrom: from}, true, nil
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
	// The day's (or the route's) budget already spent: back to the queue
	// before the file is fetched or decoded, so a paused budget costs no
	// download and no CPU. The exact check, with the request's estimate,
	// still comes before the paid call.
	if spent, err := w.P.Budget.Exhausted(ctx, route); err != nil {
		return nil, err
	} else if spent {
		return nil, errBudget
	}
	owner, err := w.P.Store.OwnerContext(ctx, j.OwnerContentID)
	if err != nil {
		return nil, err
	}
	// Its post isn't stored yet: the file waits for it, so the post's text
	// and captions go with it, before anything is fetched.
	if err := w.waitForOwner(j, owner); err != nil {
		return nil, err
	}
	// A video without the platform's captions needs speech-to-text: it
	// takes a slot before the file is fetched, so a busy server sends it
	// back to the queue before any work is done.
	// Near its deadline with no slot, it goes ahead from its frames alone.
	framesOnly := false
	if j.IsVideo() && owner.Captions == "" && w.Cfg.Video.STTProvider != "" {
		release, err := w.sttSlot(ctx)
		switch {
		case err == nil:
			defer release()
		case errors.Is(err, errSTTBusy) && w.nearDeadline(j):
			framesOnly = true
		default:
			return nil, err
		}
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
		side := w.Cfg.ImageMaxSide
		if j.Role == "thumbnail" && w.Cfg.ThumbnailMaxSide > 0 && (side <= 0 || w.Cfg.ThumbnailMaxSide < side) {
			side = w.Cfg.ThumbnailMaxSide
		}
		img, err := Resize(imageData, side, w.Cfg.ImageMaxPixels)
		if errors.Is(err, ErrNotReadYet) && w.Cfg.HEIFConvert != "" {
			// HEIC or AVIF (TikTok's photos): converted to JPEG first. If
			// that fails the image waits as before (ErrNotReadYet).
			if name, _ := Sniff(imageData); Convertible(name) {
				jpg, cerr := ConvertHEIF(ctx, w.Cfg.HEIFConvert, imageData, w.Cfg.Video.TempDir)
				if cerr == nil {
					slog.Info("analysis: image converted to JPEG", "platform", j.Platform, "format", name)
					img, err = Resize(jpg, side, w.Cfg.ImageMaxPixels)
				} else if !errors.Is(cerr, ErrNoConverter) {
					slog.Warn("analysis: image not converted", "media", j.MediaID, "platform", j.Platform, "format", name, "err", cerr)
				}
			}
		}
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
	} else if !framesOnly {
		stt, sttModel = w.transcriber(audio)
	}
	noSpeech := false
	if framesOnly {
		ametrics.Transcribed.WithLabelValues(w.Cfg.Video.STTProvider, "frames_only").Inc()
		slog.Info("analysis: video near its deadline without a speech-to-text slot; analysing its frames only",
			"media", j.MediaID, "deadline_in", time.Until(j.DeleteAfter).Round(time.Minute))
		noSpeech = true
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
	est := w.P.Pricing.Estimate(rc.Model, len(system), len(postText)+spoken, len(images), 250, false).HitUSD + speechCost
	if ok, err := w.P.Budget.Allow(ctx, route, est, false); err != nil || !ok {
		if err == nil {
			err = errBudget
		}
		return nil, err
	}
	if stt != nil {
		start := time.Now()
		text, err := stt.Transcribe(ctx, audio, "audio.mp3", w.Cfg.Video.STTLanguage)
		switch {
		case err == nil:
			ametrics.Transcribed.WithLabelValues(w.Cfg.Video.STTProvider, "ok").Inc()
			transcript = textnorm.Truncate(text, 20000)
			transcriptLang, transcriptSource = w.Cfg.Video.STTLanguage, sttModel
			// Logged as a request of its own, so the day's spend (read back
			// from the request log) includes it.
			w.P.Budget.Add(route, speechCost)
			w.record(ctx, uuid.NewString(), route, w.Cfg.Video.STTProvider, sttModel, false, j.MediaID,
				&provider.Response{Latency: time.Since(start)}, speechCost)
		case errors.Is(err, provider.ErrUnavailable) && w.nearDeadline(j):
			// No server reachable, and the deadline near: its frames alone.
			ametrics.Transcribed.WithLabelValues(w.Cfg.Video.STTProvider, "frames_only").Inc()
			slog.Warn("analysis: no speech-to-text server reachable near the video's deadline; analysing its frames only",
				"media", j.MediaID, "err", err)
			noSpeech, speechCost = true, 0
		case errors.Is(err, provider.ErrUnavailable):
			ametrics.Transcribed.WithLabelValues(w.Cfg.Video.STTProvider, "unavailable").Inc()
			return nil, fmt.Errorf("transcribe: %w", err)
		case serverError(err) && j.Tries >= w.Cfg.MaxTries:
			// The video's last try, and the speech-to-text server still
			// answers with an error: its frames are analysed without a
			// transcript rather than the whole video failing (the master,
			// 11 Nov). Its transcript_source reads "none".
			ametrics.Transcribed.WithLabelValues(w.Cfg.Video.STTProvider, "error").Inc()
			slog.Warn("analysis: speech-to-text failed on the video's last try; analysing its frames without a transcript",
				"media", j.MediaID, "err", err)
			noSpeech, speechCost = true, 0
		default:
			ametrics.Transcribed.WithLabelValues(w.Cfg.Video.STTProvider, "error").Inc()
			return nil, fmt.Errorf("transcribe: %w", err)
		}
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
		User:       prompt.MediaUser(prompt.MediaAbout(j.Role), ownerText, hints, mask(transcript), len(images)),
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
	} else if noSpeech {
		mr.TranscriptSource = "none"
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
