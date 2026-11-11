// Package app wires the analysis service's components from the config;
// cmd/sb-analysis runs them and the end-to-end tests reuse the wiring.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/redis/go-redis/v9"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/adapter"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ametrics"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/bots"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/curate"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/entities"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ingest"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/media"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/pipeline"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/kafka"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/ratelimit"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/run"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider/registry"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/relay"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/taxonomy"
)

// Roles a process can run.
var Roles = []string{"ingest", "pipeline", "media", "relay", "rollup"}

// App holds the wired components.
type App struct {
	Cfg      *config.Config
	DB       *pgxpool.Pool
	Store    *store.Store
	Pipeline *pipeline.Pipeline
	Kafka    kafka.ClientConfig
	S3       *minio.Client
	// Objects, when set, is where the media role reads files instead of
	// S3 (tests).
	Objects  media.Objects
	Producer *kafka.Producer
	// File is the taxonomy file: the fixed list, or the seed of the
	// dynamic one.
	File *taxonomy.Taxonomy
}

// New wires the store, providers, taxonomy, registry and pipeline. db must
// point at the analysis database.
func New(ctx context.Context, cfg *config.Config, db *pgxpool.Pool, kc kafka.ClientConfig) (*App, error) {
	a := cfg.Analysis
	loc, err := time.LoadLocation(a.Timezone)
	if err != nil {
		return nil, err
	}
	st := store.New(db, loc)
	st.ErasureMode = cfg.Analysis.Erasure.Mode
	tax, err := taxonomy.Load(a.TaxonomyFile)
	if err != nil {
		return nil, err
	}
	var opts []registry.Option
	if addr := cfg.Valkey.Address(); addr != "" {
		lf, err := sharedLimiters(cfg)
		if err != nil {
			return nil, err
		}
		opts = append(opts, registry.WithLimiters(lf))
	}
	providers, err := registry.New(a.Providers, opts...)
	if err != nil {
		return nil, err
	}
	calib, err := pipeline.LoadCalibrations(a.CalibrationDir)
	if err != nil {
		return nil, err
	}
	p := pipeline.New(st, providers, tax, a, calib)
	return &App{Cfg: cfg, DB: db, Store: st, Pipeline: p, Kafka: kc, File: tax}, nil
}

// Prepare migrates, seeds the registry when empty, creates the partitions
// around now and loads the registry.
func (a *App) Prepare(ctx context.Context, migrate bool) error {
	if migrate {
		if err := store.Migrate(ctx, a.DB); err != nil {
			return err
		}
	}
	seed, err := entities.LoadSeed(a.Cfg.Analysis.EntitiesSeed)
	if err != nil {
		return err
	}
	if seeded, err := a.Store.SeedIfEmpty(ctx, seed); err != nil {
		return err
	} else if seeded {
		slog.Info("analysis: entity registry seeded", "entities", len(seed.Entities))
	}
	if a.Cfg.Analysis.Topics.Dynamic {
		if seeded, err := a.Store.SeedTopicsIfEmpty(ctx, a.File); err != nil {
			return err
		} else if seeded {
			slog.Info("analysis: topic list seeded", "version", a.File.Version, "topics", a.File.Len())
		}
	}
	if err := a.Store.EnsureWindow(ctx, time.Now(), 1, 2); err != nil {
		return err
	}
	if err := a.RefreshTaxonomy(ctx); err != nil {
		return err
	}
	return a.RefreshEntities(ctx)
}

// RefreshTaxonomy loads the latest version of the dynamic topic list.
func (a *App) RefreshTaxonomy(ctx context.Context) error {
	if !a.Cfg.Analysis.Topics.Dynamic {
		return nil
	}
	tax, err := a.Store.LoadTaxonomy(ctx)
	if err != nil {
		return err
	}
	a.Pipeline.SetTaxonomy(tax)
	return nil
}

// Curator returns the topic curation job.
func (a *App) Curator() *curate.Curator {
	return &curate.Curator{Store: a.Store, Providers: a.Pipeline.Providers, Pricing: provider.Pricing(a.Cfg.Analysis.Pricing),
		Cfg: a.Cfg.Analysis.Topics, Taxonomy: a.Pipeline.Taxonomy, Allow: a.budgetAllow}
}

// budgetAllow checks the daily budget for a call that isn't urgent.
func (a *App) budgetAllow(ctx context.Context, route string, est float64) (bool, error) {
	return a.Pipeline.Budget.Allow(ctx, route, est, false)
}

// EntityCurator returns the entity registry curation job.
func (a *App) EntityCurator() *curate.EntityCurator {
	c := a.Cfg.Analysis
	return &curate.EntityCurator{Store: a.Store, Providers: a.Pipeline.Providers, Pricing: provider.Pricing(c.Pricing),
		Route: c.Topics.Curation, MinSeen: c.Entities.MinSeen, Registry: a.Pipeline.Entities, MaskPII: c.Privacy.MaskPII,
		MaskTags: c.Privacy.MaskTaggedUsers, Allow: a.budgetAllow}
}

// ScoreBots scores the authors active in the window.
func (a *App) ScoreBots(ctx context.Context) (store.BotRun, error) {
	b := a.Cfg.Analysis.Bots
	t := bots.Thresholds{Suspected: b.SuspectedScore, Likely: b.LikelyScore}
	return a.Store.ScoreAuthors(ctx, b.Window, b.MinItems, b.CoordinationAuthors, func(s core.AuthorSignals) (int, string, []string) {
		return bots.Score(s, t)
	})
}

// RefreshEntities reloads the registry snapshot.
func (a *App) RefreshEntities(ctx context.Context) error {
	reg, err := a.Store.LoadRegistry(ctx)
	if err != nil {
		return err
	}
	a.Pipeline.SetEntities(reg)
	return nil
}

// Components returns the run functions of the given roles.
func (a *App) Components(roles []string) ([]func(context.Context) error, error) {
	var fns []func(context.Context) error
	cfg := a.Cfg.Analysis
	for _, r := range roles {
		switch r {
		case "ingest":
			bp := cfg.Backpressure
			// Both gates read one count of the queue per check.
			depth := ingest.SharedDepth(a.Store.QueueDepth, bp.CheckEvery/2)
			gate := &ingest.Gate{Max: bp.MaxQueue, Resume: bp.ResumeQueue, Every: bp.CheckEvery, Depth: depth}
			var postGate *ingest.Gate
			if bp.MaxQueuePosts > 0 {
				postGate = &ingest.Gate{Max: bp.MaxQueuePosts, Resume: bp.ResumeQueuePosts, Every: bp.CheckEvery, Depth: depth}
			}
			c, err := ingest.New(ingest.Config{Kafka: a.Kafka, Group: cfg.ConsumerGroup, Decoder: adapter.NewDecoder(cfg.Inputs),
				Store: a.Store, Plan: a.Pipeline.Plan, Media: cfg.Media.Enabled, Consumer: cfg.Media.Consumer, Gate: gate, PostGate: postGate})
			if err != nil {
				return nil, err
			}
			fns = append(fns, c.Run)
		case "pipeline":
			if err := a.Pipeline.RecordModels(context.Background()); err != nil {
				return nil, err
			}
			fns = append(fns, a.Pipeline.RunRealtime, a.Pipeline.RunBatch,
				func(ctx context.Context) error {
					// Once a minute: the alerts on these wait 10 minutes or
					// more, and the database is shared.
					return Every(ctx, "queue-metrics", time.Minute, a.Pipeline.QueueMetrics)
				})
		case "media":
			if !cfg.Media.Enabled {
				continue
			}
			objs := a.Objects
			if objs == nil {
				if a.S3 == nil {
					return nil, errors.New("media role needs S3")
				}
				objs = media.MinioObjects{Client: a.S3}
			}
			w := &media.Worker{P: a.Pipeline, Objects: objs, Cfg: cfg.Media}
			fns = append(fns, w.Run, func(ctx context.Context) error {
				return Every(ctx, "media-ttl", time.Minute, func(ctx context.Context) error {
					n, err := a.Store.TTLAtRisk(ctx, 6*time.Hour)
					if err != nil {
						return err
					}
					ametrics.MediaAtRisk.Set(float64(n))
					// The backlog by type: a growing video backlog means
					// speech-to-text (or ffmpeg) can't keep up.
					pending, err := a.Store.PendingMedia(ctx)
					if err != nil {
						return err
					}
					byType := map[string]float64{"image": 0, "video": 0}
					for kind, n := range pending {
						t := "image"
						if (&core.MediaJob{Kind: kind}).IsVideo() {
							t = "video"
						}
						byType[t] += float64(n)
					}
					for t, n := range byType {
						ametrics.MediaPending.WithLabelValues(t).Set(n)
					}
					return nil
				})
			})
		case "relay":
			if a.Producer == nil {
				p, err := kafka.NewProducer(a.Kafka)
				if err != nil {
					return nil, err
				}
				a.Producer = p
			}
			rl := &relay.Relay{Store: a.Store, Producer: a.Producer}
			fns = append(fns, rl.Run)
		case "rollup":
			// The scheduled jobs: rollups, topic curation, bot scoring
			// (each takes a database lock, so replicas do not overlap).
			fns = append(fns, func(ctx context.Context) error {
				return Every(ctx, "rollups", cfg.Rollups.Every, func(ctx context.Context) error {
					n, err := a.Store.RecomputeDirtyDays(ctx, 50, 30*time.Second)
					ametrics.RollupDays.WithLabelValues().Add(float64(n))
					return err
				})
			})
			if cfg.Topics.Dynamic {
				cur := a.Curator()
				fns = append(fns, func(ctx context.Context) error {
					return Every(ctx, "topic-curation", cfg.Topics.CurateEvery, func(ctx context.Context) error {
						_, err := cur.Run(ctx)
						return err
					})
				})
			}
			if cfg.Entities.Auto {
				ec := a.EntityCurator()
				fns = append(fns, func(ctx context.Context) error {
					return Every(ctx, "entity-curation", cfg.Entities.CurateEvery, func(ctx context.Context) error {
						_, err := ec.Run(ctx)
						return err
					})
				})
			}
			if cfg.Bots.Enabled {
				fns = append(fns, func(ctx context.Context) error {
					return Every(ctx, "bot-scoring", cfg.Bots.Every, func(ctx context.Context) error {
						run, err := a.ScoreBots(ctx)
						if err == nil {
							slog.Info("analysis: authors scored", "scored", run.Scored, "flagged", run.Flagged, "changed", run.Changed)
						}
						return err
					})
				})
			}
		default:
			return nil, fmt.Errorf("unknown role %q (want %s)", r, strings.Join(Roles, ", "))
		}
	}
	// Every role reads the registry and the topic list: refresh them.
	fns = append(fns, func(ctx context.Context) error {
		return Every(ctx, "entities", time.Minute, func(ctx context.Context) error {
			return errors.Join(a.RefreshTaxonomy(ctx), a.RefreshEntities(ctx))
		})
	})
	return fns, nil
}

// Every runs fn now and then every interval (a minute when unset).
func Every(ctx context.Context, name string, interval time.Duration, fn func(context.Context) error) error {
	if interval <= 0 {
		interval = time.Minute
	}
	return run.Every(ctx, name, interval, fn)
}

// sharedLimiters shares each provider's rps across every process using the
// same Valkey. The client connects lazily: an unreachable Valkey never stops
// the service, each process then limits itself to its share
// (analysis.rate_limit.fallback_divisor), and counts it
// (sb_analysis_rate_limit_fallback_total).
func sharedLimiters(cfg *config.Config) (registry.LimiterFunc, error) {
	tc, err := cfg.Valkey.TLS.Config()
	if err != nil {
		return nil, fmt.Errorf("valkey: %w", err)
	}
	client := redis.NewClient(&redis.Options{Addr: cfg.Valkey.Address(), Password: cfg.Valkey.Password, DB: cfg.Valkey.DB,
		TLSConfig: tc, DialTimeout: 2 * time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	rl := cfg.Analysis.RateLimit
	return func(name string, rps float64) registry.Limiter {
		l := ratelimit.NewShared(client, rl.KeyPrefix+name, rps, max(1, int(rps)), rl.FallbackDivisor)
		l.OnFallback = func(error) { ametrics.RateFallback.WithLabelValues(name).Inc() }
		return l
	}, nil
}
