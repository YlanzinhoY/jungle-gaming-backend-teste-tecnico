package domain

import (
	"errors"
	"testing"
	"time"
)

func TestWalletDebitCreditAndVersion(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	wallet, err := NewWallet("wallet", "player", mustMoney(t, "100.00", "BRL"), now)
	if err != nil {
		t.Fatalf("NewWallet() error = %v", err)
	}
	if wallet.Version() != 1 {
		t.Fatalf("initial version = %d, want 1", wallet.Version())
	}

	before, after, err := wallet.Debit(mustMoney(t, "25.00", "BRL"), now.Add(time.Second))
	if err != nil || before.Amount() != "100.00" || after.Amount() != "75.00" || wallet.Version() != 2 {
		t.Fatalf("Debit() = (%s, %s, %v), version %d", before.Amount(), after.Amount(), err, wallet.Version())
	}
	_, after, err = wallet.Credit(mustMoney(t, "10.00", "BRL"), now.Add(2*time.Second))
	if err != nil || after.Amount() != "85.00" || wallet.Version() != 3 {
		t.Fatalf("Credit() = (%s, %v), version %d", after.Amount(), err, wallet.Version())
	}
}

func TestWalletRejectsInvalidMovementsWithoutMutation(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	wallet, _ := NewWallet("wallet", "player", mustMoney(t, "20.00", "BRL"), now)
	if _, _, err := wallet.Debit(mustMoney(t, "21.00", "BRL"), now); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("Debit() error = %v, want %v", err, ErrInsufficientFunds)
	}
	if wallet.Balance().Amount() != "20.00" || wallet.Version() != 1 {
		t.Fatalf("wallet mutated after rejected debit: balance=%s version=%d", wallet.Balance().Amount(), wallet.Version())
	}
	if _, _, err := wallet.Credit(mustMoney(t, "1.00", "USD"), now); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Credit() error = %v, want %v", err, ErrCurrencyMismatch)
	}
	if _, err := NewWallet("", "player", mustMoney(t, "1.00", "BRL"), now); !errors.Is(err, ErrInvalidWallet) {
		t.Fatalf("NewWallet() error = %v, want %v", err, ErrInvalidWallet)
	}
	negative, _ := NewMoneyFromMinor(-1, "BRL")
	if _, err := RehydrateWallet("wallet", "player", negative, 1, now, now); !errors.Is(err, ErrInvalidWallet) {
		t.Fatalf("RehydrateWallet() error = %v, want %v", err, ErrInvalidWallet)
	}
}
