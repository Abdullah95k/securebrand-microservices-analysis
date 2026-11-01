// Command sb-eval is the analysis evaluation harness
// (docs/analysis/evaluation.md).
//
//	sb-eval sample   --out /srv/securebrand/gold/gold.labels.jsonl   (on the pilot server)
//	sb-eval label    --file /srv/securebrand/gold/gold.labels.jsonl  (web tool on 127.0.0.1:8099)
//	sb-eval run      --gold FILE --provider anthropic --model claude-haiku-5-5 --out results.jsonl
//	sb-eval report   --gold FILE results*.jsonl
//	sb-eval calibrate --gold FILE --model claude-haiku-5-5 results.jsonl > calibration/claude-haiku-5-5.yaml
//	sb-eval cost     [--measured results.jsonl]
//
// Gold files hold personal data: keep them on the server, never in git.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/entities"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/eval"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/pipeline"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider/registry"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/taxonomy"
	"github.com/abdullah95k/securebrand-microservices/internal/config"
	"github.com/abdullah95k/securebrand-microservices/internal/platform/pg"
)

const usage = `usage: sb-eval <sample|label|run|report|calibrate|cost> [flags]   (sb-eval <command> -h for flags)`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "sample":
		err = sample(ctx, os.Args[2:])
	case "label":
		err = label(ctx, os.Args[2:])
	case "run":
		err = run(ctx, os.Args[2:])
	case "report":
		err = report(os.Args[2:])
	case "calibrate":
		err = calibrate(os.Args[2:])
	case "cost":
		err = cost(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q\n%s", os.Args[1], usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sb-eval:", err)
		os.Exit(1)
	}
}

func loadConfig(path string) (*config.Config, error) {
	return config.Load(path)
}

func registryFromSeed(path string) (*entities.Registry, *entities.Seed, error) {
	seed, err := entities.LoadSeed(path)
	if err != nil {
		return nil, nil, err
	}
	var ents []entities.Entity
	var aliases []entities.Alias
	for _, e := range seed.Entities {
		ents = append(ents, entities.Entity{ID: e.ID, Name: e.Name, NameAR: e.NameAR, Type: e.Type, PublicFigure: e.PublicFigure})
		for _, a := range e.Aliases {
			aliases = append(aliases, entities.Alias{Alias: a, EntityID: e.ID})
		}
	}
	return entities.New(ents, aliases, nil), seed, nil
}

func sample(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sample", flag.ExitOnError)
	cfgPath := fs.String("config", os.Getenv("FBS_CONFIG"), "config file(s) (postgres.dsn points at the pilot database)")
	out := fs.String("out", "", "file to write (outside the repository, e.g. /srv/securebrand/gold/gold.labels.jsonl)")
	comments := fs.Int("comments", eval.DefaultSample.Comments, "comments to sample")
	posts := fs.Int("posts", eval.DefaultSample.Posts, "posts to sample")
	seedFile := fs.String("entities", "", "entity seed (\"\" = built-in) used to oversample brand mentions")
	seed := fs.Uint64("seed", 1, "sampling seed")
	_ = fs.Parse(args)
	if *out == "" {
		return fmt.Errorf("--out is required")
	}
	if err := refuseRepoPath(*out); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	db, err := pg.Open(ctx, cfg.Postgres.DSN, 4)
	if err != nil {
		return err
	}
	defer db.Close()
	reg, _, err := registryFromSeed(*seedFile)
	if err != nil {
		return err
	}
	sc := eval.DefaultSample
	sc.Comments, sc.Posts, sc.Seed = *comments, *posts, *seed
	items, err := eval.Sample(ctx, db, reg, sc)
	if err != nil {
		return err
	}
	if err := eval.WriteItems(*out, items); err != nil {
		return err
	}
	strata := map[string]int{}
	for _, it := range items {
		for _, s := range it.Strata {
			strata[s]++
		}
	}
	fmt.Printf("wrote %d items to %s (mode 600); strata: %v\n", len(items), *out, strata)
	return nil
}

// refuseRepoPath stops gold files from being written inside a git checkout.
func refuseRepoPath(path string) error {
	abs, err := absDir(path)
	if err != nil {
		return err
	}
	for d := abs; d != "/" && d != "."; d = parent(d) {
		if _, err := os.Stat(d + "/.git"); err == nil {
			return fmt.Errorf("%s is inside a git checkout (%s): gold data must stay outside the repository", path, d)
		}
	}
	return nil
}

func label(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("label", flag.ExitOnError)
	file := fs.String("file", "", "the file sb-eval sample wrote; labels are saved into it")
	addr := fs.String("addr", "127.0.0.1:8099", "listen address (127.0.0.1 only)")
	seedFile := fs.String("entities", "", "entity seed for the id suggestions (\"\" = built-in)")
	_ = fs.Parse(args)
	if *file == "" {
		return fmt.Errorf("--file is required")
	}
	tax, err := taxonomy.Load("")
	if err != nil {
		return err
	}
	_, seed, err := registryFromSeed(*seedFile)
	if err != nil {
		return err
	}
	srv, err := eval.NewLabelServer(*file, tax, seed.Entities)
	if err != nil {
		return err
	}
	fmt.Printf("Labelling tool on http://%s (open it through an SSH tunnel; Ctrl+C to stop)\n", *addr)
	return srv.Serve(ctx, *addr)
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", os.Getenv("FBS_CONFIG"), "config file(s) with analysis.providers (API keys via FBS_ANALYSIS__PROVIDERS__<NAME>__API_KEY)")
	gold := fs.String("gold", "", "labelled gold file")
	out := fs.String("out", "", "results file to append to")
	var rc eval.RunConfig
	fs.StringVar(&rc.Provider, "provider", "anthropic", "provider name in analysis.providers")
	fs.StringVar(&rc.Model, "model", "claude-haiku-5-5", "model id")
	fs.IntVar(&rc.GroupSize, "group", 25, "items per request (grouped by post)")
	fs.StringVar(&rc.Effort, "effort", "low", "effort / reasoning_effort (\"\" = provider default)")
	fs.StringVar(&rc.Thinking, "thinking", "off", "off | adaptive")
	fs.IntVar(&rc.MaxTokens, "max-tokens", 0, "max output tokens (0 = 400 + 150 per item)")
	fs.BoolVar(&rc.Batch, "batch", false, "use the provider's batch API (half price; can take up to 24h)")
	fs.BoolVar(&rc.MaskPII, "mask-pii", true, "mask phones and e-mails, as production does")
	limit := fs.Int("limit", 0, "only the first N labelled items (0 = all)")
	_ = fs.Parse(args)
	if *gold == "" || *out == "" {
		return fmt.Errorf("--gold and --out are required")
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	items, err := eval.ReadItems(*gold)
	if err != nil {
		return err
	}
	items = eval.Labelled(items)
	if *limit > 0 && *limit < len(items) {
		items = items[:*limit]
	}
	tax, err := taxonomy.Load(cfg.Analysis.TaxonomyFile)
	if err != nil {
		return err
	}
	reg, _, err := registryFromSeed(cfg.Analysis.EntitiesSeed)
	if err != nil {
		return err
	}
	providers, err := registry.New(cfg.Analysis.Providers)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- operator-named file
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	r := &eval.Runner{Providers: providers, Tax: tax, Entities: reg, Pricing: provider.Pricing(cfg.Analysis.Pricing), Out: json.NewEncoder(f)}
	start := time.Now()
	fmt.Printf("analysing %d items with %s/%s (group %d, thinking %s, batch %v)...\n", len(items), rc.Provider, rc.Model, rc.GroupSize, rc.Thinking, rc.Batch)
	if err := r.Run(ctx, items, rc); err != nil {
		return err
	}
	fmt.Printf("done in %s; results appended to %s. Next: sb-eval report --gold %s %s\n", time.Since(start).Round(time.Second), *out, *gold, *out)
	return nil
}

func readAll(gold string, files []string) ([]eval.Item, []eval.Request, error) {
	items, err := eval.ReadItems(gold)
	if err != nil {
		return nil, nil, err
	}
	var reqs []eval.Request
	for _, f := range files {
		r, err := eval.ReadRequests(f)
		if err != nil {
			return nil, nil, err
		}
		reqs = append(reqs, r...)
	}
	return items, reqs, nil
}

func report(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	gold := fs.String("gold", "", "labelled gold file")
	_ = fs.Parse(args)
	if *gold == "" || fs.NArg() == 0 {
		return fmt.Errorf("usage: sb-eval report --gold FILE results.jsonl [more.jsonl...]")
	}
	items, reqs, err := readAll(*gold, fs.Args())
	if err != nil {
		return err
	}
	fmt.Printf("Gold set: %d items, %d labelled (unsure excluded).\n\n", len(items), len(eval.Labelled(items)))
	eval.WriteReport(os.Stdout, eval.Score(items, reqs))
	fmt.Println("\nScores in %. Models whose sentiment intervals overlap are tied: choose the cheaper.")
	return nil
}

func calibrate(args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	gold := fs.String("gold", "", "labelled gold file")
	model := fs.String("model", "", "model whose results to fit")
	_ = fs.Parse(args)
	if *gold == "" || *model == "" || fs.NArg() == 0 {
		return fmt.Errorf("usage: sb-eval calibrate --gold FILE --model ID results.jsonl")
	}
	items, reqs, err := readAll(*gold, fs.Args())
	if err != nil {
		return err
	}
	var mine []eval.Request
	for _, r := range reqs {
		if r.Model == *model {
			mine = append(mine, r)
		}
	}
	pts := eval.Calibrate(items, mine)
	if len(pts) == 0 {
		return fmt.Errorf("no results of %s match the gold set", *model)
	}
	return yaml.NewEncoder(os.Stdout).Encode(pipeline.Calibration{Model: *model, Points: pts})
}

func cost(args []string) error {
	fs := flag.NewFlagSet("cost", flag.ExitOnError)
	cfgPath := fs.String("config", os.Getenv("FBS_CONFIG"), "config file(s) with analysis.pricing")
	volumes := fs.String("items", "100000000,300000000,1000000000", "text items per year, comma separated")
	measured := fs.String("measured", "", "results file: use its measured tokens per comment")
	model := fs.String("model", "", "with --measured: the model whose tokens to use")
	_ = fs.Parse(args)
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	a := eval.DefaultAssumptions
	if *measured != "" {
		reqs, err := eval.ReadRequests(*measured)
		if err != nil {
			return err
		}
		var in, cached, out, n int
		for _, r := range reqs {
			if (*model == "" || r.Model == *model) && r.Error == "" && len(r.Items) > 1 {
				in, cached, out, n = in+r.Usage.InputTokens, cached+r.Usage.CacheReadTokens, out+r.Usage.OutputTokens, n+len(r.Items)
			}
		}
		if n == 0 {
			return fmt.Errorf("no grouped text requests in %s", *measured)
		}
		a.Comment = eval.Tokens{In: float64(in) / float64(n), Cached: float64(cached) / float64(n), Out: float64(out) / float64(n)}
		fmt.Printf("Measured on %d items.\n", n)
	}
	var vs []float64
	for _, v := range strings.Split(*volumes, ",") {
		var f float64
		if _, err := fmt.Sscan(strings.TrimSpace(v), &f); err != nil {
			return fmt.Errorf("--items: %w", err)
		}
		vs = append(vs, f)
	}
	eval.WriteCostReport(os.Stdout, provider.Pricing(cfg.Analysis.Pricing), a, vs)
	return nil
}

func absDir(path string) (string, error) {
	if !strings.HasPrefix(path, "/") {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = wd + "/" + path
	}
	return parent(path), nil
}

func parent(p string) string {
	i := strings.LastIndex(strings.TrimRight(p, "/"), "/")
	if i <= 0 {
		return "/"
	}
	return p[:i]
}
