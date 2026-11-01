package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/app"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/store"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/taxonomy"
)

// topicsCmd reviews and edits the dynamic topic list.
func topicsCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("topics: want list, approve, rename, merge, reject, curate or import")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("topics "+sub, flag.ExitOnError)
	status := fs.String("status", "candidate", "list: candidate, active, merged, rejected or all")
	minSeen := fs.Int("min-seen", 0, "list: candidates used by at least this many items")
	id := fs.String("id", "", "approve, rename, reject: topic id")
	en := fs.String("en", "", "approve, rename: English name")
	ar := fs.String("ar", "", "approve, rename: Arabic name")
	parent := fs.String("parent", "", "approve: move under this top-level topic")
	from := fs.String("from", "", "merge: topic merged away")
	into := fs.String("into", "", "merge: topic kept")
	file := fs.String("file", "", "import: taxonomy YAML file (format of configs/analysis/taxonomy.v1.yaml)")
	return withApp("topics", args, fs, func(ctx context.Context, a *app.App) error {
		if !a.Cfg.Analysis.Topics.Dynamic {
			return fmt.Errorf("analysis.topics.dynamic is off: the taxonomy file is the topic list")
		}
		var d store.TopicDecision
		switch sub {
		case "list":
			st := *status
			if st == "all" {
				st = ""
			}
			rows, err := a.Store.Topics(ctx, st, *minSeen)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tSTATUS\tITEMS\tENGLISH\tARABIC\tMERGED INTO\tLAST USED (Iraq time)")
			for _, r := range rows {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n", r.ID, r.Status, r.Seen, r.EN, r.AR, r.MergedInto,
					r.LastSeen.In(a.Store.Loc).Format("2006-01-02 15:04"))
			}
			return w.Flush()
		case "approve", "rename":
			d = store.TopicDecision{ID: *id, Action: sub, EN: *en, AR: *ar, Parent: *parent}
		case "reject":
			d = store.TopicDecision{ID: *id, Action: sub}
		case "merge":
			d = store.TopicDecision{ID: *from, Action: sub, Into: *into}
		case "curate":
			if err := a.RefreshTaxonomy(ctx); err != nil {
				return err
			}
			v, err := a.Curator().Run(ctx)
			if err != nil {
				return err
			}
			if v == "" {
				fmt.Println("no change")
			} else {
				fmt.Println("topic list updated:", v)
			}
			return nil
		case "import":
			tax, err := taxonomy.Load(*file)
			if err != nil {
				return err
			}
			v, err := a.Store.ImportTaxonomy(ctx, tax, "owner")
			if err != nil {
				return err
			}
			fmt.Println("imported, taxonomy version", v)
			return nil
		default:
			return fmt.Errorf("topics: unknown subcommand %q", sub)
		}
		if d.ID == "" {
			return fmt.Errorf("topics %s: --id (or --from) is required", sub)
		}
		v, err := a.Store.ApplyTopicDecisions(ctx, []store.TopicDecision{d}, "owner "+sub)
		if err != nil {
			return err
		}
		fmt.Println("done, taxonomy version", v)
		return nil
	})
}

// authorsCmd reviews bot detection: flagged authors, the owner's decision
// on one, or a scoring run now.
func authorsCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("authors: want list, mark or score")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("authors "+sub, flag.ExitOnError)
	all := fs.Bool("all", false, "list: every scored author, not only flagged ones")
	limit := fs.Int("limit", 100, "list: at most this many")
	key := fs.String("key", "", "mark: author key (hex, as list prints it)")
	platform := fs.String("platform", "facebook", "mark: platform")
	as := fs.String("as", "", "mark: bot, human or none (back to the score)")
	return withApp("authors", args, fs, func(ctx context.Context, a *app.App) error {
		switch sub {
		case "list":
			rows, err := a.Store.Authors(ctx, !*all, *limit)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "AUTHOR KEY\tPLATFORM\tBOT\tSCORE\tITEMS\tPER DAY\tREASONS\tOWNER")
			for _, r := range rows {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%.1f\t%s\t%s\n", hex.EncodeToString(r.Key), r.Platform, r.Bot, r.Score,
					r.Items, r.PerDay, strings.Join(r.Reasons, ","), r.Override)
			}
			return w.Flush()
		case "mark":
			k, err := hex.DecodeString(*key)
			if err != nil || len(k) == 0 {
				return fmt.Errorf("mark: --key must be the hex author key")
			}
			override := *as
			switch override {
			case "none":
				override = ""
			case "":
				return fmt.Errorf("mark: --as bot, human or none")
			}
			if err := a.Store.OverrideAuthor(ctx, *platform, k, override); err != nil {
				return err
			}
			fmt.Println("done")
			return nil
		case "score":
			run, err := a.ScoreBots(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("scored %d authors, %d flagged, %d changed\n", run.Scored, run.Flagged, run.Changed)
			return nil
		}
		return fmt.Errorf("authors: unknown subcommand %q", sub)
	})
}
