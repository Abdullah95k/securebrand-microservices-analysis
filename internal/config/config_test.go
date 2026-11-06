package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
}
