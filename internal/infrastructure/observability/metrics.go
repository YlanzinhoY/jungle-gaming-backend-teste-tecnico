package observability

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/enzom/jungle-gaming/internal/application"
)

type Metrics struct {
	registry                  *prometheus.Registry
	wagerResults              *prometheus.CounterVec
	idempotentReplays         prometheus.Counter
	concurrencyConflicts      prometheus.Counter
	sqsRetries                *prometheus.CounterVec
	sqsDLQHandoffs            prometheus.Counter
	outboxLag                 prometheus.Histogram
	wagerProcessingDuration   *prometheus.HistogramVec
	reconciliationDivergences prometheus.Counter
}

var _ application.Metrics = (*Metrics)(nil)

func New() *Metrics {
	metrics := &Metrics{
		registry: prometheus.NewRegistry(),
		wagerResults: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "jungle",
			Name:      "wager_transactions_total",
			Help:      "Total number of wager processing results by status.",
		}, []string{"status"}),
		idempotentReplays: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "jungle",
			Name:      "idempotent_replays_total",
			Help:      "Total number of idempotent wager replays.",
		}),
		concurrencyConflicts: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "jungle",
			Name:      "concurrency_conflicts_total",
			Help:      "Total number of persistence conflicts caused by concurrent writes.",
		}),
		sqsRetries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "jungle",
			Name:      "sqs_retries_total",
			Help:      "Total number of SQS message retries by reason.",
		}, []string{"reason"}),
		sqsDLQHandoffs: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "jungle",
			Name:      "sqs_dlq_handoffs_total",
			Help:      "Total number of messages released for SQS redrive to the DLQ.",
		}),
		outboxLag: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "jungle",
			Name:      "outbox_lag_seconds",
			Help:      "Delay between event occurrence and successful outbox publication.",
			Buckets:   prometheus.ExponentialBuckets(0.01, 2, 16),
		}),
		wagerProcessingDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "jungle",
			Name:      "wager_processing_duration_seconds",
			Help:      "End-to-end wager use-case processing duration by result status.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"status"}),
		reconciliationDivergences: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "jungle",
			Name:      "reconciliation_divergences_total",
			Help:      "Total number of wallet reconciliation divergences.",
		}),
	}

	metrics.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		metrics.wagerResults,
		metrics.idempotentReplays,
		metrics.concurrencyConflicts,
		metrics.sqsRetries,
		metrics.sqsDLQHandoffs,
		metrics.outboxLag,
		metrics.wagerProcessingDuration,
		metrics.reconciliationDivergences,
	)
	for _, status := range []string{"PENDING", "PENDING_REFERENCE", "PROCESSED", "REJECTED", "FAILED", "ERROR"} {
		metrics.wagerResults.WithLabelValues(status)
		metrics.wagerProcessingDuration.WithLabelValues(status)
	}
	return metrics
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

func (m *Metrics) ObserveWager(status string, replay bool, duration time.Duration) {
	if status == "" {
		status = "ERROR"
	}
	m.wagerResults.WithLabelValues(status).Inc()
	m.wagerProcessingDuration.WithLabelValues(status).Observe(duration.Seconds())
	if replay {
		m.idempotentReplays.Inc()
	}
}

func (m *Metrics) IncConcurrencyConflict() {
	m.concurrencyConflicts.Inc()
}

func (m *Metrics) IncSQSRetry(reason string) {
	m.sqsRetries.WithLabelValues(reason).Inc()
}

func (m *Metrics) IncSQSDLQHandoff() {
	m.sqsDLQHandoffs.Inc()
}

func (m *Metrics) ObserveOutboxLag(delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	m.outboxLag.Observe(delay.Seconds())
}

func (m *Metrics) IncReconciliationDivergence() {
	m.reconciliationDivergences.Inc()
}
