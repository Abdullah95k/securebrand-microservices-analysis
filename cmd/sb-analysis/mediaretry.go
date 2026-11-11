package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/app"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
)

// mediaRetryCmd puts media files that failed for a given reason back in the
// queue, to be tried again from their first try, while their files still
// exist: their deadline at least --min-left away. Without --apply it only
// counts them. Made for the videos acknowledged failed_retryable while the
// first whisper server couldn't load its model (7-11 Nov 2026). Prints
// counts only.
func mediaRetryCmd(args []string) error {
	fs := flag.NewFlagSet("media-retry", flag.ExitOnError)
	reason := fs.String("reason", "", "text the failure's error contains, e.g. \"transcribe\" (required)")
	platform := fs.String("platform", "", "only this platform (tiktok, youtube...)")
	since := fs.Duration("since", 120*time.Hour, "only files that failed this long ago or later")
	minLeft := fs.Duration("min-left", 6*time.Hour, "only files whose deadline is at least this far away")
	apply := fs.Bool("apply", false, "put them back (without it, only count them)")
	return withApp("media-retry", args, fs, func(ctx context.Context, a *app.App) error {
		if *reason == "" {
			return errors.New("media-retry: --reason is required")
		}
		f := store.MediaRetryFilter{Reason: *reason, Platform: *platform, FailedSince: time.Now().Add(-*since), MinLeft: *minLeft}
		r, err := a.Store.RetryFailedMedia(ctx, f, *apply)
		if err != nil {
			return err
		}
		return printMediaRetry(os.Stdout, f, r, *apply)
	})
}

func printMediaRetry(w io.Writer, f store.MediaRetryFilter, r store.MediaRetry, applied bool) error {
	scope := "every platform"
	if f.Platform != "" {
		scope = f.Platform
	}
	_, err := fmt.Fprintf(w, "failed media containing %q (%s): %d match; %d still have at least %s before their deadline", f.Reason, scope,
		r.Matched, r.InTime, f.MinLeft)
	if err != nil {
		return err
	}
	if applied {
		_, err = fmt.Fprintf(w, "; %d put back to be tried again\n", r.Retried)
	} else {
		_, err = fmt.Fprintf(w, "; none changed (add --apply to put them back)\n")
	}
	return err
}
