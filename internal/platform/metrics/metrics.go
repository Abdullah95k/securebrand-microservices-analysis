// Package metrics holds the Prometheus registry and sb-analysis's metrics.
// Every name starts with sb_analysis_.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Namespace prefixes every metric name.
const Namespace = "sb_analysis"

// Registry is the process registry exposed on /metrics.
var Registry = prometheus.NewRegistry()

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

func counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: name, Help: help}, labels)
	Registry.MustRegister(c)
	return c
}

func histVec(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: Namespace, Name: name, Help: help, Buckets: buckets}, labels)
	Registry.MustRegister(h)
	return h
}

func gauge(name, help string) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Namespace: Namespace, Name: name, Help: help})
	Registry.MustRegister(g)
	return g
}

func gaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: Namespace, Name: name, Help: help}, labels)
	Registry.MustRegister(g)
	return g
}

var (
	KafkaProduce       = counterVec("kafka_produce_total", "Produced records.", "topic", "outcome")
	KafkaCommitted     = counterVec("kafka_commits_total", "Offset commits.", "group", "outcome")
	KafkaHandlerPanics = counterVec("kafka_handler_panics_total", "Record handlers that panicked (the record is retried; its partition waits).", "group")
)
