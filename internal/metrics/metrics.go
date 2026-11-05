// Package metrics defines every Prometheus metric the indexer exports, in one
// registry, so dashboards and alerts have a single source of truth.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var Registry = prometheus.NewRegistry()

func init() {
	Registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

func counter(name, help string) prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
	Registry.MustRegister(c)
	return c
}

func gauge(name, help string) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
	Registry.MustRegister(g)
	return g
}

func counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	Registry.MustRegister(c)
	return c
}

var (
	// Fetcher (RPC → queue, or RPC → processor in direct mode).
	FetchedLedgers = counter("indexer_fetch_ledgers_total", "Ledgers read from RPC.")
	FetchLast      = gauge("indexer_fetch_last_ledger", "Last ledger read from RPC.")
	RPCTip         = gauge("indexer_rpc_tip_ledger", "Latest ledger reported by RPC.")
	FetchErrors    = counterVec("indexer_fetch_errors_total", "Fetcher failures by stage.", "stage")

	// Queue.
	Produced        = counter("indexer_queue_produced_total", "Ledger records produced to the queue.")
	ProduceErrors   = counter("indexer_queue_produce_errors_total", "Failed produce attempts.")
	Consumed        = counter("indexer_queue_consumed_total", "Ledger records consumed from the queue.")
	ConsumerLag     = gauge("indexer_queue_consumer_lag", "Records between the writer's position and the topic end.")
	RejectedRecords = counterVec("indexer_queue_rejected_total", "Records ignored by the writer.", "reason")

	// Writer (processor → Postgres).
	WrittenLedgers = counter("indexer_write_ledgers_total", "Ledgers committed to Postgres.")
	SkippedLedgers = counter("indexer_write_skipped_total", "Ledgers skipped because they were already indexed.")
	WrittenRows    = counter("indexer_write_rows_total", "Rows written (all tables).")
	WriteErrors    = counter("indexer_write_errors_total", "Failed write transactions.")
	WriteDuration  = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "indexer_write_duration_seconds", Help: "Duration of one write transaction.",
		Buckets: prometheus.ExponentialBuckets(0.005, 2, 14),
	})
	LastWritten  = gauge("indexer_last_written_ledger", "Newest ledger committed to Postgres.")
	CloseLag     = gauge("indexer_ledger_close_lag_seconds", "Seconds between the newest committed ledger closing and its commit.")
	Gaps         = counter("indexer_ledger_gaps_total", "Ledger ranges that were never received.")
	ChainBreaks  = counter("indexer_chain_mismatch_total", "Ledgers whose prev_hash did not match the stored predecessor.")
	ProcessError = counter("indexer_process_errors_total", "Ledgers that failed to decode.")

	// Protocol verification.
	CodeLookups       = counterVec("indexer_contract_code_lookups_total", "Contract executable lookups via RPC.", "result")
	UnverifiedEvents  = counterVec("indexer_unverified_protocol_events_total", "Protocol-shaped events from contracts whose code is not allowlisted.", "protocol")
	VerifiedEvents    = counterVec("indexer_verified_protocol_events_total", "Protocol events from allowlisted contract code.", "protocol")
	PartitionsCreated = counter("indexer_partitions_created_total", "Ledger-range partitions created.")
	PartitionsDropped = counter("indexer_partitions_dropped_total", "Ledger-range partitions dropped by retention.")
	PrunedRows        = counter("indexer_pruned_rows_total", "Rows deleted by retention outside partition drops.")

	// HTTP API.
	HTTPRequests = counterVec("http_requests_total", "HTTP requests by route pattern and status code.", "route", "code")
	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_request_duration_seconds", Help: "HTTP request latency by route pattern.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2.5, 12),
	}, []string{"route"})
	RateLimited = counter("http_rate_limited_total", "Requests rejected by the rate limiter.")
	CacheHits   = counterVec("http_cache_total", "Response cache lookups.", "result")
)

func init() {
	Registry.MustRegister(WriteDuration, HTTPDuration)
}

// Handler serves the registry in the Prometheus text format.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{Registry: Registry})
}
