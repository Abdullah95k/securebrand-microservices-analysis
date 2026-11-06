// Package config defines sb-analysis's configuration: embedded defaults
// (configs/default.yaml), then files passed with --config, then environment
// variables SB_ANALYSIS_<SECTION>__<KEY>.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/configs"
	pconfig "github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/config"
)

// EnvPrefix is the prefix of environment variable overrides, e.g.
// SB_ANALYSIS_KAFKA__BROKERS or SB_ANALYSIS_ANALYSIS__POSTGRES__DSN.
const EnvPrefix = "SB_ANALYSIS_"

// ConfigEnv names the config file(s) when --config is not given.
const ConfigEnv = "SB_ANALYSIS_CONFIG"

type Config struct {
	Service  Service  `koanf:"service"`
	Log      Log      `koanf:"log"`
	HTTP     HTTP     `koanf:"http"`
	Postgres Postgres `koanf:"postgres"`
	Kafka    Kafka    `koanf:"kafka"`
	// S3 is where the media workers read the files named in media events.
	S3 S3 `koanf:"s3"`
	// Analysis configures the analysis itself.
	Analysis Analysis `koanf:"analysis"`
}

type Service struct {
	Name string `koanf:"name"`
	Env  string `koanf:"env"`
}

type Log struct {
	Level  string `koanf:"level"`
	Format string `koanf:"format"`
}

// HTTP holds the ops listener (/metrics, /healthz, /readyz).
type HTTP struct {
	OpsAddr string `koanf:"ops_addr"`
}

// Postgres is the default database; analysis.postgres overrides it.
type Postgres struct {
	DSN      string `koanf:"dsn"`
	MaxConns int32  `koanf:"max_conns"`
}

type Kafka struct {
	Brokers       []string      `koanf:"brokers"`
	ClientID      string        `koanf:"client_id"`
	TopicPrefix   string        `koanf:"topic_prefix"`
	ProduceLinger time.Duration `koanf:"produce_linger"`
	TLS           TLS           `koanf:"tls"`
	SASL          SASL          `koanf:"sasl"`
}

// SASL authenticates to Kafka: Mechanism is plain, scram-sha-256 or
// scram-sha-512 ("" = none).
type SASL struct {
	Mechanism string `koanf:"mechanism"`
	Username  string `koanf:"username"`
	Password  string `koanf:"password"`
}

type S3 struct {
	Endpoint  string `koanf:"endpoint"`
	AccessKey string `koanf:"access_key"`
	SecretKey string `koanf:"secret_key"`
	UseSSL    bool   `koanf:"use_ssl"`
	Region    string `koanf:"region"`
}

// Load reads the config from defaults, optional files (comma separated,
// later files win) and environment variables.
func Load(path string) (*Config, error) {
	var c Config
	if _, err := pconfig.Load(configs.Default(), path, EnvPrefix, &c); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// MustLoadFromFlags parses --config (or SB_ANALYSIS_CONFIG) and loads the
// config, exiting on an error.
func MustLoadFromFlags(fs *flag.FlagSet, args []string) *Config {
	path := fs.String("config", os.Getenv(ConfigEnv), "YAML config file(s), comma separated; later files win")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	c, err := Load(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	return c
}

// Validate rejects configs that would misbehave at runtime.
func (c *Config) Validate() error {
	var errs []error
	switch c.Kafka.SASL.Mechanism {
	case "", "plain", "scram-sha-256", "scram-sha-512":
	default:
		errs = append(errs, fmt.Errorf("kafka.sasl.mechanism %q must be plain, scram-sha-256 or scram-sha-512", c.Kafka.SASL.Mechanism))
	}
	if err := c.Analysis.Validate(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
