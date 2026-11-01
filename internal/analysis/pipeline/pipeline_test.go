package pipeline_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/curate"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/entities"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/pipeline"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider/fake"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider/registry"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/store"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/taxonomy"
	"github.com/abdullah95k/securebrand-microservices/internal/config"
	"github.com/abdullah95k/securebrand-microservices/internal/testenv"
)

type env struct {
	s        *store.Store
	p        *pipeline.Pipeline
	primary  *fake.Provider
	fallback *fake.Provider
}

// setup builds a pipeline over a fresh database, with fake primary and
// fallback providers on every route.
func setup(t *testing.T, edit func(*config.Analysis)) *env {
	t.Helper()
	db := testenv.Postgres(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	full, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := full.Analysis
	for name, r := range cfg.Routes {
		r.Provider, r.FallbackProvider, r.FallbackModel = "primary", "", ""
		cfg.Routes[name] = r
	}
	cfg.Realtime.Wait = 0
	if edit != nil {
		edit(&cfg)
	}
	loc, _ := time.LoadLocation(cfg.Timezone)
	st := store.New(db, loc)
	tax, err := taxonomy.Load("")
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := registry.New(nil)
	e := &env{s: st, primary: fake.New("primary"), fallback: fake.New("fallback")}
	reg.Add("primary", e.primary, true, 0)
	reg.Add("fallback", e.fallback, true, 0)
	e.p = pipeline.New(st, reg, tax, cfg, nil)
	seed, err := entities.LoadSeed("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SeedIfEmpty(ctx, seed); err != nil {
		t.Fatal(err)
	}
	r, err := st.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e.p.SetEntities(r)
	return e
}

func (e *env) ingest(t *testing.T, items ...*core.Item) {
	t.Helper()
	if _, err := e.s.Ingest(context.Background(), items, e.p.Plan); err != nil {
		t.Fatal(err)
	}
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.s.DB.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

var at = time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)

func post() *core.Item {
	return &core.Item{ContentID: "fb:post:1", Platform: "facebook", Kind: core.KindPost, SourceID: "fb:page:1", SourceName: "Asiacell Iraq",
		CreatedAt: at.Add(-time.Hour), ObservedAt: at, Text: "عرض جديد على باقات الانترنت", Status: core.StatusVisible}
}

func comment(n int, text string) *core.Item {
	id := "fb:comment:" + string(rune('0'+n))
	return &core.Item{ContentID: id, Platform: "facebook", Kind: core.KindComment, SourceID: "fb:page:1", ThreadID: "fb:post:1",
		ParentID: "fb:post:1", AuthorKey: []byte{byte(n)}, CreatedAt: at, ObservedAt: at, Text: text, Status: core.StatusVisible}
}

func TestBatchLaneEndToEnd(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	e.ingest(t, post(), comment(1, "شبكة اسياسيل خايسة"), comment(2, "شكرا الكم"), comment(3, "@someone"))
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_batches WHERE status = 'in_progress'`); n != 1 {
		t.Fatalf("batches %d", n)
	}
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE is_current`); n != 4 {
		t.Fatalf("analyses %d, want 4 (post + 3 comments)", n)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:1' AND sentiment = 'negative' AND lane = 'batch'`); n != 1 {
		t.Fatal("comment 1 not negative on the batch lane")
	}
	// The alias matcher's hint makes the model's entity link to the registry.
	if n := e.count(t, `SELECT count(*) FROM analysis.entity_mentions WHERE content_id = 'fb:comment:1' AND entity_id = 'asiacell'`); n != 1 {
		t.Fatal("asiacell not linked")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_requests WHERE status = 'done'`); n != 2 {
		t.Fatalf("requests done %d (one per route)", n)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue`); n != 0 {
		t.Fatal("queue not empty")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.outbox WHERE topic = 'analysis.result.v1'`); n != 4 {
		t.Fatal("outbox")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_batches WHERE status = 'ended'`); n != 1 {
		t.Fatal("batch not ended")
	}
	// Polling again finds nothing to do.
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatal(err)
	}
}

// Identical comments of a thread are sent once and all get the answer;
// a different parent, a different text or another lane's claim are not
// merged.
func TestDuplicatesAnalysedOnce(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	var users []string
	e.primary.Fail = func(req provider.Request) error {
		users = append(users, req.User)
		return nil
	}
	reply := comment(5, "تم")
	reply.Kind, reply.ParentID = core.KindReply, "fb:comment:1"
	e.ingest(t, comment(1, "تم"), comment(2, "تم"), comment(3, " تم "), comment(4, "سيء"), reply, comment(6, "سيء"))
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE is_current`); n != 6 {
		t.Fatalf("analyses %d, want 6", n)
	}
	if len(users) != 1 {
		t.Fatalf("requests %d, want 1", len(users))
	}
	// Prompted: comment 1 ("تم"), comment 4 ("سيء"), the reply (another
	// parent).
	if n := strings.Count(users[0], `"text":`); n != 3 {
		t.Fatalf("items shown to the model %d, want 3:\n%s", n, users[0])
	}
	if n := e.count(t, `SELECT count(DISTINCT request_id) FROM analysis.analyses WHERE content_id IN ('fb:comment:1','fb:comment:2','fb:comment:3')`); n != 1 {
		t.Fatal("followers must carry the leader's request")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE content_id IN ('fb:comment:4','fb:comment:6') AND sentiment = 'negative'`); n != 2 {
		t.Fatal("follower without its leader's answer")
	}
	// Script and keywords come from the text, not the model.
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:6' AND script = 'arabic' AND keywords = ARRAY['سيء']`); n != 1 {
		t.Fatal("script and keywords not computed from the text")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue`); n != 0 {
		t.Fatal("items left queued")
	}
	var cost2, cost4 float64
	if err := e.s.DB.QueryRow(ctx, `SELECT (SELECT cost_usd FROM analysis.analyses WHERE content_id = 'fb:comment:2'),
		(SELECT cost_usd FROM analysis.analyses WHERE content_id = 'fb:comment:4')`).Scan(&cost2, &cost4); err != nil {
		t.Fatal(err)
	}
	if cost2 != cost4 {
		t.Fatalf("the request's cost is split over every answered item: %v vs %v", cost2, cost4)
	}
}

// Short posts use the comment route: identical ones of two pages are not
// merged, and each is sent with its own page as context.
func TestShortPostsKeepTheirPage(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	var users []string
	e.primary.Fail = func(req provider.Request) error {
		users = append(users, req.User)
		return nil
	}
	a, b := post(), post()
	a.Text, b.Text = "شبكتنا الأقوى", "شبكتنا الأقوى"
	b.ContentID, b.SourceID, b.SourceName = "fb:post:2", "fb:page:2", "Zain Iraq"
	e.ingest(t, a, b)
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(DISTINCT request_id) FROM analysis.analyses WHERE is_current`); n != 2 {
		t.Fatalf("requests %d, want one per page", n)
	}
	joined := strings.Join(users, "\n")
	if !strings.Contains(joined, "page: Asiacell Iraq") || !strings.Contains(joined, "page: Zain Iraq") {
		t.Fatalf("posts sent without their page:\n%s", joined)
	}
}

// A missing topic becomes a candidate on the item, brands become "@"
// topics, ads and breaches are stored, and once used often enough the
// curation approves the candidate into a new taxonomy version.
func TestNewTopicsEntityTopicsAndFlags(t *testing.T) {
	e := setup(t, func(c *config.Analysis) {
		c.Topics.ApproveMinItems = 2
		c.Topics.Curation = config.Route{Provider: "primary", Model: "curation-model", MaxTokens: 2000}
	})
	ctx := context.Background()
	tax, _ := taxonomy.Load("")
	if _, err := e.s.SeedTopicsIfEmpty(ctx, tax); err != nil {
		t.Fatal(err)
	}
	e.ingest(t, comment(1, "شح المياه بالبصرة"), comment(2, "شح المياه ثاني يوم"), comment(3, "شبكة اسياسيل خايسة"),
		comment(4, "إعلان: اربح ايفون، نصب"))
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE topics @> ARRAY['services.water_shortage'] AND is_current`); n != 2 {
		t.Fatalf("candidate topic on %d items, want 2", n)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:3' AND topics @> ARRAY['@asiacell']`); n != 1 {
		t.Fatal("entity topic missing")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:4' AND ad_type = 'paid' AND legal = ARRAY['fraud_scam']`); n != 1 {
		t.Fatal("ad and breach not stored")
	}
	cur := &curate.Curator{Store: e.s, Providers: e.p.Providers, Pricing: e.p.Pricing, Cfg: e.p.Cfg.Topics, Taxonomy: e.p.Taxonomy}
	v, err := cur.Run(ctx)
	if err != nil || v == "" {
		t.Fatalf("curation: %q %v", v, err)
	}
	next, err := e.s.LoadTaxonomy(ctx)
	if err != nil || !next.Valid("services.water_shortage") {
		t.Fatalf("approved topic not in the list: %v", err)
	}
	e.p.SetTaxonomy(next)
	if e.p.Taxonomy().Version != v {
		t.Fatal("pipeline kept the old list")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_requests WHERE route = 'curation'`); n != 1 {
		t.Fatal("curation call not logged for the budget")
	}
	// Nothing left to curate: no new version.
	if v2, err := cur.Run(ctx); err != nil || v2 != "" {
		t.Fatalf("second curation: %q %v", v2, err)
	}
	// Without a curation provider, candidates are approved on the count.
	if _, err := e.s.ResolveTopics(ctx, [][]core.TopicProposal{{{EN: "Fuel queues", Parent: "economy"}}, {{EN: "fuel queue", Parent: "economy"}}}, nil); err != nil {
		t.Fatal(err)
	}
	// Use is counted from stored analyses: two items carry the candidate.
	if _, err := e.s.DB.Exec(ctx, `UPDATE analysis.analyses SET topics = topics || 'economy.fuel_queues'::text
		WHERE content_id IN ('fb:comment:1', 'fb:comment:2')`); err != nil {
		t.Fatal(err)
	}
	plain := *cur
	plain.Cfg.Curation = config.Route{}
	if v3, err := plain.Run(ctx); err != nil || v3 == "" {
		t.Fatalf("count-only curation: %q %v", v3, err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.topics WHERE topic_id = 'economy.fuel_queues' AND status = 'active'`); n != 1 {
		t.Fatal("candidate not approved on the count")
	}
}

// "رئيس الوزراء" alone is any country's prime minister: a hint marked
// ambiguous, never urgent, never linked by name; the qualified form links.
func TestAmbiguousTitles(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	if _, err := e.s.DB.Exec(ctx, `INSERT INTO analysis.clients (client_id, name) VALUES ('c1', 'Client')`); err != nil {
		t.Fatal(err)
	}
	if err := e.s.TrackEntity(ctx, "c1", "pos-iraq-pm", "watch"); err != nil {
		t.Fatal(err)
	}
	r, err := e.s.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e.p.SetEntities(r)
	foreign, iraqi := comment(1, "رئيس الوزراء البريطاني زار بغداد اليوم"), comment(2, "رئيس الوزراء العراقي وعد بحل ازمة الكهرباء")
	if pl := e.p.Plan(foreign); pl.Lane != core.LaneBatch {
		t.Fatal("a generic title made the item urgent")
	}
	if pl := e.p.Plan(iraqi); pl.Lane != core.LaneRealtime {
		t.Fatal("the qualified title of a tracked position is not urgent")
	}
	var users []string
	e.primary.Fail = func(req provider.Request) error {
		users = append(users, req.User)
		return nil
	}
	e.ingest(t, foreign, iraqi)
	if _, err := e.p.RealtimeOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(users, "\n"), `"ambiguous":true`) {
		t.Fatal("the generic title was not offered as an ambiguous hint")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.entity_mentions WHERE content_id = 'fb:comment:1' AND entity_id = 'pos-iraq-pm'`); n != 0 {
		t.Fatal("the British prime minister was linked to the Iraqi one")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:2' AND topics @> ARRAY['@pos-iraq-pm']`); n != 1 {
		t.Fatal("the Iraqi prime minister was not linked")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.entity_candidates`); n != 0 {
		t.Fatal("a generic title went to the review queue")
	}
}

// Names found often enough are decided by the curation model: a new
// entity approved, a spelling merged into a known one, a private name
// rejected; who decided is recorded.
func TestEntityCuration(t *testing.T) {
	e := setup(t, func(c *config.Analysis) {
		c.Topics.Curation = config.Route{Provider: "primary", Model: "curation-model", MaxTokens: 2000}
	})
	ctx := context.Background()
	var cands []store.Candidate
	for i := range 5 {
		cands = append(cands, store.Candidate{Surface: "Toyota Iraq", Type: "brand", ContentID: fmt.Sprintf("x%d", i)},
			store.Candidate{Surface: "اسيا سيل الجديدة", Type: "brand", ContentID: fmt.Sprintf("y%d", i)},
			store.Candidate{Surface: "محمد علي", Type: "person_public", ContentID: fmt.Sprintf("z%d", i)})
	}
	cands = append(cands, store.Candidate{Surface: "Rare Brand", Type: "brand", ContentID: "r1"}) // seen once: waits
	if err := e.s.AddCandidates(ctx, cands); err != nil {
		t.Fatal(err)
	}
	cur := &curate.EntityCurator{Store: e.s, Providers: e.p.Providers, Pricing: e.p.Pricing, Route: e.p.Cfg.Topics.Curation, MinSeen: 5,
		Registry: e.p.Entities}
	n, err := cur.Run(ctx)
	if err != nil || n != 3 {
		t.Fatalf("decided %d %v", n, err)
	}
	if c := e.count(t, `SELECT count(*) FROM analysis.entities WHERE entity_id = 'toyota-iraq' AND type = 'brand'`); c != 1 {
		t.Fatal("new brand not approved")
	}
	if c := e.count(t, `SELECT count(*) FROM analysis.entity_aliases WHERE entity_id = 'asiacell' AND alias = 'اسيا سيل الجديدة'`); c != 1 {
		t.Fatal("spelling not merged into the known brand")
	}
	if c := e.count(t, `SELECT count(*) FROM analysis.entity_candidates WHERE surface = 'محمد علي' AND status = 'rejected'
		AND resolved_by = 'curation-model' AND resolved_at IS NOT NULL`); c != 1 {
		t.Fatal("private-looking name not rejected, or the decider not recorded")
	}
	if c := e.count(t, `SELECT count(*) FROM analysis.entity_candidates WHERE surface = 'Rare Brand' AND status = 'pending'`); c != 1 {
		t.Fatal("a name seen once was decided")
	}
	if c := e.count(t, `SELECT count(*) FROM analysis.provider_requests WHERE route = 'curation'`); c != 1 {
		t.Fatal("curation call not logged for the budget")
	}
}

// A leader the model leaves out fails with its followers; they are retried
// together and analysed.
func TestDuplicatesFailWithTheirLeader(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	e.primary.Drop = func(i int) bool { return i == 0 }
	e.ingest(t, comment(1, "تم"), comment(2, "تم"), comment(3, "حلو"))
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses`); n != 1 {
		t.Fatalf("analyses %d, want 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue WHERE attempts = 1 AND last_error LIKE '2 item(s) missing%'`); n != 2 {
		t.Fatal("leader and follower must both be released for a retry")
	}
	e.primary.Drop = nil
	if _, err := e.s.DB.Exec(ctx, `UPDATE analysis.analysis_queue SET not_before = now() - interval '1s'`); err != nil {
		t.Fatal(err)
	}
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses`); n != 3 {
		t.Fatalf("analyses %d after the retry, want 3", n)
	}
}

func TestUrgentItemsGoRealtimeWithContext(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	if _, err := e.s.DB.Exec(ctx, `INSERT INTO analysis.clients VALUES ('c1', 'Client', now())`); err != nil {
		t.Fatal(err)
	}
	if err := e.s.TrackEntity(ctx, "c1", "asiacell", "own"); err != nil {
		t.Fatal(err)
	}
	r, _ := e.s.LoadRegistry(ctx)
	e.p.SetEntities(r)
	e.ingest(t, post(), comment(1, "آسياسيل النت واقف من الصبح"), comment(2, "صباح الخير"))
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue WHERE lane = 'realtime' AND priority = 0`); n != 1 {
		t.Fatal("tracked-entity comment not urgent")
	}
	n, err := e.p.RealtimeOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("realtime processed %d %v", n, err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:1' AND lane = 'realtime'`); n != 1 {
		t.Fatal("urgent comment not analysed in real time")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue WHERE lane = 'batch'`); n != 2 {
		t.Fatal("the other items should wait for the batch lane")
	}
}

func TestMissingItemsAreRetriedAlone(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	e.primary.Drop = func(i int) bool { return i == 1 }
	e.ingest(t, comment(1, "حلو"), comment(2, "سيء"), comment(3, "عادي"))
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses`); n != 2 {
		t.Fatalf("analyses %d, want 2", n)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue WHERE attempts = 1 AND lease_until IS NULL AND last_error LIKE '%missing%'`); n != 1 {
		t.Fatal("missing item not released")
	}
	e.primary.Drop = nil
	if _, err := e.s.DB.Exec(ctx, `UPDATE analysis.analysis_queue SET not_before = now() - interval '1s'`); err != nil {
		t.Fatal(err)
	}
	// A new comment of the same thread arrives meanwhile: the retried item
	// must still go alone, not in a group with it.
	e.ingest(t, comment(4, "شكرا"))
	calls := e.primary.Calls()
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses`); n != 4 {
		t.Fatal("retried item not analysed")
	}
	if e.primary.Calls()-calls != 2 {
		t.Fatalf("want 2 requests (the retried item alone, the new one), got %d", e.primary.Calls()-calls)
	}
}

func TestRefusalIsolatedThenFallback(t *testing.T) {
	e := setup(t, func(c *config.Analysis) {
		r := c.Routes[core.RouteComment]
		r.Lane, r.FallbackProvider, r.FallbackModel = core.LaneRealtime, "fallback", "fallback-model"
		c.Routes[core.RouteComment] = r
	})
	ctx := context.Background()
	// The primary refuses anything containing the word "تهديد".
	e.primary.Refuse = func(req provider.Request) bool { return contains(req.User, "تهديد") }
	e.ingest(t, comment(1, "عادي"), comment(2, "تهديد"))
	if _, err := e.p.RealtimeOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses`); n != 0 {
		t.Fatal("a refused group stored results")
	}
	// Retried one by one: the clean item on the primary, the refused one on
	// the fallback.
	if _, err := e.s.DB.Exec(ctx, `UPDATE analysis.analysis_queue SET not_before = now() - interval '1s'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.p.RealtimeOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:1' AND NOT fallback AND provider = 'primary'`); n != 1 {
		t.Fatal("clean item not analysed by the primary")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:2' AND fallback AND model = 'fallback-model'`); n != 1 {
		t.Fatal("refused item not analysed by the fallback")
	}
	if e.fallback.Calls() != 1 {
		t.Fatalf("fallback calls %d", e.fallback.Calls())
	}
}

func TestRefusedEverywhereIsStoredAsRefused(t *testing.T) {
	e := setup(t, func(c *config.Analysis) {
		r := c.Routes[core.RouteComment]
		r.Lane = core.LaneRealtime
		c.Routes[core.RouteComment] = r
	})
	e.primary.Refuse = func(provider.Request) bool { return true }
	// Two identical comments: one input, refused; both are stored as refused.
	e.ingest(t, comment(1, "نص"), comment(2, "نص"))
	if _, err := e.p.RealtimeOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE refused AND sentiment = 'neutral' AND keywords IS NULL`); n != 2 {
		t.Fatal("refused items not stored as refused")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue`); n != 0 {
		t.Fatal("refused item still queued")
	}
}

func TestProviderOutageFallsBackInRealtime(t *testing.T) {
	e := setup(t, func(c *config.Analysis) {
		r := c.Routes[core.RouteComment]
		r.Lane, r.FallbackProvider, r.FallbackModel = core.LaneRealtime, "fallback", "fallback-model"
		c.Routes[core.RouteComment] = r
	})
	e.primary.Fail = func(provider.Request) error { return &provider.Error{Status: 529, Retryable: true, Err: errOverloaded} }
	e.ingest(t, comment(1, "حلو"))
	if _, err := e.p.RealtimeOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE fallback`); n != 1 {
		t.Fatal("not served by the fallback")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_requests WHERE fallback AND provider = 'fallback' AND status = 'done'`); n != 1 {
		t.Fatal("request does not record the fallback")
	}
}

func TestBudgetHoldsBatchWork(t *testing.T) {
	e := setup(t, func(c *config.Analysis) {
		c.Budget.DailyUSD = 0.0000001
		c.Pricing["claude-haiku-5-5"] = config.ModelPrice{Input: 1, Output: 1}
	})
	ctx := context.Background()
	e.ingest(t, comment(1, "حلو"))
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if e.primary.Calls() != 0 {
		t.Fatal("budget did not hold the request")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue WHERE attempts = 0 AND not_before > now()`); n != 1 {
		t.Fatal("held item not released for later without an attempt")
	}
}

func TestEscalationAndAudit(t *testing.T) {
	e := setup(t, func(c *config.Analysis) {
		esc := c.Routes[core.RouteEscalation]
		esc.MinConfidence = 0.95 // the fake answers 0.8
		esc.Model = "strong-model"
		c.Routes[core.RouteEscalation] = esc
		au := c.Routes[core.RouteAudit]
		au.SampleRate = 1
		au.Model = "audit-model"
		c.Routes[core.RouteAudit] = au
	})
	ctx := context.Background()
	if _, err := e.s.DB.Exec(ctx, `INSERT INTO analysis.clients VALUES ('c1', 'Client', now())`); err != nil {
		t.Fatal(err)
	}
	if err := e.s.TrackEntity(ctx, "c1", "korek", "competitor"); err != nil {
		t.Fatal(err)
	}
	r, _ := e.s.LoadRegistry(ctx)
	e.p.SetEntities(r)
	e.ingest(t, comment(1, "كورك احسن"), comment(2, "صباح الخير"))
	// Urgent (tracked) comment goes real time; the other one batch.
	if _, err := e.p.RealtimeOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatal(err)
	}
	// Follow-ups were queued and processed by the next batch round.
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:1' AND is_current AND route = 'escalation' AND model = 'strong-model'`); n != 1 {
		t.Fatal("escalated result is not current")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:2' AND route = 'escalation'`); n != 0 {
		t.Fatal("untracked item escalated")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE purpose = 'audit' AND NOT is_current AND model = 'audit-model'`); n != 2 {
		t.Fatal("audit results")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.entity_mentions WHERE content_id = 'fb:comment:1'`); n != 1 {
		t.Fatal("mentions should follow the current (escalated) analysis only")
	}
}

func TestSampled(t *testing.T) {
	n := 0
	for i := 0; i < 100000; i++ {
		if pipeline.Sampled("fb:comment:"+itoa(i), 0.01) {
			n++
		}
	}
	if n < 800 || n > 1200 {
		t.Fatalf("sampled %d of 100000 at 1%%", n)
	}
	if pipeline.Sampled("x", 0) || !pipeline.Sampled("x", 1) {
		t.Fatal("bounds")
	}
	first := pipeline.Sampled("fb:comment:42", 0.5)
	for i := 0; i < 3; i++ {
		if pipeline.Sampled("fb:comment:42", 0.5) != first {
			t.Fatal("not deterministic")
		}
	}
}

func TestCalibration(t *testing.T) {
	c := &pipeline.Calibration{Points: [][2]float64{{0.5, 0.3}, {0.9, 0.7}, {1, 0.8}}}
	for raw, want := range map[float64]float64{0.1: 0.3, 0.5: 0.3, 0.7: 0.5, 0.9: 0.7, 0.95: 0.75, 1.2: 0.8} {
		if got := c.Apply(raw); got < want-1e-9 || got > want+1e-9 {
			t.Errorf("Apply(%v) = %v, want %v", raw, got, want)
		}
	}
	var none *pipeline.Calibration
	if none.Apply(0.42) != 0.42 {
		t.Fatal("nil calibration must be identity")
	}
}

// failingBatcher wraps the fake provider with a batch API that fails.
type failingBatcher struct {
	*fake.Provider
	fail bool
}

func (f *failingBatcher) SubmitBatch(ctx context.Context, reqs []provider.Request) (string, error) {
	if f.fail {
		return "", &provider.Error{Status: 503, Retryable: true, Err: errOverloaded}
	}
	return f.Provider.SubmitBatch(ctx, reqs)
}

func TestBatchOutageWaitsThenFallsBack(t *testing.T) {
	e := setup(t, func(c *config.Analysis) {
		r := c.Routes[core.RouteComment]
		r.FallbackProvider, r.FallbackModel = "fallback", "fallback-model"
		c.Routes[core.RouteComment] = r
		c.Batch.MaxRequests = 1 // one request per batch: several chunks
		c.Grouping.MaxItems = 1
	})
	ctx := context.Background()
	fb := &failingBatcher{Provider: e.primary, fail: true}
	e.p.Providers.Add("primary", fb, true, 0)
	e.ingest(t, comment(1, "حلو"), comment(2, "سيء"), comment(3, "عادي"))
	// Down, but not for long: nothing is analysed, nothing stays leased.
	if err := e.p.SubmitBatches(ctx); err == nil {
		t.Fatal("submit error not reported")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses`); n != 0 {
		t.Fatal("analysed during a short outage")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_requests WHERE status = 'submitted'`); n != 0 {
		t.Fatalf("%d dangling submitted requests", n)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue WHERE attempts = 0 AND lease_until IS NULL`); n != 3 {
		t.Fatal("items not released without an attempt")
	}
	// After batch.fallback_after, the backlog goes to the fallback.
	e.p.Cfg.Batch.FallbackAfter = 0
	if _, err := e.s.DB.Exec(ctx, `UPDATE analysis.analysis_queue SET not_before = now() - interval '1s'`); err != nil {
		t.Fatal(err)
	}
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE is_current`); n != 3 {
		t.Fatalf("analysed %d after the outage threshold, want 3", n)
	}
	// Recovery: a successful submission clears the outage.
	fb.fail = false
	e.ingest(t, comment(4, "تمام"))
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_batches WHERE status = 'in_progress'`); n != 1 {
		t.Fatal("batch lane did not recover")
	}
}
