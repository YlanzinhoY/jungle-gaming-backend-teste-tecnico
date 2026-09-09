package domain

import (
	"fmt"
	"time"
)

type Wallet struct {
	id        string
	playerID  string
	balance   Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

func NewWallet(id, playerID string, initialBalance Money, now time.Time) (*Wallet, error) {
	if id == "" || playerID == "" || now.IsZero() || initialBalance.IsNegative() {
		return nil, fmt.Errorf("%w: missing identity, timestamp, or negative initial balance", ErrInvalidWallet)
	}
	if err := initialBalance.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidWallet, err)
	}
	return &Wallet{
		id:        id,
		playerID:  playerID,
		balance:   initialBalance,
		version:   1,
		createdAt: now.UTC(),
		updatedAt: now.UTC(),
	}, nil
}

func RehydrateWallet(
	id, playerID string,
	balance Money,
	version int64,
	createdAt, updatedAt time.Time,
) (*Wallet, error) {
	if id == "" || playerID == "" || version < 1 || createdAt.IsZero() || updatedAt.IsZero() || balance.IsNegative() {
		return nil, fmt.Errorf("%w: invalid persisted state", ErrInvalidWallet)
	}
	if err := balance.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidWallet, err)
	}
	return &Wallet{
		id:        id,
		playerID:  playerID,
		balance:   balance,
		version:   version,
		createdAt: createdAt.UTC(),
		updatedAt: updatedAt.UTC(),
	}, nil
}

func (w *Wallet) Debit(amount Money, now time.Time) (Money, Money, error) {
	if !amount.IsPositive() || now.IsZero() {
		return Money{}, Money{}, fmt.Errorf("%w: debit must be positive", ErrInvalidMoney)
	}
	before := w.balance
	after, err := before.Subtract(amount)
	if err != nil {
		return Money{}, Money{}, err
	}
	if after.IsNegative() {
		return Money{}, Money{}, ErrInsufficientFunds
	}
	w.apply(after, now)
	return before, after, nil
}

func (w *Wallet) Credit(amount Money, now time.Time) (Money, Money, error) {
	if !amount.IsPositive() || now.IsZero() {
		return Money{}, Money{}, fmt.Errorf("%w: credit must be positive", ErrInvalidMoney)
	}
	before := w.balance
	after, err := before.Add(amount)
	if err != nil {
		return Money{}, Money{}, err
	}
	w.apply(after, now)
	return before, after, nil
}

func (w *Wallet) apply(balance Money, now time.Time) {
	w.balance = balance
	w.version++
	w.updatedAt = now.UTC()
}

func (w *Wallet) ID() string           { return w.id }
func (w *Wallet) PlayerID() string     { return w.playerID }
func (w *Wallet) Balance() Money       { return w.balance }
func (w *Wallet) Version() int64       { return w.version }
func (w *Wallet) CreatedAt() time.Time { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time { return w.updatedAt }
