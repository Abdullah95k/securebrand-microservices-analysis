package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLoadLayers(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	b := filepath.Join(dir, "b.yaml")
	_ = os.WriteFile(a, []byte("kafka:\n  client_id: a\nanalysis:\n  consumer_group: g-a\n"), 0o644)
	_ = os.WriteFile(b, []byte("analysis:\n  consumer_group: g-b\n"), 0o644)
	t.Setenv("SB_ANALYSIS_KAFKA__BROKERS", "k1:9092,k2:9092")
	t.Setenv("SB_ANALYSIS_ANALYSIS__POSTGRES__MAX_CONNS", "7")
	c, err := Load(a + "," + b)
	if err != nil {
		t.Fatal(err)
	}
	if c.Kafka.ClientID != "a" || c.Analysis.ConsumerGroup != "g-b" {
		t.Fatalf("layering wrong: client %q, group %q", c.Kafka.ClientID, c.Analysis.ConsumerGroup)
	}
	if len(c.Kafka.Brokers) != 2 || c.Analysis.Postgres.MaxConns != 7 {
		t.Fatalf("env override wrong: %v %d", c.Kafka.Brokers, c.Analysis.Postgres.MaxConns)
	}
	// Defaults survive.
	if c.Service.Name != "sb-analysis" || c.HTTP.OpsAddr != ":9090" || len(c.Analysis.Routes) == 0 {
		t.Fatalf("defaults lost: %+v %+v", c.Service, c.HTTP)
	}
}

// fbs's variables no longer configure sb-analysis.
func TestOldPrefixIgnored(t *testing.T) {
	t.Setenv("FBS_KAFKA__CLIENT_ID", "fbs-name")
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Kafka.ClientID != "sb-analysis" {
		t.Fatalf("client id %q", c.Kafka.ClientID)
	}
}

func TestValidateSASL(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"", "plain", "scram-sha-256", "scram-sha-512"} {
		c.Kafka.SASL.Mechanism = m
		if err := c.Validate(); err != nil {
			t.Errorf("%q refused: %v", m, err)
		}
	}
	c.Kafka.SASL.Mechanism = "gssapi"
	if c.Validate() == nil {
		t.Error("unknown mechanism accepted")
	}
}

// comments.sample_share: above 0, at most 1; 1 (all) by default; a quoted
// value from a values file reads as a number.
func TestCommentSampleShare(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Analysis.Comments.SampleShare != 1 {
		t.Errorf("default sample_share %v, want 1 (every comment)", c.Analysis.Comments.SampleShare)
	}
	for _, bad := range []float64{0, -0.1, 1.5} {
		c.Analysis.Comments.SampleShare = bad
		if c.Validate() == nil {
			t.Errorf("sample_share %v accepted", bad)
		}
	}
	f := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(f, []byte("analysis:\n  comments:\n    sample_share: \"0.10\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(f); err != nil || c.Analysis.Comments.SampleShare != 0.10 {
		t.Fatalf("quoted 0.10: %v %v", c, err)
	}
}

// media.recent_first is off by default; its deadline share is a share,
// and needs its window while recent_first is on.
func TestMediaRecentFirst(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	m := c.Analysis.Media
	if m.RecentFirst != 0 || m.DeadlineShare != 0.25 || m.DeadlineWithin != 12*time.Hour {
		t.Errorf("defaults recent_first %v, deadline_share %v, deadline_within %v; want off, 0.25, 12h", m.RecentFirst, m.DeadlineShare, m.DeadlineWithin)
	}
	for _, bad := range []func(*AnalysisMedia){
		func(m *AnalysisMedia) { m.DeadlineShare = 1.5 },
		func(m *AnalysisMedia) { m.DeadlineShare = -0.1 },
		func(m *AnalysisMedia) { m.RecentFirst = -time.Hour },
		func(m *AnalysisMedia) { m.RecentFirst, m.DeadlineWithin = 6*time.Hour, 0 },
	} {
		c.Analysis.Media = m
		bad(&c.Analysis.Media)
		if c.Validate() == nil {
			t.Errorf("media %+v accepted", c.Analysis.Media)
		}
	}
	c.Analysis.Media = m
	c.Analysis.Media.RecentFirst = 6 * time.Hour
	if err := c.Validate(); err != nil {
		t.Errorf("recent_first 6h refused: %v", err)
	}
}

// batch.top_up_items as a quoted string, as a gitops values file may set
// it: "0" turns the top-up off.
func TestTopUpItemsQuoted(t *testing.T) {
	for _, c := range []struct {
		yaml string
		want int
	}{{`"0"`, 0}, {`"10000"`, 10000}, {`0`, 0}} {
		f := filepath.Join(t.TempDir(), "c.yaml")
		if err := os.WriteFile(f, []byte("analysis:\n  batch:\n    top_up_items: "+c.yaml+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(f)
		if err != nil {
			t.Fatalf("top_up_items %s: %v", c.yaml, err)
		}
		if cfg.Analysis.Batch.TopUpItems != c.want {
			t.Errorf("top_up_items %s read as %d, want %d", c.yaml, cfg.Analysis.Batch.TopUpItems, c.want)
		}
	}
}

// analysis.inputs: fb, fb-media and content; fb and fb-media not together.
func TestAnalysisInputs(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range [][]string{{"content", "fb"}, {"content", "fb-media"}, {"fb"}, {"content"}} {
		c.Analysis.Inputs = ok
		if err := c.Validate(); err != nil {
			t.Errorf("inputs %v refused: %v", ok, err)
		}
	}
	for _, bad := range [][]string{{"fb", "fb-media"}, {"content", "x"}} {
		c.Analysis.Inputs = bad
		if c.Validate() == nil {
			t.Errorf("inputs %v accepted", bad)
		}
	}
}

func TestAnalysisPrivacyDefaults(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Analysis.Erasure.Mode != ErasureRecord || !c.Analysis.Privacy.MaskTaggedUsers || !c.Analysis.Privacy.MaskPII {
		t.Fatalf("defaults: erasure %q, mask tagged %v, mask pii %v", c.Analysis.Erasure.Mode, c.Analysis.Privacy.MaskTaggedUsers,
			c.Analysis.Privacy.MaskPII)
	}
	for _, mode := range []string{ErasureApply, ErasureOff} {
		c.Analysis.Erasure.Mode = mode
		if err := c.Validate(); err != nil {
			t.Errorf("%s refused: %v", mode, err)
		}
	}
	for _, mode := range []string{"", "delete"} {
		c.Analysis.Erasure.Mode = mode
		if c.Validate() == nil {
			t.Errorf("erasure mode %q accepted", mode)
		}
	}
}

// The platform's secret gives the database as parts; any password must
// survive, and the parts win over a default DSN.
func TestPostgresConnString(t *testing.T) {
	for _, pw := range []string{"plain", `it's a "pass" with spaces`, `back\slash'`} {
		p := Postgres{DSN: "postgres://default@localhost/x", Host: "db.internal", Port: "5433",
			Database: "analysis", User: "analysis", Password: pw, SSLMode: "require"}
		pc, err := pgxpool.ParseConfig(p.ConnString())
		if err != nil {
			t.Fatalf("%q: %v", pw, err)
		}
		cc := pc.ConnConfig
		if cc.Host != "db.internal" || cc.Port != 5433 || cc.Database != "analysis" || cc.User != "analysis" || cc.Password != pw {
			t.Fatalf("%q: parsed %s:%d/%s user %s password %q", pw, cc.Host, cc.Port, cc.Database, cc.User, cc.Password)
		}
	}
	withCA := Postgres{Host: "db", SSLMode: "require", SSLRootCert: "/etc/sb-analysis-certs/postgres-ca.pem"}.ConnString()
	if !strings.Contains(withCA, "sslrootcert='/etc/sb-analysis-certs/postgres-ca.pem'") {
		t.Fatalf("CA file missing: %q", withCA)
	}
	if got := (Postgres{DSN: "postgres://a@b/c"}).ConnString(); got != "postgres://a@b/c" {
		t.Fatalf("without host: %q", got)
	}
}

// The variables a chart maps from platform-credentials, in the platform's
// spellings.
func TestPlatformCredentialsEnv(t *testing.T) {
	for k, v := range map[string]string{
		"SB_ANALYSIS_POSTGRES__HOST":         "pg.platform",
		"SB_ANALYSIS_POSTGRES__PORT":         "5432",
		"SB_ANALYSIS_POSTGRES__DATABASE":     "analysis",
		"SB_ANALYSIS_POSTGRES__USER":         "analysis",
		"SB_ANALYSIS_POSTGRES__PASSWORD":     "p@ss:w/rd",
		"SB_ANALYSIS_POSTGRES__SSLMODE":      "verify-full",
		"SB_ANALYSIS_KAFKA__BROKERS":         "k1:9093,k2:9093",
		"SB_ANALYSIS_KAFKA__SASL__MECHANISM": "SCRAM-SHA-512",
		"SB_ANALYSIS_KAFKA__SASL__USERNAME":  "analysis-kafka",
		"SB_ANALYSIS_KAFKA__TLS__ENABLED":    "true",
		"SB_ANALYSIS_S3__ENDPOINT":           "https://s3.platform:443/",
		"SB_ANALYSIS_VALKEY__HOST":           "valkey.platform",
		"SB_ANALYSIS_VALKEY__PORT":           "25061",
		"SB_ANALYSIS_VALKEY__TLS__ENABLED":   "true",
	} {
		t.Setenv(k, v)
	}
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	pc, err := pgxpool.ParseConfig(c.Postgres.ConnString())
	if err != nil || pc.ConnConfig.Host != "pg.platform" || pc.ConnConfig.Password != "p@ss:w/rd" {
		t.Fatalf("postgres: %v %+v", err, pc)
	}
	if len(c.Kafka.Brokers) != 2 || c.Kafka.SASL.Mechanism != "scram-sha-512" || c.Kafka.SASL.Username != "analysis-kafka" || !c.Kafka.TLS.Enabled {
		t.Fatalf("kafka: %+v", c.Kafka)
	}
	if c.S3.Endpoint != "s3.platform:443" || !c.S3.UseSSL {
		t.Fatalf("s3: %+v", c.S3)
	}
	if c.Valkey.Address() != "valkey.platform:25061" || !c.Valkey.TLS.Enabled {
		t.Fatalf("valkey: %+v", c.Valkey)
	}
	if got := (Valkey{Addr: "localhost:6379"}).Address(); got != "localhost:6379" {
		t.Fatalf("valkey addr: %q", got)
	}
	if got := (Valkey{Host: "v"}).Address(); got != "v:6379" {
		t.Fatalf("valkey default port: %q", got)
	}
}

// No unlimited budget by accident: a daily cap of 0 (none) is refused
// outside dev unless allow_unlimited says so on purpose.
func TestBudgetNeverUnlimitedByAccident(t *testing.T) {
	for name, tc := range map[string]struct {
		env       string
		daily     float64
		allow, ok bool
	}{
		"prod, no cap":                 {"prod", 0, false, false},
		"prod, no cap on purpose":      {"prod", 0, true, true},
		"prod, $5":                     {"prod", 5, false, true},
		"pilot, no cap":                {"pilot", 0, false, false},
		"pilot, $5":                    {"pilot", 5, false, true},
		"staging, no cap":              {"staging", 0, false, false},
		"no env set, no cap":           {"", 0, false, false},
		"dev, no cap (local and test)": {"dev", 0, false, true},
	} {
		c, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		c.Service.Env, c.Analysis.Budget.DailyUSD, c.Analysis.Budget.AllowUnlimited = tc.env, tc.daily, tc.allow
		if err := c.Validate(); (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The chart's production values set the cap; Load must accept them as a
// production file would arrive (env prod, $5).
func TestProductionBudgetFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "prod.yaml")
	_ = os.WriteFile(f, []byte("service: {env: prod}\nanalysis:\n  budget: {daily_usd: 5}\n"), 0o644)
	c, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	if c.Analysis.Budget.DailyUSD != 5 || c.Analysis.Budget.AllowUnlimited {
		t.Fatalf("budget %+v", c.Analysis.Budget)
	}
	_ = os.WriteFile(f, []byte("service: {env: prod}\n"), 0o644)
	if _, err := Load(f); err == nil || !strings.Contains(err.Error(), "allow_unlimited") {
		t.Fatalf("a production file without a cap loaded: %v", err)
	}
}
