// Command sb-analysis is SecureBrand's content analysis service
// (docs/design/analysis-service.md).
//
//	sb-analysis [run] [--roles ingest,pipeline,media,relay,rollup]
//	sb-analysis migrate
//	sb-analysis entities import [--file f.yaml] | review | resolve | merge | track | curate
//	sb-analysis sources monitor --client c --source fb:page:1
//	sb-analysis reanalyze [filters] [--dry-run] [--max-usd N]
//	sb-analysis replay-failed [--route r]
//	sb-analysis agent-role --client c --name "Client" --role sb_agent_c --password-file f
//	sb-analysis rollups recompute --day 2026-11-01
//	sb-analysis topics find <words> | list | approve | rename | merge | reject | curate | import
//	sb-analysis authors list | mark --key k --as bot|human|none --by name [--note why] | log | score
//	sb-analysis kafka-topics [--partitions-cap n] [--replicas n]
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/app"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/entities"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/store"
	appcfg "github.com/abdullah95k/securebrand-microservices/internal/app"
	"github.com/abdullah95k/securebrand-microservices/internal/config"
	"github.com/abdullah95k/securebrand-microservices/internal/platform/httpserver"
	plog "github.com/abdullah95k/securebrand-microservices/internal/platform/log"
	"github.com/abdullah95k/securebrand-microservices/internal/platform/pg"
	"github.com/abdullah95k/securebrand-microservices/internal/platform/run"
	"github.com/abdullah95k/securebrand-microservices/internal/platform/s3x"
)

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "run":
		err = runService(args)
	case "migrate":
		err = withApp("migrate", args, nil, func(ctx context.Context, a *app.App) error {
			if err := store.Migrate(ctx, a.DB); err != nil {
				return err
			}
			fmt.Println("analysis migrations applied")
			return nil
		})
	case "entities":
		err = entitiesCmd(args)
	case "sources":
		err = sourcesCmd(args)
	case "reanalyze":
		err = reanalyzeCmd(args)
	case "replay-failed":
		err = replayCmd(args)
	case "agent-role":
		err = agentRoleCmd(args)
	case "rollups":
		err = rollupsCmd(args)
	case "topics":
		err = topicsCmd(args)
	case "authors":
		err = authorsCmd(args)
	case "kafka-topics":
		err = kafkaTopicsCmd(args)
	case "help", "-h", "--help":
		fmt.Println(usage)
		return
	default:
		err = fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sb-analysis:", err)
		os.Exit(1)
	}
}

const usage = `usage: sb-analysis [run|migrate|entities|sources|reanalyze|replay-failed|agent-role|rollups|topics|authors|kafka-topics] [flags]
  run            run the service (--roles ingest,pipeline,media,relay,rollup; default all)
  migrate        apply the analysis migrations
  entities       import [--file f] | review [--min-seen n] | resolve --norm n --type t --action accept|merge|reject [--entity-id id] [--name n]
                 | merge --from id --into id | track --client c --entity id [--role own|competitor|watch] | curate (decide candidates now)
  sources        monitor --client c --source fb:page:123
  reanalyze      --since 2026-10-01 --until ... --platform --source --entity --model --taxonomy-not --sample 0.1 --limit n --route r [--dry-run] [--max-usd n]
  replay-failed  [--route r] [--limit n]
  agent-role     --client id --name "Name" --role sb_agent_x --password-file /run/secrets/f
  rollups        recompute --day YYYY-MM-DD
  topics         find <words, any language> | list [--status candidate|active|merged|rejected|all] [--min-seen n]
                 | approve --id t [--en --ar --parent p]
                 | rename --id t --en .. --ar .. | merge --from t --into t | reject --id t | curate | import --file f.yaml
  authors        list [--all] [--limit n] | mark --key hex --as bot|human|none --by name [--note why] [--platform facebook]
                 | log [--key hex] | score
  kafka-topics   create the analysis's own Kafka topics (content.*, analysis.*), never fbs's [--partitions-cap n] [--replicas n]`

// withApp loads the config (flags parsed into fs first), opens the
// analysis database and runs fn.
func withApp(name string, args []string, fs *flag.FlagSet, fn func(context.Context, *app.App) error) error {
	if fs == nil {
		fs = flag.NewFlagSet(name, flag.ExitOnError)
	}
	cfg := config.MustLoadFromFlags(fs, args)
	plog.Setup("sb-analysis", cfg.Log.Level, cfg.Log.Format)
	ctx := context.Background()
	dsn := cfg.Analysis.Postgres.DSN
	if dsn == "" {
		dsn = cfg.Postgres.DSN
	}
	db, err := pg.Open(ctx, dsn, cfg.Analysis.Postgres.MaxConns)
	if err != nil {
		return err
	}
	defer db.Close()
	kc, err := appcfg.KafkaClient(cfg, "-analysis")
	if err != nil {
		return err
	}
	a, err := app.New(ctx, cfg, db, kc)
	if err != nil {
		return err
	}
	return fn(ctx, a)
}

func runService(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	roles := fs.String("roles", strings.Join(app.Roles, ","), "comma-separated roles to run: "+strings.Join(app.Roles, ", "))
	migrate := fs.Bool("migrate", true, "apply the analysis migrations at start")
	return withApp("run", args, fs, func(ctx context.Context, a *app.App) error {
		cfg := a.Cfg
		rs := strings.Split(*roles, ",")
		for _, r := range rs {
			if strings.TrimSpace(r) == "media" && cfg.Analysis.Media.Enabled {
				s3, err := s3x.Open(ctx, cfg.S3.Endpoint, cfg.S3.AccessKey, cfg.S3.SecretKey, cfg.S3.Region, cfg.S3.UseSSL)
				if err != nil {
					return err
				}
				a.S3 = s3
			}
		}
		if err := a.Prepare(ctx, *migrate); err != nil {
			return err
		}
		fns, err := a.Components(trim(rs))
		if err != nil {
			return err
		}
		ops := httpserver.NewOps(cfg.HTTP.OpsAddr)
		ops.AddCheck("postgres", a.DB.Ping)
		slog.Info("sb-analysis starting", "roles", rs, "inputs", cfg.Analysis.Inputs)
		comps := []run.Func{ops.Run}
		for _, f := range fns {
			comps = append(comps, f)
		}
		defer func() {
			if a.Producer != nil {
				a.Producer.Close()
			}
		}()
		return run.Group(ctx, comps...)
	})
}

func trim(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func entitiesCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("entities: want import, review, resolve, merge, track or curate")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("entities "+sub, flag.ExitOnError)
	file := fs.String("file", "", "YAML file in the format of configs/analysis/entities.seed.yaml (\"\" = the built-in seed)")
	minSeen := fs.Int("min-seen", 20, "review: candidates seen at least this often")
	norm := fs.String("norm", "", "resolve: candidate (normalised name, as review prints it)")
	typ := fs.String("type", "", "resolve: candidate type")
	action := fs.String("action", "", "resolve: accept, merge or reject")
	entityID := fs.String("entity-id", "", "resolve: new id (accept) or existing id (merge)")
	name := fs.String("name", "", "resolve accept: canonical name")
	from := fs.String("from", "", "merge: entity merged away")
	into := fs.String("into", "", "merge: entity kept")
	client := fs.String("client", "", "track: client id")
	role := fs.String("role", "own", "track: own, competitor or watch")
	return withApp("entities", args, fs, func(ctx context.Context, a *app.App) error {
		switch sub {
		case "import":
			seed, err := entities.LoadSeed(*file)
			if err != nil {
				return err
			}
			source := "owner"
			if *file == "" {
				source = "seed"
			}
			n, err := a.Store.ImportSeed(ctx, seed, source)
			if err != nil {
				return err
			}
			fmt.Printf("imported %d entities\n", n)
		case "review":
			rows, err := a.Store.PendingCandidates(ctx, *minSeen, 200)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "SEEN\tTYPE\tNORM\tAS WRITTEN\tLAST SEEN (Iraq time)")
			for _, r := range rows {
				_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", r.Seen, r.Type, r.Norm, r.Surface, r.LastSeen.In(a.Store.Loc).Format("2006-01-02 15:04"))
			}
			return w.Flush()
		case "resolve":
			if err := a.Store.ResolveCandidate(ctx, *norm, *typ, *action, *entityID, *name); err != nil {
				return err
			}
			fmt.Println("done")
		case "merge":
			if *from == "" || *into == "" {
				return fmt.Errorf("merge needs --from and --into")
			}
			if err := a.Store.MergeEntity(ctx, *from, *into); err != nil {
				return err
			}
			fmt.Println("merged")
		case "curate":
			if err := a.RefreshEntities(ctx); err != nil {
				return err
			}
			n, err := a.EntityCurator().Run(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("%d candidates decided\n", n)
		case "track":
			if *client == "" || *entityID == "" {
				return fmt.Errorf("track needs --client and --entity-id")
			}
			return a.Store.TrackEntity(ctx, *client, *entityID, *role)
		default:
			return fmt.Errorf("entities: unknown subcommand %q", sub)
		}
		return nil
	})
}

func sourcesCmd(args []string) error {
	if len(args) == 0 || args[0] != "monitor" {
		return fmt.Errorf("sources: want monitor")
	}
	fs := flag.NewFlagSet("sources monitor", flag.ExitOnError)
	client := fs.String("client", "", "client id")
	source := fs.String("source", "", "source id, e.g. fb:page:123")
	return withApp("sources", args[1:], fs, func(ctx context.Context, a *app.App) error {
		if *client == "" || *source == "" {
			return fmt.Errorf("needs --client and --source")
		}
		return a.Store.MonitorSource(ctx, *client, *source)
	})
}

func reanalyzeCmd(args []string) error {
	fs := flag.NewFlagSet("reanalyze", flag.ExitOnError)
	var f store.ReanalyzeFilter
	since := fs.String("since", "", "items created on or after this day (YYYY-MM-DD, Iraq time)")
	until := fs.String("until", "", "items created before this day")
	fs.StringVar(&f.Platform, "platform", "", "platform (facebook, ...)")
	fs.StringVar(&f.SourceID, "source", "", "source id")
	fs.StringVar(&f.EntityID, "entity", "", "items mentioning this entity")
	fs.StringVar(&f.Model, "model", "", "items whose current analysis used this model")
	fs.StringVar(&f.TaxonomyNot, "taxonomy-not", "", "items analysed with another taxonomy version")
	fs.Float64Var(&f.Sample, "sample", 0, "share of matching items (0..1)")
	fs.IntVar(&f.Limit, "limit", 100000, "at most this many items")
	route := fs.String("route", "", "route to analyse them on (default: their kind's route)")
	dry := fs.Bool("dry-run", false, "only count items and estimate the cost")
	maxUSD := fs.Float64("max-usd", 0, "refuse when the estimate exceeds this (0 = no cap)")
	return withApp("reanalyze", args, fs, func(ctx context.Context, a *app.App) error {
		loc := a.Store.Loc
		var err error
		if f.Since, err = day(*since, loc); err != nil {
			return err
		}
		if f.Until, err = day(*until, loc); err != nil {
			return err
		}
		items, err := a.Store.SelectForReanalysis(ctx, f)
		if err != nil {
			return err
		}
		byRoute := map[string][]core.Item{}
		for _, it := range items {
			r := *route
			if r == "" {
				r = core.RouteComment
				if it.Kind == core.KindPost {
					r = core.RoutePost
				}
			}
			byRoute[r] = append(byRoute[r], it)
		}
		total := 0.0
		for r, its := range byRoute {
			rc, ok := a.Cfg.Analysis.Routes[r]
			if !ok {
				return fmt.Errorf("route %q not configured", r)
			}
			// Per-item estimate on the batch lane: ~150 input, 80 output tokens.
			est := a.Pipeline.Pricing.Estimate(rc.Model, 0, 450, 0, 80, true) * float64(len(its))
			total += est
			fmt.Printf("%-10s %-20s %8d items  ~$%.2f\n", r, rc.Model, len(its), est)
		}
		fmt.Printf("total ~$%.2f\n", total)
		if *maxUSD > 0 && total > *maxUSD {
			return fmt.Errorf("estimate $%.2f exceeds --max-usd %.2f", total, *maxUSD)
		}
		if *dry {
			return nil
		}
		for r, its := range byRoute {
			n, err := a.Store.Enqueue(ctx, its, r, core.LaneBatch, core.PriorityBackground, "reanalysis")
			if err != nil {
				return err
			}
			fmt.Printf("queued %d on %s\n", n, r)
		}
		return nil
	})
}

func day(s string, loc *time.Location) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.ParseInLocation("2006-01-02", s, loc)
}

func replayCmd(args []string) error {
	fs := flag.NewFlagSet("replay-failed", flag.ExitOnError)
	route := fs.String("route", "", "only this route")
	limit := fs.Int("limit", 10000, "at most this many items")
	return withApp("replay-failed", args, fs, func(ctx context.Context, a *app.App) error {
		n, err := a.Store.ReplayFailed(ctx, *route, *limit)
		if err != nil {
			return err
		}
		fmt.Printf("requeued %d items\n", n)
		return nil
	})
}

func agentRoleCmd(args []string) error {
	fs := flag.NewFlagSet("agent-role", flag.ExitOnError)
	client := fs.String("client", "", "client id")
	name := fs.String("name", "", "client display name")
	role := fs.String("role", "", "database login role to create (e.g. sb_agent_acme)")
	pwFile := fs.String("password-file", "", "file holding the role's password (never pass it as an argument)")
	timeout := fs.Duration("statement-timeout", store.DefaultAgentLimits.StatementTimeout, "query time limit")
	return withApp("agent-role", args, fs, func(ctx context.Context, a *app.App) error {
		if *client == "" || *role == "" || *pwFile == "" {
			return fmt.Errorf("needs --client, --role and --password-file")
		}
		pw, err := os.ReadFile(*pwFile) // #nosec G304 -- operator-named secret file
		if err != nil {
			return err
		}
		l := store.DefaultAgentLimits
		l.StatementTimeout = *timeout
		if *name == "" {
			*name = *client
		}
		if err := a.Store.CreateAgentRole(ctx, *client, *name, *role, strings.TrimSpace(string(pw)), l); err != nil {
			return err
		}
		fmt.Printf("role %s ready: read-only, views of schema agent, client %s\n", *role, *client)
		return nil
	})
}

func rollupsCmd(args []string) error {
	if len(args) == 0 || args[0] != "recompute" {
		return fmt.Errorf("rollups: want recompute --day YYYY-MM-DD")
	}
	fs := flag.NewFlagSet("rollups recompute", flag.ExitOnError)
	d := fs.String("day", "", "day (YYYY-MM-DD)")
	return withApp("rollups", args[1:], fs, func(ctx context.Context, a *app.App) error {
		t, err := time.Parse("2006-01-02", *d)
		if err != nil {
			return err
		}
		return a.Store.RecomputeDay(ctx, t)
	})
}
