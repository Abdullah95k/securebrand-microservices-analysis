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
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/pipeline"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
)

// statsCmd prints, per platform, what came in and what came out over the
// last --since: items ingested, analysed, waiting and failed; media files
// announced, analysed, skipped (by reason) and failed, and how many were
// acknowledged to their collector; the outbox waiting to be published; the
// provider requests by route and lane (how full, re-sent, tokens and cost);
// and what the real-time lane's rules have to work with. Read-only; counts
// only, no content.
func statsCmd(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	since := fs.Duration("since", 24*time.Hour, "count what happened this long ago or later")
	return withApp("stats", args, fs, func(ctx context.Context, a *app.App) error {
		from := time.Now().Add(-*since)
		r, err := a.Store.PlatformCounts(ctx, from, a.Cfg.Analysis.Batch.RecentFirst, adapter.PlatformName)
		if err != nil {
			return err
		}
		if r.Requests, r.Resent, err = a.Store.RequestCounts(ctx, from); err != nil {
			return err
		}
		if r.Urgent, err = a.Store.UrgentCounts(ctx); err != nil {
			return err
		}
		if r.Sample, err = a.Store.CommentSampleCounts(ctx, from); err != nil {
			return err
		}
		if r.Tries, err = a.Store.QueueTriesNow(ctx); err != nil {
			return err
		}
		if r.Spend, err = a.Store.SpendByRoute(ctx, a.Store.DayStart(time.Now())); err != nil {
			return err
		}
		if r.Measures, err = a.Store.RouteMeasures(ctx, time.Now().Add(-24*time.Hour)); err != nil {
			return err
		}
		r.DefaultOutPerItem = pipeline.DefaultOutPerItem
		if r.Backfills, err = a.Store.Backfills(ctx); err != nil {
			return err
		}
		r.DailyCap, r.RouteCaps = a.Cfg.Analysis.Budget.DailyUSD, map[string]float64{}
		for name, rc := range a.Cfg.Analysis.Routes {
			if rc.DailyUSD > 0 {
				r.RouteCaps[name] = rc.DailyUSD
			}
		}
		u := a.Cfg.Analysis.Urgent
		r.HotAccounts, r.Watchlists = len(u.HotAccounts)+len(u.Sources), u.Watchlists
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
		oldest := ""
		if p.Waiting > 0 {
			recent := ""
			if r.RecentWindow > 0 {
				recent = fmt.Sprintf(", %d published in the last %g h not sent yet", p.Recent, r.RecentWindow.Hours())
			}
			oldest = fmt.Sprintf(" (%d in flight, %d backfill%s; the oldest not sent for %s)", p.InFlight, p.Backfill, recent, waited(p.OldestWaiting))
		}
		fmt.Fprintf(&b, "  items: %d ingested, %d analysed (%d refused), %d waiting%s, %d in dead letters\n",
			p.Ingested, p.Analysed, p.Refused, p.Waiting, oldest, p.DeadLetters)
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
	printRequests(&b, r)
	_, err := io.WriteString(w, b.String())
	return err
}

// printRequests adds the provider requests by route and lane, and the
// real-time lane's setup.
func printRequests(b *strings.Builder, r store.PlatformReport) {
	if len(r.Requests) == 0 {
		b.WriteString("requests: none\n")
	}
	per := func(n, of int64) int64 {
		if of == 0 {
			return 0
		}
		return (n + of/2) / of
	}
	for _, q := range r.Requests {
		itemsPer, onePct := 0.0, int64(0)
		if q.Requests > 0 {
			itemsPer = float64(q.Items) / float64(q.Requests)
			onePct = per(100*q.OneItem, q.Requests)
		}
		fmt.Fprintf(b, "requests, %s route, %s lane: %d sent (%d done, %d failed, %d in flight), %d items, %.2f items a request, %d with one item (%d%%)\n",
			q.Route, q.Lane, q.Requests, q.Done, q.Failed, q.InFlight, q.Items, itemsPer, q.OneItem, onePct)
		if q.Done > 0 {
			per1k := 0.0
			if q.DoneItems > 0 {
				per1k = q.CostUSD / float64(q.DoneItems) * 1000
			}
			fmt.Fprintf(b, "  per done request: %d input, %d cache read, %d cache write, %d output tokens; $%.2f in all, $%.3f per 1,000 items\n",
				per(q.InputTokens, q.Done), per(q.CacheReadTokens, q.Done), per(q.CacheWriteTokens, q.Done), per(q.OutputTokens, q.Done),
				q.CostUSD, per1k)
		}
		if len(q.FailReasons) > 0 {
			fmt.Fprintf(b, "  failed because (no answer, nothing paid): %s\n", countsByCount(q.FailReasons))
		}
	}
	routes := make([]string, 0, len(r.Resent))
	for route := range r.Resent {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	for _, route := range routes {
		rs := r.Resent[route]
		fmt.Fprintf(b, "re-sent, %s route: %d items sent more than once (%d extra sends)\n", route, rs.Items, rs.ExtraSends)
	}
	watch := "watchlists not counted (urgent.watchlists off)"
	if r.Watchlists {
		watch = fmt.Sprintf("%d accounts monitored by %d clients", r.Urgent.MonitoredSources, r.Urgent.Clients)
	}
	fmt.Fprintf(b, "real-time lane rules: %d hot accounts in the config; %s; %d entities tracked\n",
		r.HotAccounts, watch, r.Urgent.TrackedEntities)
	if len(r.Spend) > 0 {
		var total, worst float64
		for _, s := range r.Spend {
			total += s.Counted
			worst += s.Worst
		}
		capText := "no daily cap"
		if r.DailyCap > 0 {
			capText = fmt.Sprintf("of $%.2f", r.DailyCap)
		}
		fmt.Fprintf(b, "spend today (Iraq day, as the budget counts it): $%.2f %s; worst case $%.2f (every request in flight missing the cache)\n",
			total, capText, worst)
		for _, s := range r.Spend {
			routeCap := ""
			if c := r.RouteCaps[s.Route]; c > 0 {
				routeCap = fmt.Sprintf(" of $%.2f", c)
			}
			priced := fmt.Sprintf("output at %.0f tokens an item and the prompt as a cache miss (the defaults: under %d answered in the last day)",
				r.DefaultOutPerItem, store.MinMeasuredRequests)
			if m, ok := r.Measures[s.Route]; ok {
				priced = fmt.Sprintf("output at %.0f tokens an item and the prompt %.0f%% read from the cache (measured over the last day's %d answered)",
					m.OutPerItem, 100*m.CacheHit, m.Requests)
			}
			fmt.Fprintf(b, "  %s: $%.2f%s (answered $%.2f; %d requests in flight expected at $%.2f, $%.2f at worst); worst case $%.2f; new requests priced with %s\n",
				s.Route, s.Counted, routeCap, s.Done, s.Requests, s.InFlight, s.InFlightMax, s.Worst, priced)
		}
	}
	pages := "not started yet"
	for _, f := range r.Backfills {
		if f.Name != store.BackfillPages {
			continue
		}
		state := "running"
		switch {
		case !f.FinishedAt.IsZero():
			state = "done"
		case f.LastAt.IsZero():
			state = "started, no pass yet"
		}
		pages = fmt.Sprintf("%s: %d queue entries read, %d given their page", state, f.Scanned, f.Updated)
	}
	fmt.Fprintf(b, "page backfill (short posts queued before 00011 given their page, for the page top-up): %s\n", pages)
	for _, q := range r.Tries {
		fmt.Fprintf(b, "queue now, %s route, by tries counted: %d none, %d one, %d two, %d three or more; %d last failed on their own answer (sent alone)\n",
			q.Route, q.None, q.One, q.Two, q.ThreeOrMore, q.ItemFaults)
		if len(q.Reasons) > 0 {
			fmt.Fprintf(b, "  came back because: %s\n", countsByCount(top(q.Reasons, 8)))
		}
	}
	for _, c := range r.Sample {
		pct := 0.0
		if c.Received > 0 {
			pct = 100 * float64(c.Sampled) / float64(c.Received)
		}
		scale := ""
		if c.Share > 0 && c.Share < 1 {
			scale = fmt.Sprintf("; scale the analysed comments by %.4g for all", 1/c.Share)
		}
		fmt.Fprintf(b, "comments, %s (by Iraq day, since the window's first): %d received, %d in the %.4g%% sample (%.1f%%), %d kept outside it for the real-time lane; the rest counted, not stored%s\n",
			c.Platform, c.Received, c.Sampled, 100*c.Share, pct, c.Urgent, scale)
	}
}

// waited is a wait in minutes under an hour, else in hours.
func waited(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%.1f h", d.Hours())
}

// top keeps the n largest counts (ties by name).
func top(m map[string]int64, n int) map[string]int64 {
	if len(m) <= n {
		return m
	}
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
	out := make(map[string]int64, n)
	for _, k := range keys[:n] {
		out[k] = m[k]
	}
	return out
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
