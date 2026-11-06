package config

import (
	"os"
	"path/filepath"
	"testing"
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
