package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/app"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
)

// batchesCmd lists the provider batches still open, and those sent in the
// last --since, with their requests' states here and the provider's own
// status and counts. Read-only: it changes nothing and prints no content.
func batchesCmd(args []string) error {
	fs := flag.NewFlagSet("batches", flag.ExitOnError)
	since := fs.Duration("since", 24*time.Hour, "also list the batches sent this long ago or later")
	return withApp("batches", args, fs, func(ctx context.Context, a *app.App) error {
		list, err := a.Store.RecentBatches(ctx, time.Now().Add(-*since))
		if err != nil {
			return err
		}
		return printBatches(ctx, os.Stdout, list, a.Store.Loc, func(name string) (provider.BatchStatuser, bool) {
			b, ok := a.Pipeline.Providers.Batcher(name)
			if !ok {
				return nil, false
			}
			s, ok := b.(provider.BatchStatuser)
			return s, ok
		})
	})
}

// printBatches writes one line per batch: what this side recorded, then
// what the provider reports (or why it couldn't be read).
func printBatches(ctx context.Context, w io.Writer, list []store.BatchInfo, loc *time.Location,
	lookup func(provider string) (provider.BatchStatuser, bool)) error {
	if len(list) == 0 {
		_, err := fmt.Fprintln(w, "no open or recent batches")
		return err
	}
	at := func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.In(loc).Format("2006-01-02 15:04")
	}
	for _, b := range list {
		line := fmt.Sprintf("%s  %s/%s  %s  sent %s  here: %s, %d requests (submitted %d, done %d, failed %d)",
			b.ID, b.Provider, b.Model, b.Route, at(b.SubmittedAt), b.Status, b.Requests, b.Submitted, b.Done, b.Failed)
		if s, ok := lookup(b.Provider); !ok {
			line += "  provider: status not available"
		} else if st, err := s.BatchStatus(ctx, b.ID); err != nil {
			line += "  provider: " + err.Error()
		} else {
			line += fmt.Sprintf("  provider: %s, processing %d, succeeded %d, errored %d, canceled %d, expired %d, expires %s",
				st.Status, st.Processing, st.Succeeded, st.Errored, st.Canceled, st.Expired, at(st.ExpiresAt))
			if !st.EndedAt.IsZero() {
				line += ", ended " + at(st.EndedAt)
			}
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}
