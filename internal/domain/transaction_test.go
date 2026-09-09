package domain

import (
	"errors"
	"testing"
	"time"
)

func TestExternalTransactionAmountAndReferencePolicies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		kind      TransactionKind
		amount    string
		reference string
		wantErr   bool
	}{
		{name: "bet", kind: KindBet, amount: "1.00"},
		{name: "win", kind: KindWin, amount: "1.00"},
		{name: "win with reference", kind: KindWin, amount: "2.00", reference: "bet-1"},
		{name: "loss", kind: KindLoss, amount: "0.00"},
		{name: "refund", kind: KindRefund, amount: "1.00", reference: "bet-1"},
		{name: "rollback", kind: KindRollback, amount: "1.00", reference: "bet-1"},
		{name: "opening is internal", kind: KindOpening, amount: "1.00", wantErr: true},
		{name: "bet zero", kind: KindBet, amount: "0.00", wantErr: true},
		{name: "win zero", kind: KindWin, amount: "0.00", wantErr: true},
		{name: "loss positive", kind: KindLoss, amount: "1.00", wantErr: true},
		{name: "refund without reference", kind: KindRefund, amount: "1.00", wantErr: true},
		{name: "rollback without reference", kind: KindRollback, amount: "1.00", wantErr: true},
		{name: "bet with reference", kind: KindBet, amount: "1.00", reference: "bet-1", wantErr: true},
		{name: "unknown kind", kind: "UNKNOWN", amount: "1.00", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := validExternalTransaction(t, test.kind, test.amount)
			input.ReferenceExternalID = test.reference
			transaction, err := NewWagerTransaction(input)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidTransaction) {
					t.Fatalf("NewWagerTransaction() error = %v, want %v", err, ErrInvalidTransaction)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewWagerTransaction() error = %v", err)
			}
			if transaction.Status() != StatusPending {
				t.Fatalf("status = %s, want %s", transaction.Status(), StatusPending)
			}
		})
	}
}

func TestTransactionStateMachine(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	input := validExternalTransaction(t, KindRefund, "10.00")
	input.ReferenceExternalID = "bet-1"
	transaction, _ := NewWagerTransaction(input)
	next := now.Add(time.Minute)
	if err := transaction.MarkPendingReference(next, now); err != nil {
		t.Fatalf("MarkPendingReference() error = %v", err)
	}
	if transaction.Status() != StatusPendingReference || transaction.AttemptCount() != 1 {
		t.Fatalf("pending state = (%s, %d)", transaction.Status(), transaction.AttemptCount())
	}

	returnedNext := transaction.NextAttemptAt()
	*returnedNext = returnedNext.Add(time.Hour)
	if transaction.NextAttemptAt().Equal(*returnedNext) {
		t.Fatal("NextAttemptAt() exposed mutable aggregate state")
	}

	if err := transaction.ResolveReference("internal-bet", now.Add(time.Second)); err != nil {
		t.Fatalf("ResolveReference() error = %v", err)
	}
	if transaction.Status() != StatusPending || transaction.ReferenceTransactionID() != "internal-bet" {
		t.Fatalf("resolved state = (%s, %s)", transaction.Status(), transaction.ReferenceTransactionID())
	}
	if err := transaction.MarkProcessed(mustMoney(t, "100.00", "BRL"), now.Add(2*time.Second)); err != nil {
		t.Fatalf("MarkProcessed() error = %v", err)
	}
	if err := transaction.Reject("LATE_REJECTION", mustMoney(t, "100.00", "BRL"), now.Add(3*time.Second)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal Reject() error = %v, want %v", err, ErrInvalidTransition)
	}
}

func TestTransactionRejectAndFail(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	rejected, _ := NewWagerTransaction(validExternalTransaction(t, KindBet, "10.00"))
	if err := rejected.Reject(FailureInsufficientFunds, mustMoney(t, "5.00", "BRL"), now); err != nil {
		t.Fatalf("Reject() error = %v", err)
	}
	if rejected.Status() != StatusRejected || rejected.FailureCode() != FailureInsufficientFunds {
		t.Fatalf("rejected state = (%s, %s)", rejected.Status(), rejected.FailureCode())
	}

	failed, _ := NewWagerTransaction(validExternalTransaction(t, KindBet, "10.00"))
	if err := failed.Fail(FailureProcessingAttemptsExhausted, mustMoney(t, "5.00", "BRL"), now); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if failed.Status() != StatusFailed || failed.FailureCode() != FailureProcessingAttemptsExhausted {
		t.Fatalf("failed state = (%s, %s)", failed.Status(), failed.FailureCode())
	}
}

func TestRehydrateTransactionRejectsInconsistentState(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	state := RehydratedTransaction{
		ID:             "transaction",
		ProviderID:     "provider-a",
		ExternalID:     "external",
		IdempotencyKey: "key",
		PayloadHash:    "hash",
		WalletID:       "wallet",
		PlayerID:       "player",
		RoundID:        "round",
		GameID:         "game",
		Kind:           KindBet,
		Money:          mustMoney(t, "10.00", "BRL"),
		Status:         StatusProcessed,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if _, err := RehydrateWagerTransaction(state); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("RehydrateWagerTransaction() error = %v, want %v", err, ErrInvalidTransaction)
	}
}

func validExternalTransaction(t *testing.T, kind TransactionKind, amount string) NewExternalTransaction {
	t.Helper()
	return NewExternalTransaction{
		ID:             "transaction",
		ProviderID:     "provider-a",
		ExternalID:     "external",
		IdempotencyKey: "key",
		PayloadHash:    "hash",
		WalletID:       "wallet",
		PlayerID:       "player",
		RoundID:        "round",
		GameID:         "game",
		Kind:           kind,
		Money:          mustMoney(t, amount, "BRL"),
		Now:            time.Now().UTC(),
	}
}
