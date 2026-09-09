package observability

import (
	"testing"
	"time"
)

func TestMetricsAreRegisteredAndRecorded(t *testing.T) {
	t.Parallel()

	metrics := New()
	metrics.ObserveWager("PROCESSED", true, 25*time.Millisecond)
	metrics.IncConcurrencyConflict()
	metrics.IncSQSRetry("transient")
	metrics.IncSQSDLQHandoff()
	metrics.ObserveOutboxLag(time.Second)
	metrics.IncReconciliationDivergence()

	families, err := metrics.registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	want := map[string]bool{
		"jungle_wager_transactions_total":          false,
		"jungle_idempotent_replays_total":          false,
		"jungle_concurrency_conflicts_total":       false,
		"jungle_sqs_retries_total":                 false,
		"jungle_sqs_dlq_handoffs_total":            false,
		"jungle_outbox_lag_seconds":                false,
		"jungle_wager_processing_duration_seconds": false,
		"jungle_reconciliation_divergences_total":  false,
	}
	for _, family := range families {
		if _, ok := want[family.GetName()]; ok {
			want[family.GetName()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("metric %s was not gathered", name)
		}
	}
}
