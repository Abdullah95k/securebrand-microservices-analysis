package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/app"
)

// erasuresCmd lists the erasure requests received, or applies one that
// analysis.erasure.mode "record" stored: deletion only on the owner's
// explicit order.
func erasuresCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("erasures: want list or apply")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("erasures "+sub, flag.ExitOnError)
	limit := fs.Int("limit", 100, "list: at most this many")
	request := fs.String("request", "", "apply: the request id, as list prints it")
	confirm := fs.Bool("yes-delete", false, "apply: confirm that the subject's analysis data is deleted for good")
	return withApp("erasures", args, fs, func(ctx context.Context, a *app.App) error {
		switch sub {
		case "list":
			rows, err := a.Store.RecordedErasures(ctx, *limit)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "RECEIVED (Iraq time)\tREQUEST\tSUBJECT\tMODE\tDELETED")
			for _, r := range rows {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\n", r.ReceivedAt.In(a.Store.Loc).Format("2006-01-02 15:04"), r.RequestID,
					r.Subject, r.Mode, r.Done)
			}
			return w.Flush()
		case "apply":
			if *request == "" {
				return fmt.Errorf("apply: --request is required")
			}
			if !*confirm {
				return fmt.Errorf("apply deletes the subject's analysis data for good: add --yes-delete to confirm")
			}
			stats, err := a.Store.ApplyRecordedErasure(ctx, *request, a.Cfg.Analysis.Media.Consumer)
			if err != nil {
				return err
			}
			fmt.Printf("erasure %s applied: %v\n", *request, stats)
			return nil
		}
		return fmt.Errorf("erasures: unknown %q (want list or apply)", sub)
	})
}
