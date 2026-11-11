package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/adapter"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/app"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
)

// statsCmd prints, per platform, what came in and what came out over the
// last --since: items ingested, analysed, waiting and failed; media files
// announced, analysed, skipped (by reason) and failed, and how many were
// acknowledged to their collector; and the outbox waiting to be published.
// Read-only; counts only, no content.
func statsCmd(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	since := fs.Duration("since", 24*time.Hour, "count what happened this long ago or later")
	return withApp("stats", args, fs, func(ctx context.Context, a *app.App) error {
		r, err := a.Store.PlatformCounts(ctx, time.Now().Add(-*since), adapter.PlatformName)
		if err != nil {
			return err
		}
		return printStats(os.Stdout, r, *since)
	})
}

func printStats(w io.Writer, r store.PlatformReport, since time.Duration) error {
	var b strings.Builder
	fmt.Fprintf(&b, "last %s (waiting: now)\n", since)
	if len(r.Platforms) == 0 {
		b.WriteString("no platform has items or media in that time\n")
	}
	for _, p := range r.Platforms {
		fmt.Fprintf(&b, "%s\n", p.Platform)
		fmt.Fprintf(&b, "  items: %d ingested, %d analysed (%d refused), %d waiting, %d in dead letters\n",
			p.Ingested, p.Analysed, p.Refused, p.Waiting, p.DeadLetters)
		acked := p.MediaDone + p.MediaSkipped + p.MediaFailed
		fmt.Fprintf(&b, "  media: %d announced, %d analysed; now %d pending, %d done, %d skipped, %d failed, %d gone; %d acknowledged\n",
			p.MediaAnnounced, p.MediaAnalysed, p.MediaPending, p.MediaDone, p.MediaSkipped, p.MediaFailed, p.MediaGone, acked)
		if len(p.MediaReasons) > 0 {
			reasons := make([]string, 0, len(p.MediaReasons))
			for k := range p.MediaReasons {
				reasons = append(reasons, k)
			}
			sort.Strings(reasons)
			parts := make([]string, len(reasons))
			for i, k := range reasons {
				parts[i] = fmt.Sprintf("%s %d", k, p.MediaReasons[k])
			}
			fmt.Fprintf(&b, "  skipped because: %s\n", strings.Join(parts, ", "))
		}
		if pending := p.PendingPast + p.Pending12h + p.Pending24h + p.Pending48h + p.PendingLater; pending > 0 {
			fmt.Fprintf(&b, "  pending now by deadline: %d past it, %d within 12 h, %d in 12-24 h, %d in 24-48 h, %d later or none; %d waiting for their post\n",
				p.PendingPast, p.Pending12h, p.Pending24h, p.Pending48h, p.PendingLater, p.PendingForPost)
		}
		if len(p.MediaFailReasons) > 0 {
			fmt.Fprintf(&b, "  %d waiting to be tried again after a failure; failed or retrying because: %s\n",
				p.MediaRetrying, countsByCount(p.MediaFailReasons))
		}
	}
	topics := make([]string, 0, len(r.Outbox))
	for t := range r.Outbox {
		topics = append(topics, t)
	}
	sort.Strings(topics)
	parts := make([]string, len(topics))
	for i, t := range topics {
		parts[i] = fmt.Sprintf("%s %d", t, r.Outbox[t])
	}
	if len(parts) == 0 {
		parts = []string{"empty"}
	}
	fmt.Fprintf(&b, "outbox (waiting to be published): %s; %d held after a failed publish\n", strings.Join(parts, ", "), r.OutboxHeld)
	_, err := io.WriteString(w, b.String())
	return err
}

// countsByCount formats counts largest first (then by name): "a 9, b 2".
func countsByCount(m map[string]int64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, m[k])
	}
	return strings.Join(parts, ", ")
}
