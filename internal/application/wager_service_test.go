package application

import (
	"testing"
	"time"

	"github.com/enzom/jungle-gaming/internal/domain"
)

func TestWagerPayloadHashIsDeterministicAndBusinessSensitive(t *testing.T) {
	t.Parallel()

	money, _ := domain.ParseMoney("25.00", "BRL")
	command := ProcessWagerCommand{
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "ignored-by-hash",
		PlayerID:              "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
		WalletID:              "0192f291-27dd-7d3f-8071-5f8685deef37",
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  domain.KindBet,
		Money:                 money,
	}

	first, err := wagerPayloadHash(command)
	if err != nil {
		t.Fatalf("wagerPayloadHash() error = %v", err)
	}
	command.IdempotencyKey = "another-transport-key"
	command.CorrelationID = "another-correlation"
	second, _ := wagerPayloadHash(command)
	if first != second {
		t.Fatalf("transport metadata changed hash: %s != %s", first, second)
	}
	command.RoundID = "different-round"
	third, _ := wagerPayloadHash(command)
	if first == third {
		t.Fatal("business field change did not change hash")
	}
}

func TestReferenceBackoffIsExponentialAndCapped(t *testing.T) {
	t.Parallel()

	service := WagerService{policy: ReferencePolicy{
		BaseBackoff: 2 * time.Second,
		MaxBackoff:  10 * time.Second,
	}}
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: 2 * time.Second},
		{attempt: 1, want: 2 * time.Second},
		{attempt: 2, want: 4 * time.Second},
		{attempt: 3, want: 8 * time.Second},
		{attempt: 4, want: 10 * time.Second},
		{attempt: 20, want: 10 * time.Second},
	}
	for _, test := range tests {
		if got := service.backoff(test.attempt); got != test.want {
			t.Errorf("backoff(%d) = %v, want %v", test.attempt, got, test.want)
		}
	}
}
