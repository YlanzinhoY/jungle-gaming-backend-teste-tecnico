package domain

import (
	"errors"
	"testing"
	"time"
)

func TestWalletLedgerEntryInvariants(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	tests := []struct {
		name      string
		direction LedgerDirection
		money     Money
		before    Money
		after     Money
		wantErr   bool
	}{
		{name: "debit", direction: DirectionDebit, money: mustMoney(t, "25.00", "BRL"), before: mustMoney(t, "100.00", "BRL"), after: mustMoney(t, "75.00", "BRL")},
		{name: "credit", direction: DirectionCredit, money: mustMoney(t, "25.00", "BRL"), before: mustMoney(t, "100.00", "BRL"), after: mustMoney(t, "125.00", "BRL")},
		{name: "wrong arithmetic", direction: DirectionDebit, money: mustMoney(t, "25.00", "BRL"), before: mustMoney(t, "100.00", "BRL"), after: mustMoney(t, "80.00", "BRL"), wantErr: true},
		{name: "invalid direction", direction: "SIDEWAYS", money: mustMoney(t, "25.00", "BRL"), before: mustMoney(t, "100.00", "BRL"), after: mustMoney(t, "75.00", "BRL"), wantErr: true},
		{name: "zero movement", direction: DirectionCredit, money: mustMoney(t, "0.00", "BRL"), before: mustMoney(t, "0.00", "BRL"), after: mustMoney(t, "0.00", "BRL"), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewWalletLedgerEntry("entry", "wallet", "transaction", test.direction, test.money, test.before, test.after, now)
			if test.wantErr && !errors.Is(err, ErrInvalidLedgerEntry) {
				t.Fatalf("NewWalletLedgerEntry() error = %v, want %v", err, ErrInvalidLedgerEntry)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("NewWalletLedgerEntry() error = %v", err)
			}
		})
	}
}
