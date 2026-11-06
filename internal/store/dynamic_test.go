package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	analysisv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/analysis/v1"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/bots"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/taxonomy"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
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
	// A bot: 40 copies of one long text across 5 threads in one hour. Its
	// key is a contract account ID; the others are fbs's author keys.
	bot := "fb:account:9001"
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
	// A network: five accounts posting the same campaign text twice each on
	// one day, in different threads. Each has only two posts.
	campaign := "الحكومة الحالية افضل حكومة مرت على العراق والكل يشهد بانجازاتها"
	for a := range 5 {
		for k := range 2 {
			it := comment(fmt.Sprintf("fb:comment:n%d-%d", a, k), campaign, base.Add(time.Duration(a*10+k)*time.Minute), fmt.Sprintf("net-%d", a))
			it.ThreadID, it.ParentID = fmt.Sprintf("fb:post:n%d", k), fmt.Sprintf("fb:post:n%d", k)
			items = append(items, it)
		}
	}
	// A coincidence: the same long quote by five people on five different
	// days is not coordination.
	quote := "قال الشاعر العراقي الكبير ان الوطن ليس فندقا نغادره حين تسوء الخدمة"
	for a := range 5 {
		for k := range 2 {
			it := comment(fmt.Sprintf("fb:comment:q%d-%d", a, k), quote+fmt.Sprint(k), base.Add(-time.Duration(a)*24*time.Hour+time.Duration(k)*time.Minute), fmt.Sprintf("quote-%d", a))
			if k == 1 {
				it.Text = quote
			}
			items = append(items, it)
		}
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
	// The bot, human-1, fan-1 (5+ items) and the five network accounts; the
	// quoting people are neither busy nor coordinated.
	if run.Scored != 8 || run.Flagged != 6 || run.Changed != 6 {
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
	if n := count(t, s, `SELECT count(*) FROM analysis.authors WHERE bot = 'suspected' AND 'network' = ANY(reasons)
		AND encode(author_key, 'escape') LIKE 'net-%'`); n != 5 {
		t.Fatalf("network accounts flagged: %d, want 5", n)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.authors WHERE encode(author_key, 'escape') LIKE 'quote-%'`); n != 0 {
		t.Fatal("people quoting a poem on different days were scored as coordinated")
	}
	// Only authors known by a contract account ID are published (the
	// contract keys AuthorFlag by author_id); fbs's author keys stay here.
	flags := outboxOf(t, s, topics.AnalysisAuthor, func() *analysisv1.AuthorFlag { return &analysisv1.AuthorFlag{} })
	if len(flags) != 1 || flags[0].AuthorId != bot || flags[0].Bot != bots.Likely || flags[0].Platform != "fb" {
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
	items[25].Version = 1 // published only for contract items
	write(t, s, items[25], core.Result{})
	res := outboxOf(t, s, topics.AnalysisResult, func() *analysisv1.ItemAnalysis { return &analysisv1.ItemAnalysis{} })
	if last := res[len(res)-1]; last.AuthorBot != bots.Likely || last.ContentId != items[25].ContentID {
		t.Fatalf("result author_bot %q", last.AuthorBot)
	}
	// The owner's decision wins over the score, both ways.
	if err := s.OverrideAuthor(ctx, "facebook", []byte(bot), "human", "", ""); err == nil {
		t.Fatal("an override without who decided was accepted")
	}
	if err := s.OverrideAuthor(ctx, "facebook", []byte(bot), "human", "Abdullah", "customer service account"); err != nil {
		t.Fatal(err)
	}
	if err := s.OverrideAuthor(ctx, "facebook", []byte("human-1"), "bot", "Abdullah", ""); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.authors WHERE (author_key = $1 AND bot IS NULL) OR (author_key = $2 AND bot = 'confirmed')`,
		[]byte(bot), []byte("human-1")); n != 2 {
		t.Fatal("overrides not applied")
	}
	if err := s.OverrideAuthor(ctx, "facebook", []byte(bot), "maybe", "Abdullah", ""); err == nil {
		t.Fatal("unknown override accepted")
	}
	// Every decision is logged, with the level before and after.
	log, err := s.AuthorOverrides(ctx, []byte(bot), 10)
	if err != nil || len(log) != 1 || log[0].Override != "human" || log[0].Before != bots.Likely || log[0].After != "" ||
		log[0].By != "Abdullah" || log[0].Note != "customer service account" {
		t.Fatalf("override log %+v %v", log, err)
	}
	if all, _ := s.AuthorOverrides(ctx, nil, 10); len(all) != 2 {
		t.Fatalf("whole log: %d rows, want 2", len(all))
	}
	rows, err := s.Authors(ctx, true, 10)
	if err != nil || len(rows) != 6 || rows[len(rows)-1].Bot != "confirmed" || string(rows[len(rows)-1].Key) != "human-1" {
		t.Fatalf("flagged authors %+v %v", rows, err)
	}
	// Erasing the author removes the profile.
	if _, err := s.Erase(ctx, core.Erasure{RequestID: "e1", Subject: "author", AuthorKey: []byte(bot)}, "analysis"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.authors WHERE author_key = $1`, []byte(bot)); n != 0 {
		t.Fatal("author profile survived erasure")
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.author_overrides WHERE author_key = $1`, []byte(bot)); n != 0 {
		t.Fatal("override log survived erasure")
	}
}

func TestSubTopicsAndFindTopics(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	file, _ := taxonomy.Load("")
	if _, err := s.SeedTopicsIfEmpty(ctx, file); err != nil {
		t.Fatal(err)
	}
	// A narrower topic under a listed topic (not an area): a sub-topic.
	ids, err := s.ResolveTopics(ctx, [][]core.TopicProposal{{{EN: "Electricity cuts", AR: "انقطاع الكهرباء", Parent: "services.electricity"}}}, nil)
	if err != nil || len(ids[0]) != 1 {
		t.Fatalf("resolve: %v %v", ids, err)
	}
	cuts := ids[0][0]
	if cuts != "services.electricity.electricity_cuts" {
		t.Fatalf("sub-topic id %s", cuts)
	}
	if _, err := s.ApplyTopicDecisions(ctx, []TopicDecision{{ID: cuts, Action: "approve"}}, "test"); err != nil {
		t.Fatal(err)
	}
	tax, err := s.LoadTaxonomy(ctx)
	if err != nil || !tax.Valid(cuts) || taxonomy.Depth(cuts) != 3 || !strings.Contains(tax.Prompt(), "    "+cuts+":") {
		t.Fatalf("three levels: %v", err)
	}
	// A sub-topic cannot get sub-topics, and a topic with sub-topics is not
	// merged away from under them.
	if _, err := s.ApplyTopicDecisions(ctx, []TopicDecision{{ID: cuts, Action: "approve", Parent: cuts}}, "x"); err == nil {
		t.Fatal("fourth level accepted")
	}
	if _, err := s.ApplyTopicDecisions(ctx, []TopicDecision{{ID: "services.electricity", Action: "merge", Into: "services.water"}}, "x"); err == nil {
		t.Fatal("parent merged away from its sub-topics")
	}
	find := func(q string) map[string]string {
		t.Helper()
		hits, err := s.FindTopics(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, h := range hits {
			out[h.ID] = h.Under
		}
		return out
	}
	// English with another word form, Arabic with or without the article:
	// the sub-topic itself.
	for _, q := range []string{"electrical cuts", "انقطاع الكهرباء", "power cuts electricity"} {
		if got := find(q); len(got) != 1 || got[cuts] != "" {
			t.Errorf("find(%q) = %v, want only %s", q, got, cuts)
		}
	}
	// An ambiguous word ("قطع" is also spare parts) returns every reading,
	// the right one included.
	if got := find("قطع كهرباء"); len(got) < 2 {
		t.Errorf("find(قطع كهرباء) = %v", got)
	} else if _, ok := got[cuts]; !ok {
		t.Errorf("find(قطع كهرباء) misses %s: %v", cuts, got)
	}
	// The broader topic brings its sub-topics.
	if got := find("electricity supply"); len(got) != 2 || got["services.electricity"] != "" || got[cuts] != "services.electricity" {
		t.Fatalf("find(electricity supply) = %v", got)
	}
	if got := find("zzzz qqqq"); len(got) != 0 {
		t.Fatalf("nonsense found %v", got)
	}
	// Items carrying any of the ids are counted.
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	c := comment("fb:comment:1", "الوطنية طافية", at, "a1")
	if _, err := s.Ingest(ctx, []*core.Item{c}, plan); err != nil {
		t.Fatal(err)
	}
	write(t, s, c, core.Result{Topics: []string{cuts}})
	if n, err := s.TopicItems(ctx, []string{"services.electricity", cuts}); err != nil || n != 1 {
		t.Fatalf("topic items %d %v", n, err)
	}
}
