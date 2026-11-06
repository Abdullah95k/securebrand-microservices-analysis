package app

import (
	"fmt"

	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/kafka"
)

// KafkaClient is a Kafka client configuration: id is appended to
// kafka.client_id, and kafka.tls and kafka.sasl secure the connections.
func KafkaClient(cfg *config.Config, id string) (kafka.ClientConfig, error) {
	tc, err := cfg.Kafka.TLS.Config()
	if err != nil {
		return kafka.ClientConfig{}, fmt.Errorf("kafka: %w", err)
	}
	kc := kafka.ClientConfig{Brokers: cfg.Kafka.Brokers, ClientID: cfg.Kafka.ClientID + id,
		TopicPrefix: cfg.Kafka.TopicPrefix, Linger: cfg.Kafka.ProduceLinger, TLS: tc}
	s := cfg.Kafka.SASL
	switch s.Mechanism {
	case "plain":
		kc.SASL = plain.Auth{User: s.Username, Pass: s.Password}.AsMechanism()
	case "scram-sha-256":
		kc.SASL = scram.Auth{User: s.Username, Pass: s.Password}.AsSha256Mechanism()
	case "scram-sha-512":
		kc.SASL = scram.Auth{User: s.Username, Pass: s.Password}.AsSha512Mechanism()
	}
	return kc, nil
}
