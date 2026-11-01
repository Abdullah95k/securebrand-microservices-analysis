package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	analysisv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/analysis/v1"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/bots"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/taxonomy"
	"github.com/abdullah95k/securebrand-microservices/internal/topics"
)

// write stores one current result for an ingested item.
func write(t *testing.T, s *Store, it *core.Item, r core.Result) {
	t.Helper()
	ctx := context.Background()
	req := uuid.New()
	if err := s.StartRequest(ctx, RequestRow{ID: req, Route: core.RouteComment, Lane: core.LaneBatch, Provider: "fake", Model: "m",
		ContentIDs: []string{it.ContentID}, InputHashes: [][]byte{it.InputHash()}}); err != nil {
		t.Fatal(err)
	}
	if r.Sentiment == "" {
		r.Sentiment = "neutral"
	}
	ws, err := s.WriteResults(ctx, []ItemResult{{Item: *it, Route: core.RouteComment, InputHash: it.InputHash(), Result: r,
		Prov: core.Provenance{Provider: "fake", Model: "m", PromptVersion: "p3", TaxonomyVersion: "tax-v1", SchemaVersion: "s3",
			Lane: core.LaneBatch, AnalyzedAt: time.Now()}}}, RequestOutcome{ID: req})
	if err != nil || ws.Written != 1 {
		t.Fatalf("write %s: %+v %v", it.ContentID, ws, err)
	}
}

func outboxOf[M proto.Message](t *testing.T, s *Store, topic string, newMsg func() M) []M {
	t.Helper()
	rows, err := s.DB.Query(context.Background(), `SELECT payload FROM analysis.outbox WHERE topic = $1 ORDER BY id`, topic)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []M
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		m := newMsg()
		if err := proto.Unmarshal(b, m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestTopicListResolveAndCurate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	file, err := taxonomy.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SeedTopicsIfEmpty(ctx, file); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	if ok, _ := s.SeedTopicsIfEmpty(ctx, file); ok {
		t.Fatal("seeded twice")
	}
	tax, err := s.LoadTaxonomy(ctx)
	if err != nil || tax.Version != file.Version || tax.Len() != file.Len() {
		t.Fatalf("loaded %v %v", tax, err)
	}
	entityOf := func(n string) string {
		if strings.EqualFold(n, "Asiacell") || n == "آسياسيل" {
			return "asiacell"
		}
		return ""
	}
	ids, err := s.ResolveTopics(ctx, [][]core.TopicProposal{
		{{EN: "Water shortage", AR: "شح المياه", Parent: "services"}, {EN: "water shortages", Parent: "services"}},
		{{EN: "shortage of water", Parent: "economy"}},                  // same words: the same candidate
		{{EN: "Electricity supply", AR: "", Parent: "services"}},        // a listed topic's name
		{{EN: "Asiacell", AR: "آسياسيل", Parent: "telecom"}},            // an entity
		{{EN: "Power cuts", AR: "انقطاع الكهرباء", Parent: "services"}}, // a new candidate (a synonym)
		nil,
	}, entityOf)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"services.water_shortage"}, {"services.water_shortage"}, {"services.electricity"}, {"@asiacell"},
		{"services.power_cuts"}, nil}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("resolved %v, want %v", ids, want)
	}
	if n := count(t, s, `SELECT seen FROM analysis.topics WHERE topic_id = 'services.water_shortage' AND status = 'candidate'`); n != 2 {
		t.Fatalf("candidate used by %d items, want 2", n)
	}
	// Items carrying the candidates.
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	c1, c2 := comment("fb:comment:1", "الماي مقطوع", at, "a1"), comment("fb:comment:2", "الكهرباء", at, "a2")
	if _, err := s.Ingest(ctx, []*core.Item{c1, c2}, plan); err != nil {
		t.Fatal(err)
	}
	write(t, s, c1, core.Result{Topics: []string{"services.water_shortage", "@asiacell"}})
	write(t, s, c2, core.Result{Topics: []string{"services.power_cuts", "services.electricity", "other"}})

	// Curation: approve one (renamed), merge the synonym, nothing else.
	v, err := s.ApplyTopicDecisions(ctx, []TopicDecision{
		{ID: "services.power_cuts", Action: "merge", Into: "services.electricity"},
		{ID: "services.water_shortage", Action: "approve", EN: "Water shortages", AR: "شح المياه"},
	}, "test")
	if err != nil || v != file.Version+".1" {
		t.Fatalf("decisions: %q %v", v, err)
	}
	tax, _ = s.LoadTaxonomy(ctx)
	if !tax.Valid("services.water_shortage") || tax.Valid("services.power_cuts") || tax.Version != v {
		t.Fatalf("taxonomy %s: water %v power %v", tax.Version, tax.Valid("services.water_shortage"), tax.Valid("services.power_cuts"))
	}
	// The merged id is replaced in stored analyses, without a duplicate.
	var topicsOf []string
	if err := s.DB.QueryRow(ctx, `SELECT topics FROM analysis.analyses WHERE content_id = 'fb:comment:2' AND is_current`).Scan(&topicsOf); err != nil {
		t.Fatal(err)
	}
	if strings.Join(topicsOf, ",") != "services.electricity,other" {
		t.Fatalf("re-pointed topics %v", topicsOf)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.rollup_dirty`); n == 0 {
		t.Fatal("re-pointed days not rebuilt")
	}
	// The synonym's names now resolve to the topic it was merged into.
	ids, _ = s.ResolveTopics(ctx, [][]core.TopicProposal{{{EN: "power cuts", Parent: "services"}}}, nil)
	if fmt.Sprint(ids) != "[[services.electricity]]" {
		t.Fatalf("after merge %v", ids)
	}
	ev := outboxOf(t, s, topics.AnalysisTopic, func() *analysisv1.TopicUpdate { return &analysisv1.TopicUpdate{} })
	if len(ev) != 2 || ev[0].Status != TopicActive || ev[0].NameEn != "Water shortages" || ev[1].Status != TopicMerged ||
		ev[1].MergedInto != "services.electricity" || ev[1].TaxonomyVersion != v {
		t.Fatalf("topic events %v", ev)
	}

	// Reject: the id leaves stored analyses, and the name stays dropped.
	ids, _ = s.ResolveTopics(ctx, [][]core.TopicProposal{{{EN: "Celebrity gossip", Parent: "entertainment"}}}, nil)
	gossip := ids[0][0]
	if _, err := s.DB.Exec(ctx, `UPDATE analysis.analyses SET topics = topics || $1::text WHERE content_id = 'fb:comment:1'`, gossip); err != nil {
		t.Fatal(err)
	}
	if v, err = s.ApplyTopicDecisions(ctx, []TopicDecision{{ID: gossip, Action: "reject"}}, "test"); err != nil || v != file.Version+".2" {
		t.Fatalf("reject: %q %v", v, err)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.analyses WHERE topics @> ARRAY[$1]`, gossip); n != 0 {
		t.Fatal("rejected topic left in analyses")
	}
	if ids, _ = s.ResolveTopics(ctx, [][]core.TopicProposal{{{EN: "celebrity gossip", Parent: "other"}}}, nil); ids[0] != nil {
		t.Fatalf("rejected topic came back: %v", ids)
	}

	// Approving under another parent gives a new id and merges the old.
	ids, _ = s.ResolveTopics(ctx, [][]core.TopicProposal{{{EN: "Fuel queues", Parent: "other"}}}, nil)
	if _, err := s.ApplyTopicDecisions(ctx, []TopicDecision{{ID: ids[0][0], Action: "approve", Parent: "economy"}}, "test"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.topics WHERE topic_id = 'economy.fuel_queues' AND status = 'active'`); n != 1 {
		t.Fatal("moved topic not created")
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.topics WHERE topic_id = 'other.fuel_queues' AND merged_into = 'economy.fuel_queues'`); n != 1 {
		t.Fatal("old id not merged into the moved topic")
	}
	// Errors roll everything back.
	if _, err := s.ApplyTopicDecisions(ctx, []TopicDecision{{ID: "services.water_shortage", Action: "merge", Into: "no.such"}}, "x"); err == nil {
		t.Fatal("merge into an unknown topic accepted")
	}
	if _, err := s.ApplyTopicDecisions(ctx, []TopicDecision{{ID: "services.water_shortage", Action: "explode"}}, "x"); err == nil {
		t.Fatal("unknown action accepted")
	}
	// A re-import of the file keeps added topics and records a version.
	if v, err = s.ImportTaxonomy(ctx, file, "owner"); err != nil || !strings.HasPrefix(v, file.Version+".") {
		t.Fatalf("re-import: %q %v", v, err)
	}
	if tax, _ = s.LoadTaxonomy(ctx); !tax.Valid("services.water_shortage") || !tax.Valid("economy.fuel_queues") {
		t.Fatal("re-import dropped added topics")
	}
}

func TestAdsLegalAndBotScoring(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	var items []*core.Item
	// A bot: 40 copies of one long text across 5 threads in one hour.
	bot := "bot-1"
	spamText := "اربح جوال ايفون مجانا ادخل على الرابط الان وسجل رقمك"
	for i := range 40 {
		it := comment(fmt.Sprintf("fb:comment:b%d", i), spamText, base.Add(time.Duration(i)*time.Minute), bot)
		it.ThreadID = fmt.Sprintf("fb:post:%d", i%5)
		it.ParentID = it.ThreadID
		items = append(items, it)
	}
	// A person: 12 different comments over 6 days.
	for i := range 12 {
		items = append(items, comment(fmt.Sprintf("fb:comment:h%d", i), fmt.Sprintf("رأيي في الموضوع رقم %d مختلف", i),
			base.Add(-time.Duration(i)*12*time.Hour), "human-1"))
	}
	// A contest fan: the same entry text in 12 contests, one a day. Contest
	// entries never count as repetition.
	fan := len(items)
	for i := range 12 {
		it := comment(fmt.Sprintf("fb:comment:g%d", i), "تم المشاركة والمتابعة مع الاصدقاء", base.Add(-time.Duration(i)*24*time.Hour), "fan-1")
		it.ThreadID, it.ParentID = fmt.Sprintf("fb:post:g%d", i), fmt.Sprintf("fb:post:g%d", i)
		items = append(items, it)
	}
	if _, err := s.Ingest(ctx, items, plan); err != nil {
		t.Fatal(err)
	}
	for i := fan; i < fan+12; i++ {
		write(t, s, items[i], core.Result{Intent: "giveaway_entry"})
	}
	// Some analyses: spam and a scam for the bot, an ad and a breach to count.
	write(t, s, items[0], core.Result{Spam: true, Legal: []string{"fraud_scam"}, AdType: "hidden"})
	for i := 1; i < 20; i++ {
		write(t, s, items[i], core.Result{Spam: true})
	}
	write(t, s, items[40], core.Result{Sentiment: "positive"})
	if n := count(t, s, `SELECT count(*) FROM analysis.analyses WHERE ad_type = 'hidden' AND legal = ARRAY['fraud_scam'] AND author_key IS NOT NULL`); n != 1 {
		t.Fatal("ad, legal or author not stored")
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:h0' AND ad_type IS NULL AND legal IS NULL`); n != 1 {
		t.Fatal("no ad and no breach must be NULL")
	}

	th := bots.Thresholds{Suspected: 45, Likely: 70}
	scorer := func(a core.AuthorSignals) (int, string, []string) { return bots.Score(a, th) }
	run, err := s.ScoreAuthors(ctx, 30*24*time.Hour, 10, 5, scorer)
	if err != nil {
		t.Fatal(err)
	}
	if run.Scored != 3 || run.Flagged != 1 || run.Changed != 1 {
		t.Fatalf("run %+v", run)
	}
	var level string
	var reasons []string
	if err := s.DB.QueryRow(ctx, `SELECT coalesce(bot, ''), reasons FROM analysis.authors WHERE author_key = $1`, []byte(bot)).Scan(&level, &reasons); err != nil {
		t.Fatal(err)
	}
	if level != bots.Likely || !strings.Contains(strings.Join(reasons, ","), "burst") || !strings.Contains(strings.Join(reasons, ","), "repetition") {
		t.Fatalf("bot level %q reasons %v", level, reasons)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.authors WHERE author_key = $1 AND bot IS NULL`, []byte("human-1")); n != 1 {
		t.Fatal("a person was flagged")
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.authors WHERE author_key = $1 AND repeated = 0`, []byte("fan-1")); n != 1 {
		t.Fatal("contest entries counted as repetition")
	}
	flags := outboxOf(t, s, topics.AnalysisAuthor, func() *analysisv1.AuthorFlag { return &analysisv1.AuthorFlag{} })
	if len(flags) != 1 || flags[0].Bot != bots.Likely || string(flags[0].AuthorKey) != bot {
		t.Fatalf("author events %v", flags)
	}
	// A second run changes nothing and publishes nothing.
	if run, _ = s.ScoreAuthors(ctx, 30*24*time.Hour, 10, 5, scorer); run.Changed != 0 {
		t.Fatalf("second run %+v", run)
	}
	// Rollups count ads, breaches and bot items.
	if _, err := s.RecomputeDirtyDays(ctx, 50, 0); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT coalesce(sum(bot_items), 0) FROM analysis.rollup_source_daily`); n != 20 {
		t.Fatalf("bot items %d, want 20", n)
	}
	if n := count(t, s, `SELECT coalesce(sum(ads + legal), 0) FROM analysis.rollup_source_daily`); n != 2 {
		t.Fatal("ads and breaches not counted")
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.rollup_flag_daily WHERE flag IN ('ad_hidden', 'fraud_scam') AND items = 1 AND bot_items = 1`); n != 2 {
		t.Fatal("flag rollup")
	}
	// A new result of a flagged author carries the level.
	write(t, s, items[25], core.Result{})
	res := outboxOf(t, s, topics.AnalysisResult, func() *analysisv1.ItemAnalysis { return &analysisv1.ItemAnalysis{} })
	if last := res[len(res)-1]; last.AuthorBot != bots.Likely || last.ContentId != items[25].ContentID {
		t.Fatalf("result author_bot %q", last.AuthorBot)
	}
	// The owner's decision wins over the score, both ways.
	if err := s.OverrideAuthor(ctx, "facebook", []byte(bot), "human"); err != nil {
		t.Fatal(err)
	}
	if err := s.OverrideAuthor(ctx, "facebook", []byte("human-1"), "bot"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.authors WHERE (author_key = $1 AND bot IS NULL) OR (author_key = $2 AND bot = 'confirmed')`,
		[]byte(bot), []byte("human-1")); n != 2 {
		t.Fatal("overrides not applied")
	}
	if err := s.OverrideAuthor(ctx, "facebook", []byte(bot), "maybe"); err == nil {
		t.Fatal("unknown override accepted")
	}
	if rows, err := s.Authors(ctx, true, 10); err != nil || len(rows) != 1 || rows[0].Bot != "confirmed" {
		t.Fatalf("flagged authors %+v %v", rows, err)
	}
	// Erasing the author removes the profile.
	if _, err := s.Erase(ctx, core.Erasure{RequestID: "e1", Subject: "author", AuthorKey: []byte(bot)}, "analysis"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.authors WHERE author_key = $1`, []byte(bot)); n != 0 {
		t.Fatal("author profile survived erasure")
	}
}
