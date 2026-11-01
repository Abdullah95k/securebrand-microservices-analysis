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

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/adapter"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/ametrics"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/bots"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/curate"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/entities"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/ingest"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/media"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/pipeline"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider/registry"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/relay"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/store"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/taxonomy"
	"github.com/abdullah95k/securebrand-microservices/internal/config"
	"github.com/abdullah95k/securebrand-microservices/internal/platform/kafka"
	"github.com/abdullah95k/securebrand-microservices/internal/platform/run"
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
	tax, err := taxonomy.Load(a.TaxonomyFile)
	if err != nil {
		return nil, err
	}
	providers, err := registry.New(a.Providers)
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
		Cfg: a.Cfg.Analysis.Topics, Taxonomy: a.Pipeline.Taxonomy}
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
			c, err := ingest.New(ingest.Config{Kafka: a.Kafka, Group: cfg.ConsumerGroup, Decoder: adapter.NewDecoder(cfg.Inputs),
				Store: a.Store, Plan: a.Pipeline.Plan, Media: cfg.Media.Enabled, Consumer: cfg.Media.Consumer})
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
					return Every(ctx, "queue-metrics", 15*time.Second, a.Pipeline.QueueMetrics)
				})
		case "media":
			if !cfg.Media.Enabled {
				continue
			}
			if a.S3 == nil {
				return nil, errors.New("media role needs S3")
			}
			w := &media.Worker{P: a.Pipeline, Objects: media.MinioObjects{Client: a.S3}, Cfg: cfg.Media}
			fns = append(fns, w.Run, func(ctx context.Context) error {
				return Every(ctx, "media-ttl", time.Minute, func(ctx context.Context) error {
					n, err := a.Store.TTLAtRisk(ctx, 6*time.Hour)
					ametrics.MediaAtRisk.Set(float64(n))
					return err
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
