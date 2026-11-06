package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/abdullah95k/securebrand-microservices-analysis/configs"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/app"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/kafka"
)

// ownTopicPrefixes are the topics the analysis service owns: the canonical
// content contract it reads and the results it publishes. Next to a running
// fbs, the analysis creates only these and never alters fbs's topics (their
// retention is fbs's to set).
var ownTopicPrefixes = []string{"content.", "analysis."}

// ownTopics returns the analysis topics of a topics.yaml document, with the
// partition counts capped (0 = no cap).
func ownTopics(doc []byte, partitionsCap int32) ([]kafka.TopicSpec, int16, error) {
	var d struct {
		Replicas int16             `yaml:"replicas"`
		Topics   []kafka.TopicSpec `yaml:"topics"`
	}
	if err := yaml.Unmarshal(doc, &d); err != nil {
		return nil, 0, err
	}
	var out []kafka.TopicSpec
	for _, t := range d.Topics {
		for _, p := range ownTopicPrefixes {
			if strings.HasPrefix(t.Name, p) {
				if partitionsCap > 0 && t.Partitions > partitionsCap {
					t.Partitions = partitionsCap
				}
				out = append(out, t)
				break
			}
		}
	}
	return out, d.Replicas, nil
}

// kafkaTopicsCmd creates the analysis topics (idempotent).
func kafkaTopicsCmd(args []string) error {
	fs := flag.NewFlagSet("kafka-topics", flag.ExitOnError)
	pcap := fs.Int("partitions-cap", 0, "cap partition counts (a one-broker Redpanda)")
	repl := fs.Int("replicas", 0, "override the replication factor")
	cfg := config.MustLoadFromFlags(fs, args)
	b, err := configs.FS.ReadFile("topics.yaml")
	if err != nil {
		return err
	}
	specs, replicas, err := ownTopics(b, int32(*pcap))
	if err != nil {
		return err
	}
	if *repl > 0 {
		replicas = int16(*repl)
	}
	kc, err := app.KafkaClient(cfg, "-analysis-admin")
	if err != nil {
		return err
	}
	p, err := kafka.NewProducer(kc)
	if err != nil {
		return err
	}
	defer p.Close()
	if err := kafka.EnsureTopics(context.Background(), p.Client(), cfg.Kafka.TopicPrefix, specs, replicas); err != nil {
		return err
	}
	fmt.Printf("%d analysis topics applied\n", len(specs))
	return nil
}
