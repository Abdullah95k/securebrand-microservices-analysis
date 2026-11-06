package store

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
)

// agentConn connects to the test database as role.
func agentConn(t *testing.T, s *Store, role, password string) *pgx.Conn {
	t.Helper()
	cc := s.DB.Config().ConnConfig
	u := url.URL{Scheme: "postgres", User: url.UserPassword(role, password), Host: fmt.Sprintf("%s:%d", cc.Host, cc.Port),
		Path: "/" + cc.Database, RawQuery: "sslmode=disable"}
	c, err := pgx.Connect(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

func TestAgentRolesAreScopedAndReadOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, `INSERT INTO analysis.entities (entity_id, name, type) VALUES ('brand-a', 'A', 'brand'), ('brand-b', 'B', 'brand')`); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	mk := func(id, source, entity string) {
		it := &core.Item{ContentID: id, Platform: "facebook", Kind: core.KindComment, SourceID: source, CreatedAt: at, ObservedAt: at,
			Text: "text " + id + " 07701234567", Status: core.StatusVisible}
		if _, err := s.Ingest(ctx, []*core.Item{it}, plan); err != nil {
			t.Fatal(err)
		}
		if _, err := s.WriteResults(ctx, []ItemResult{{Item: *it, Route: core.RouteComment, InputHash: it.InputHash(),
			Result: core.Result{Sentiment: "negative", Entities: []core.Entity{{ID: entity, Name: entity, Type: "brand", Sentiment: "negative"}}},
			Prov:   core.Provenance{Provider: "p", Model: "m", AnalyzedAt: time.Now()}}}, RequestOutcome{}); err != nil {
			t.Fatal(err)
		}
	}
	mk("c1", "fb:page:1", "brand-a") // client A's brand on any page
	mk("c2", "fb:page:2", "brand-b") // client B's brand
	mk("c3", "fb:page:3", "brand-b") // client B's brand on client A's monitored page
	if _, err := s.RecomputeDirtyDays(ctx, 10, 0); err != nil {
		t.Fatal(err)
	}
	suffix := rand.Int64N(1 << 30)
	roleA, roleB := fmt.Sprintf("sb_agent_a_%d", suffix), fmt.Sprintf("sb_agent_b_%d", suffix)
	t.Cleanup(func() {
		for _, r := range []string{roleA, roleB} {
			_, _ = s.DB.Exec(context.Background(), `DROP OWNED BY `+pgx.Identifier{r}.Sanitize())
			_, _ = s.DB.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{r}.Sanitize())
		}
	})
	pw := "correct-horse-battery-staple"
	if err := s.CreateAgentRole(ctx, "client-a", "Client A", roleA, pw, DefaultAgentLimits); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAgentRole(ctx, "client-b", "Client B", roleB, pw, DefaultAgentLimits); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAgentRole(ctx, "client-a", "x", "Bad-Name", pw, DefaultAgentLimits); err == nil {
		t.Fatal("invalid role name accepted")
	}
	if err := s.CreateAgentRole(ctx, "client-a", "x", roleA, "short", DefaultAgentLimits); err == nil {
		t.Fatal("short password accepted")
	}
	for _, q := range []struct{ client, sql string }{
		{"client-a", `INSERT INTO analysis.client_entities VALUES ('client-a', 'brand-a', 'own')`},
		{"client-a", `INSERT INTO analysis.client_sources VALUES ('client-a', 'fb:page:3')`},
		{"client-b", `INSERT INTO analysis.client_entities VALUES ('client-b', 'brand-b', 'own')`},
	} {
		if _, err := s.DB.Exec(ctx, q.sql); err != nil {
			t.Fatal(err)
		}
	}
	a, b := agentConn(t, s, roleA, pw), agentConn(t, s, roleB, pw)
	ids := func(c *pgx.Conn, q string) string {
		rows, err := c.Query(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(got, ",")
	}
	if got := ids(a, `SELECT content_id FROM agent.mentions ORDER BY content_id`); got != "c1,c3" {
		t.Fatalf("client A mentions = %q", got)
	}
	if got := ids(b, `SELECT content_id FROM agent.mentions ORDER BY content_id`); got != "c2,c3" {
		t.Fatalf("client B mentions = %q", got)
	}
	if got := ids(a, `SELECT content_id FROM agent.items ORDER BY content_id`); got != "c1,c3" {
		t.Fatalf("client A items = %q", got)
	}
	if got := ids(b, `SELECT entity_id FROM agent.rollup_entity_daily ORDER BY entity_id`); got != "brand-b,brand-b" {
		t.Fatalf("client B rollups = %q", got)
	}
	if got := ids(a, `SELECT text FROM agent.texts WHERE content_id = 'c1'`); strings.Contains(got, "0770") || !strings.Contains(got, "[phone]") {
		t.Fatalf("texts not masked: %q", got)
	}
	// Search goes through the same normalisation as the index.
	if got := ids(a, `SELECT content_id FROM agent.texts WHERE to_tsvector('simple', search_norm) @@ plainto_tsquery('simple', analysis.ar_normalize('TEXT c3'))`); got != "c3" {
		t.Fatalf("search = %q", got)
	}
	// The new fields and views are readable; authors are not.
	for _, q := range []string{
		`SELECT count(ad_type) + count(legal) + count(author_bot) FROM agent.items`,
		`SELECT count(*) FROM agent.mentions WHERE author_bot IS NULL`,
		`SELECT count(*) FROM agent.texts WHERE author_bot IS NULL`,
		`SELECT count(*) FROM agent.rollup_flag_daily`,
		`SELECT count(*) FROM agent.topics`,
	} {
		if _, err := a.Exec(ctx, q); err != nil {
			t.Fatalf("agent cannot read %s: %v", q, err)
		}
	}
	for _, q := range []string{
		`SELECT count(*) FROM analysis.authors`,
		`SELECT count(*) FROM analysis.content_items`,
		`SELECT count(*) FROM analysis.entity_mentions`,
		`SELECT count(*) FROM public.comments`, // fbs tables in the shared database
	} {
		if _, err := a.Exec(ctx, q); err == nil {
			t.Fatalf("agent read a base table: %s", q)
		}
	}
	if _, err := a.Exec(ctx, `INSERT INTO analysis.clients VALUES ('x', 'x', now())`); err == nil {
		t.Fatal("agent wrote")
	}
	var timeout string
	if err := a.QueryRow(ctx, `SHOW statement_timeout`).Scan(&timeout); err != nil || timeout != "15s" {
		t.Fatalf("statement_timeout = %q %v", timeout, err)
	}
	var ro string
	if err := a.QueryRow(ctx, `SHOW default_transaction_read_only`).Scan(&ro); err != nil || ro != "on" {
		t.Fatalf("read only = %q %v", ro, err)
	}
}
