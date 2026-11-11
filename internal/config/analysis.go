package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
	// Timezone data for analysis.timezone: the distroless image has none.
	_ "time/tzdata"

	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"
)

// Analysis configures sb-analysis, the platform-neutral content analysis
// service (docs/design/analysis-service.md).
type Analysis struct {
	// Inputs: "fb" (the Facebook adapter over fb.post.v1, fb.comment.v1,
	// fb.media.ready.v1 and fb.erasure.v1) and/or "content" (the platform's
	// contract: content.post.v1, content.comment.v1, content.media.ready.v1
	// and content.media.deleted.v1).
	Inputs        []string `koanf:"inputs"`
	ConsumerGroup string   `koanf:"consumer_group"`
	// Postgres: "" uses postgres.dsn (the analysis tables live in schema
	// "analysis", so they can share the fbs database).
	Postgres Postgres `koanf:"postgres"`
	// Timezone of rollup days and budget days.
	Timezone string `koanf:"timezone"`
	// TaxonomyFile and EntitiesSeed: "" uses the embedded files in
	// configs/analysis.
	TaxonomyFile string `koanf:"taxonomy_file"`
	EntitiesSeed string `koanf:"entities_seed"`
	// CalibrationDir holds <model>.yaml calibration maps written by sb-eval.
	CalibrationDir string `koanf:"calibration_dir"`

	Grouping  Grouping              `koanf:"grouping"`
	Batch     BatchLane             `koanf:"batch"`
	Realtime  RealtimeLane          `koanf:"realtime"`
	Routes    map[string]Route      `koanf:"routes"`
	Urgent    Urgent                `koanf:"urgent"`
	Providers map[string]Provider   `koanf:"providers"`
	Pricing   map[string]ModelPrice `koanf:"pricing"`
	Budget    AnalysisBudget        `koanf:"budget"`
	RateLimit AnalysisRateLimit     `koanf:"rate_limit"`
	// Backpressure caps the analysis queue: at MaxQueue items ingest stops
	// taking new content (it waits in Kafka) until the queue is down to
	// ResumeQueue.
	Backpressure AnalysisBackpressure `koanf:"backpressure"`
	Media        AnalysisMedia        `koanf:"media"`
	Privacy      AnalysisPrivacy      `koanf:"privacy"`
	Erasure      AnalysisErasure      `koanf:"erasure"`
	Rollups      Rollups              `koanf:"rollups"`
	Limits       AnalysisLimits       `koanf:"limits"`
	Topics       AnalysisTopics       `koanf:"topics"`
	Entities     AnalysisEntities     `koanf:"entities"`
	Bots         AnalysisBots         `koanf:"bots"`
}

// AnalysisTopics configures the dynamic topic list: the model proposes
// topics missing from the list; they are deduplicated, kept as candidates
// and join the list once used by ApproveMinItems items and checked by the
// curation model (duplicates merged, names of people or companies
// rejected).
type AnalysisTopics struct {
	// Dynamic: false keeps the taxonomy file as a fixed list.
	Dynamic         bool          `koanf:"dynamic"`
	ApproveMinItems int           `koanf:"approve_min_items"`
	CurateEvery     time.Duration `koanf:"curate_every"`
	// Curation: provider and model of the daily check ("" provider =
	// approve candidates on the count alone).
	Curation Route `koanf:"curation"`
}

// AnalysisEntities configures the automatic growth of the entity
// registry: names found at least MinSeen times are decided by the
// curation model (analysis.topics.curation) every CurateEvery.
type AnalysisEntities struct {
	Auto        bool          `koanf:"auto"`
	MinSeen     int           `koanf:"min_seen"`
	CurateEvery time.Duration `koanf:"curate_every"`
}

// AnalysisBots configures bot detection from behaviour.
type AnalysisBots struct {
	Enabled bool          `koanf:"enabled"`
	Every   time.Duration `koanf:"every"`
	// Window of activity scored; authors with fewer than MinItems items in
	// it are not scored.
	Window   time.Duration `koanf:"window"`
	MinItems int           `koanf:"min_items"`
	// Score thresholds of the levels (0-100).
	SuspectedScore int `koanf:"suspected_score"`
	LikelyScore    int `koanf:"likely_score"`
	// CoordinationAuthors: the same substantial text from this many
	// authors counts as coordinated.
	CoordinationAuthors int `koanf:"coordination_authors"`
}

// Grouping packs a thread's items into one request.
type Grouping struct {
	MaxItems int `koanf:"max_items"`
	// MaxChars caps the items' text per request (a proxy for input tokens).
	MaxChars int `koanf:"max_chars"`
	// ContextChars truncates the thread's post text sent as context.
	ContextChars int `koanf:"context_chars"`
	// PostShortChars: posts up to this length use the comment route.
	PostShortChars int `koanf:"post_short_chars"`
}

// BatchLane submits provider batch jobs (50% off, results within 24h).
type BatchLane struct {
	SubmitEvery time.Duration `koanf:"submit_every"`
	PollEvery   time.Duration `koanf:"poll_every"`
	MaxRequests int           `koanf:"max_requests"`
	// MaxItems caps the items claimed per submission.
	MaxItems int `koanf:"max_items"`
	// SubmitLease is how long claimed items stay leased while their
	// batch is built and sent: a process that dies or fails before sending
	// gives them back within it.
	SubmitLease time.Duration `koanf:"submit_lease"`
	// Lease is how long items stay claimed once their batch is sent
	// (longer than the provider's 24h window).
	Lease time.Duration `koanf:"lease"`
	// FallbackAfter: when the primary has been unavailable this long, batch
	// items go through the route's fallback in real time.
	FallbackAfter time.Duration `koanf:"fallback_after"`
}

// RealtimeLane calls providers synchronously.
type RealtimeLane struct {
	Workers int `koanf:"workers"`
	// Wait lets a thread's siblings accumulate before an item is claimed.
	Wait  time.Duration `koanf:"wait"`
	Lease time.Duration `koanf:"lease"`
	// Poll is the wait after an empty poll; while the lane stays empty it
	// doubles up to IdlePoll, and one worker polls for all of them.
	Poll     time.Duration `koanf:"poll"`
	IdlePoll time.Duration `koanf:"idle_poll"`
}

// Route configures one content type: which model, lane and fallback.
type Route struct {
	Provider string `koanf:"provider"`
	Model    string `koanf:"model"`
	// Lane: batch or realtime (urgent items always use realtime).
	Lane string `koanf:"lane"`
	// Effort: low | medium | high | "" (provider default). Thinking: off |
	// adaptive.
	Effort    string `koanf:"effort"`
	Thinking  string `koanf:"thinking"`
	MaxTokens int    `koanf:"max_tokens"`
	// Fallback provider and model for the real-time lane ("" = none).
	FallbackProvider string `koanf:"fallback_provider"`
	FallbackModel    string `koanf:"fallback_model"`
	// DailyUSD caps the route's spend per day (0 = only the global budget).
	DailyUSD float64 `koanf:"daily_usd"`
	// Escalation route: items mentioning a tracked entity whose calibrated
	// confidence is below MinConfidence (or sarcastic/mixed) are re-queued
	// on the "escalation" route.
	MinConfidence float64 `koanf:"min_confidence"`
	// SampleRate (audit route): share of analysed items re-analysed.
	SampleRate float64 `koanf:"sample_rate"`
	Enabled    *bool   `koanf:"enabled"`
}

// On reports whether the route is enabled (default true).
func (r Route) On() bool { return r.Enabled == nil || *r.Enabled }

// Urgent decides which items skip the batch lane.
type Urgent struct {
	// TrackedEntities: items whose text matches the alias of an entity some
	// client tracks.
	TrackedEntities bool `koanf:"tracked_entities"`
	// HotAccounts are hot-tier accounts by source id (e.g. "fb:page:123"):
	// their posts, and the comments on them, are analysed in real time.
	// The platform's account registry takes over with the contract.
	HotAccounts []string `koanf:"hot_accounts"`
	// Watchlists: the accounts clients monitor (sb-analysis sources
	// monitor) are hot too, re-read every Refresh.
	Watchlists bool          `koanf:"watchlists"`
	Refresh    time.Duration `koanf:"refresh"`
	// Sources always analysed in real time (older name; same as
	// hot_accounts).
	Sources []string `koanf:"sources"`
}

// Provider is an LLM provider account.
type Provider struct {
	// Kind: anthropic | openai_compat | fake.
	Kind    string `koanf:"kind"`
	BaseURL string `koanf:"base_url"`
	// BaseURLs (openai_compat): a pool of identical speech-to-text servers
	// (the chart's whisper replicas); each takes one transcription at a
	// time. Set analysis.media.video.stt_concurrency to their number.
	BaseURLs []string `koanf:"base_urls"`
	// APIKey: set through SB_ANALYSIS_ANALYSIS__PROVIDERS__<NAME>__API_KEY or a
	// secret config file, never in a committed file.
	APIKey     string        `koanf:"api_key"`
	Timeout    time.Duration `koanf:"timeout"`
	MaxRetries int           `koanf:"max_retries"`
	// RPS caps requests per second to the provider (0 = unlimited): across
	// every process sharing valkey, or per process without it.
	RPS float64 `koanf:"rps"`
	// Batch: whether the provider's batch API is used for the batch lane
	// (otherwise batch-lane items are sent in real time).
	Batch bool `koanf:"batch"`
	// TranscribeModel: speech-to-text model (openai_compat).
	TranscribeModel string `koanf:"transcribe_model"`
}

// ModelPrice is USD per million tokens, list price.
type ModelPrice struct {
	Input      float64 `koanf:"input"`
	Output     float64 `koanf:"output"`
	CacheRead  float64 `koanf:"cache_read"`
	CacheWrite float64 `koanf:"cache_write"`
	// BatchDiscount multiplies every price in the batch lane (0.5 = 50% off).
	BatchDiscount float64 `koanf:"batch_discount"`
	// PerMinute is the price of one audio minute (speech-to-text models).
	PerMinute float64 `koanf:"per_minute"`
}

// AnalysisBackpressure caps the analysis queue's depth.
type AnalysisBackpressure struct {
	MaxQueue    int64         `koanf:"max_queue"`
	ResumeQueue int64         `koanf:"resume_queue"`
	CheckEvery  time.Duration `koanf:"check_every"`
}

// AnalysisRateLimit shares the providers' rps limits through valkey.
type AnalysisRateLimit struct {
	// KeyPrefix names the Valkey keys (one per provider).
	KeyPrefix string `koanf:"key_prefix"`
	// FallbackDivisor: while Valkey is unreachable each process sends at
	// rps / fallback_divisor (set it to the number of processes).
	FallbackDivisor int `koanf:"fallback_divisor"`
}

// AnalysisBudget caps spend per day (in Timezone).
type AnalysisBudget struct {
	DailyUSD float64 `koanf:"daily_usd"` // 0 = unlimited
	// AllowUnlimited must be set for daily_usd 0 outside service.env dev:
	// no cap only by an explicit choice, never by a missing setting.
	AllowUnlimited bool `koanf:"allow_unlimited"`
	// UrgentExempt lets urgent real-time work continue over budget.
	UrgentExempt bool `koanf:"urgent_exempt"`
}

// AnalysisMedia configures image and video analysis.
type AnalysisMedia struct {
	Enabled bool `koanf:"enabled"`
	// Consumer is the name acks carry; it must match media.required_consumers.
	Consumer string `koanf:"consumer"`
	// Kinds analysed, of fbs's media (fb.media.ready.v1); other kinds are
	// acknowledged without analysis.
	Kinds []string `koanf:"kinds"`
	// ContractKinds: the contract's media (content.media.ready.v1)
	// analysed, as "type/role" pairs ("image/content", "video/attachment");
	// other pairs are acknowledged as SKIPPED with reason "policy:type/role".
	ContractKinds []string `koanf:"contract_kinds"`
	Workers       int      `koanf:"workers"`
	// MaxBytes caps a video (streamed to a temporary file, never held in
	// memory); ImageMaxBytes and ImageMaxPixels cap an image (read into
	// memory and decoded). Larger files fail permanently and are acked.
	MaxBytes       int64 `koanf:"max_bytes"`
	ImageMaxBytes  int64 `koanf:"image_max_bytes"`
	ImageMaxPixels int   `koanf:"image_max_pixels"`
	// WorkerMemoryMiB is one worker's worst case (a decoded image at
	// image_max_pixels, or an ffmpeg run); workers are lowered at start to
	// fit the container's memory limit.
	WorkerMemoryMiB int `koanf:"worker_memory_mib"`
	ImageMaxSide    int `koanf:"image_max_side"`
	// Poll is the wait after an empty poll; while the queue stays empty it
	// doubles up to IdlePoll, and one worker polls for all of them.
	Poll     time.Duration `koanf:"poll"`
	IdlePoll time.Duration `koanf:"idle_poll"`
	Lease    time.Duration `koanf:"lease"`
	MaxTries int           `koanf:"max_tries"`
	// OwnerWait and OwnerMargin: a file whose post (or comment) isn't
	// stored yet waits for it in the queue, without counting a try, so the
	// post's text and captions go with it; it goes ahead without them
	// OwnerWait after it arrived or OwnerMargin before its deadline,
	// whichever is first. Storing the post wakes it. OwnerWait 0: no wait.
	OwnerWait   time.Duration `koanf:"owner_wait"`
	OwnerMargin time.Duration `koanf:"owner_margin"`
	Video       VideoConfig   `koanf:"video"`
}

// VideoConfig: key frames with ffmpeg, speech-to-text through a provider.
type VideoConfig struct {
	Enabled     bool   `koanf:"enabled"`
	FFmpeg      string `koanf:"ffmpeg"`
	MaxFrames   int    `koanf:"max_frames"`
	FrameSide   int    `koanf:"frame_side"`
	MaxSeconds  int    `koanf:"max_seconds"`
	STTProvider string `koanf:"stt_provider"`
	STTLanguage string `koanf:"stt_language"`
	// STTConcurrency: transcriptions at once per media process. A video
	// that needs one while all are busy waits up to STTWait, then goes
	// back to the queue for STTRetryAfter (without counting a try), so
	// images keep flowing and a slow speech-to-text server isn't flooded.
	STTConcurrency int           `koanf:"stt_concurrency"`
	STTWait        time.Duration `koanf:"stt_wait"`
	STTRetryAfter  time.Duration `koanf:"stt_retry_after"`
	Timeout        time.Duration `koanf:"timeout"`
	TempDir        string        `koanf:"temp_dir"`
}

// AnalysisPrivacy controls what leaves the service and what is stored.
type AnalysisPrivacy struct {
	// MaskPII replaces phone numbers and e-mail addresses before text is
	// sent to a provider.
	MaskPII bool `koanf:"mask_pii"`
	// MaskTaggedUsers replaces the names of tagged accounts with "@user"
	// before text is sent to a provider; tagged entities of the registry
	// (brands, public figures) stay. The stored text is unchanged.
	MaskTaggedUsers bool `koanf:"mask_tagged_users"`
}

// Erasure modes (analysis.erasure.mode).
const (
	// ErasureApply deletes the subject's analysis data.
	ErasureApply = "apply"
	// ErasureRecord stores the request and deletes nothing.
	ErasureRecord = "record"
	// ErasureOff ignores the request (it is only counted and logged).
	ErasureOff = "off"
)

// AnalysisErasure decides what an erasure event does to analysis data.
// The owner keeps all data (no deletion): "record" stores the request and
// deletes nothing, so data is deleted only after the owner switches to
// "apply".
type AnalysisErasure struct {
	Mode string `koanf:"mode"`
}

// Rollups recomputes the daily rollups of days marked dirty.
type Rollups struct {
	Every time.Duration `koanf:"every"`
}

// AnalysisLimits bounds retries and work sizes.
type AnalysisLimits struct {
	MaxAttempts int `koanf:"max_attempts"`
	// CandidateMinSeen: unknown names seen this often appear in the review.
	CandidateMinSeen int `koanf:"candidate_min_seen"`
}

// RouteNames are the routes the service knows.
var RouteNames = []string{"comment", "post", "image", "video", "escalation", "audit"}

// Validate rejects analysis configs that would misbehave at runtime.
func (a *Analysis) Validate() error {
	var errs []error
	switch a.Erasure.Mode {
	case ErasureApply, ErasureRecord, ErasureOff:
	default:
		errs = append(errs, fmt.Errorf("analysis.erasure.mode %q must be apply, record or off", a.Erasure.Mode))
	}
	for _, in := range a.Inputs {
		if in != "fb" && in != "content" {
			errs = append(errs, fmt.Errorf("analysis.inputs: %q must be fb or content", in))
		}
	}
	for _, k := range a.Media.ContractKinds {
		if !ValidContractKind(k) {
			errs = append(errs, fmt.Errorf("analysis.media.contract_kinds: %q must be type/role, from the contract's media types and roles", k))
		}
	}
	for name, r := range a.Routes {
		known := false
		for _, n := range RouteNames {
			known = known || n == name
		}
		if !known {
			errs = append(errs, fmt.Errorf("analysis.routes.%s: unknown route (want one of %v)", name, RouteNames))
			continue
		}
		if !r.On() {
			continue
		}
		if _, ok := a.Providers[r.Provider]; !ok {
			errs = append(errs, fmt.Errorf("analysis.routes.%s.provider %q is not in analysis.providers", name, r.Provider))
		}
		if r.Model == "" {
			errs = append(errs, fmt.Errorf("analysis.routes.%s.model is empty", name))
		}
		if r.Lane != "batch" && r.Lane != "realtime" {
			errs = append(errs, fmt.Errorf("analysis.routes.%s.lane %q must be batch or realtime", name, r.Lane))
		}
		if r.FallbackProvider != "" {
			if _, ok := a.Providers[r.FallbackProvider]; !ok {
				errs = append(errs, fmt.Errorf("analysis.routes.%s.fallback_provider %q is not in analysis.providers", name, r.FallbackProvider))
			}
			if r.FallbackModel == "" {
				errs = append(errs, fmt.Errorf("analysis.routes.%s.fallback_model is empty", name))
			}
		}
		switch r.Thinking {
		case "", "off", "adaptive":
		default:
			errs = append(errs, fmt.Errorf("analysis.routes.%s.thinking %q must be off or adaptive", name, r.Thinking))
		}
		if r.SampleRate < 0 || r.SampleRate > 1 {
			errs = append(errs, fmt.Errorf("analysis.routes.%s.sample_rate must be in [0, 1]", name))
		}
	}
	for name, p := range a.Providers {
		switch p.Kind {
		case "anthropic", "openai_compat", "fake":
		default:
			errs = append(errs, fmt.Errorf("analysis.providers.%s.kind %q must be anthropic, openai_compat or fake", name, p.Kind))
		}
		if p.Kind == "openai_compat" && p.BaseURL == "" && len(p.BaseURLs) == 0 {
			errs = append(errs, fmt.Errorf("analysis.providers.%s.base_url is required for openai_compat", name))
		}
	}
	if a.Grouping.MaxItems <= 0 || a.Grouping.MaxChars <= 0 {
		errs = append(errs, errors.New("analysis.grouping.max_items and max_chars must be > 0"))
	}
	if a.Urgent.Watchlists && a.Urgent.Refresh <= 0 {
		errs = append(errs, errors.New("analysis.urgent.refresh must be > 0 with watchlists"))
	}
	if b := a.Backpressure; b.MaxQueue <= 0 || b.ResumeQueue < 0 || b.ResumeQueue >= b.MaxQueue || b.CheckEvery <= 0 {
		errs = append(errs, errors.New("analysis.backpressure: max_queue must be > 0, resume_queue between 0 and below it, check_every > 0"))
	}
	if a.Budget.DailyUSD < 0 {
		errs = append(errs, errors.New("analysis.budget.daily_usd must be >= 0"))
	}
	if a.Media.Enabled && a.Media.Consumer == "" {
		errs = append(errs, errors.New("analysis.media.consumer is required when media is enabled"))
	}
	if a.Media.Enabled && (a.Media.MaxBytes <= 0 || a.Media.ImageMaxBytes <= 0 || a.Media.ImageMaxPixels <= 0 || a.Media.WorkerMemoryMiB <= 0) {
		errs = append(errs, errors.New("analysis.media.max_bytes, image_max_bytes, image_max_pixels and worker_memory_mib must be > 0"))
	}
	if a.Media.Video.Enabled && a.Media.Video.STTProvider != "" {
		p, ok := a.Providers[a.Media.Video.STTProvider]
		if !ok {
			errs = append(errs, fmt.Errorf("analysis.media.video.stt_provider %q is not in analysis.providers", a.Media.Video.STTProvider))
		}
		// A pool of servers takes one video each: fewer transcriptions at
		// once than servers would leave some idle.
		if n := len(p.BaseURLs); ok && n > 0 && a.Media.Video.STTConcurrency < n {
			errs = append(errs, fmt.Errorf("analysis.media.video.stt_concurrency %d is below the %d servers of analysis.providers.%s.base_urls",
				a.Media.Video.STTConcurrency, n, a.Media.Video.STTProvider))
		}
	}
	if _, err := time.LoadLocation(a.Timezone); err != nil {
		errs = append(errs, fmt.Errorf("analysis.timezone: %w", err))
	}
	if a.Limits.MaxAttempts <= 0 {
		errs = append(errs, errors.New("analysis.limits.max_attempts must be > 0"))
	}
	if a.Topics.Dynamic {
		if a.Topics.ApproveMinItems <= 0 {
			errs = append(errs, errors.New("analysis.topics.approve_min_items must be > 0"))
		}
		if c := a.Topics.Curation; c.Provider != "" {
			if _, ok := a.Providers[c.Provider]; !ok {
				errs = append(errs, fmt.Errorf("analysis.topics.curation.provider %q is not in analysis.providers", c.Provider))
			}
			if c.Model == "" {
				errs = append(errs, errors.New("analysis.topics.curation.model is empty"))
			}
		}
	}
	if a.Entities.Auto && (a.Entities.MinSeen <= 0 || a.Topics.Curation.Provider == "") {
		errs = append(errs, errors.New("analysis.entities.auto needs min_seen > 0 and analysis.topics.curation.provider"))
	}
	if b := a.Bots; b.Enabled {
		if b.Window <= 0 || b.MinItems <= 0 || b.CoordinationAuthors < 2 {
			errs = append(errs, errors.New("analysis.bots: window and min_items must be > 0, coordination_authors >= 2"))
		}
		if b.SuspectedScore <= 0 || b.LikelyScore < b.SuspectedScore || b.LikelyScore > 100 {
			errs = append(errs, errors.New("analysis.bots: need 0 < suspected_score <= likely_score <= 100"))
		}
	}
	return errors.Join(errs...)
}

// ValidContractKind reports whether k is "type/role" with a type and a role
// of the contract's MediaType and MediaRole, in lower case without their
// prefixes ("image/content").
func ValidContractKind(k string) bool {
	t, r, ok := strings.Cut(k, "/")
	if !ok || t == "unspecified" || r == "unspecified" {
		return false
	}
	_, tok := contentv1.MediaType_value["MEDIA_TYPE_"+strings.ToUpper(t)]
	_, rok := contentv1.MediaRole_value["MEDIA_ROLE_"+strings.ToUpper(r)]
	return tok && rok && t == strings.ToLower(t) && r == strings.ToLower(r)
}
