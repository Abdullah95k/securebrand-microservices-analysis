package metrics

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	alertsFile = "../../../deploy/helm/sb-analysis/files/alerts.yaml"
	runbook    = "../../../docs/runbooks/analysis.md"
)

// registered returns every metric name sb-analysis can expose (from each
// collector's description, so vectors count before they have series), with
// the series names Prometheus derives from histograms.
func registered(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	fq := regexp.MustCompile(`fqName: "([^"]+)"`)
	for _, c := range []prometheus.Collector{AnalysisIngested, AnalysisUndecodable, AnalysisIngestPaused, AnalysisAnalyzed, AnalysisItemsFailed,
		AnalysisRequests, AnalysisRequestSecs, AnalysisTokens, AnalysisCostUSD, AnalysisFallbacks, AnalysisRepairs,
		AnalysisRefusals, AnalysisFollowUps, AnalysisDeduplicated, AnalysisDeadLetters, AnalysisQueueDepth, AnalysisQueueAge,
		AnalysisSpentToday, AnalysisBudgetUSD, AnalysisBudgetPaused, AnalysisOpenBatches, AnalysisMedia, AnalysisMediaAtRisk,
		AnalysisMediaPending, AnalysisTranscriptions, AnalysisSTTDeferred,
		AnalysisOutboxBacklog, AnalysisPublished, AnalysisPublishFailed, AnalysisOutboxHeld, AnalysisErasures, AnalysisRollupDays, AnalysisRateFallback,
		KafkaProduce, KafkaCommitted, KafkaHandlerPanics} {
		ch := make(chan *prometheus.Desc, 4)
		c.Describe(ch)
		close(ch)
		for d := range ch {
			m := fq.FindStringSubmatch(d.String())
			if m == nil {
				t.Fatalf("no name in %s", d)
			}
			names[m[1]] = true
			if _, ok := c.(*prometheus.HistogramVec); ok {
				for _, s := range []string{"_bucket", "_sum", "_count"} {
					names[m[1]+s] = true
				}
			}
		}
	}
	return names
}

// Every metric an alert reads exists, under the sb_analysis_ namespace.
func TestAlertMetricsExist(t *testing.T) {
	b, err := os.ReadFile(alertsFile)
	if err != nil {
		t.Fatal(err)
	}
	names := registered(t)
	refs := regexp.MustCompile(`\bsb_analysis_[a-z0-9_]+`).FindAllString(string(b), -1)
	if len(refs) == 0 {
		t.Fatal("no metrics found in the alert rules")
	}
	for _, r := range refs {
		if !names[r] {
			t.Errorf("alert rule reads %s, which sb-analysis does not expose", r)
		}
	}
	if strings.Contains(string(b), "fbs_") {
		t.Error("alert rules still name fbs_ metrics")
	}
}

// Every runbook link of an alert lands on a heading of the runbook.
func TestAlertRunbookAnchors(t *testing.T) {
	b, err := os.ReadFile(alertsFile)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := os.ReadFile(runbook)
	if err != nil {
		t.Fatal(err)
	}
	anchors := map[string]bool{}
	for _, line := range strings.Split(string(rb), "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			a := strings.ToLower(strings.TrimSpace(h))
			a = regexp.MustCompile(`[^a-z0-9 -]`).ReplaceAllString(a, "")
			anchors[strings.ReplaceAll(a, " ", "-")] = true
		}
	}
	links := regexp.MustCompile(`docs/runbooks/analysis\.md#([a-z0-9-]+)`).FindAllStringSubmatch(string(b), -1)
	if len(links) == 0 {
		t.Fatal("no runbook links in the alert rules")
	}
	for _, l := range links {
		if !anchors[l[1]] {
			t.Errorf("runbook link #%s has no heading in %s", l[1], runbook)
		}
	}
}
