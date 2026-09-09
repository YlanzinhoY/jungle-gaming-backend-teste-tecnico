package domain

import (
	"fmt"
	"time"
)

type LedgerDirection string

const (
	DirectionDebit  LedgerDirection = "DEBIT"
	DirectionCredit LedgerDirection = "CREDIT"
)

type WalletLedgerEntry struct {
	id, walletID, transactionID        string
	direction                          LedgerDirection
	money, balanceBefore, balanceAfter Money
	createdAt                          time.Time
}

func NewWalletLedgerEntry(
	id, walletID, transactionID string,
	direction LedgerDirection,
	money, before, after Money,
	now time.Time,
) (*WalletLedgerEntry, error) {
	if id == "" || walletID == "" || transactionID == "" || now.IsZero() || !money.IsPositive() {
		return nil, fmt.Errorf("%w: missing metadata or non-positive movement", ErrInvalidLedgerEntry)
	}
	var expected Money
	var err error
	switch direction {
	case DirectionDebit:
		expected, err = before.Subtract(money)
	case DirectionCredit:
		expected, err = before.Add(money)
	default:
		return nil, fmt.Errorf("%w: invalid direction", ErrInvalidLedgerEntry)
	}
	if err != nil || !expected.Equal(after) || after.IsNegative() {
		return nil, fmt.Errorf("%w: balances do not match movement", ErrInvalidLedgerEntry)
	}
	return &WalletLedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		money:         money,
		balanceBefore: before,
		balanceAfter:  after,
		createdAt:     now.UTC(),
	}, nil
}

func (e *WalletLedgerEntry) ID() string                 { return e.id }
func (e *WalletLedgerEntry) WalletID() string           { return e.walletID }
func (e *WalletLedgerEntry) TransactionID() string      { return e.transactionID }
func (e *WalletLedgerEntry) Direction() LedgerDirection { return e.direction }
func (e *WalletLedgerEntry) Money() Money               { return e.money }
func (e *WalletLedgerEntry) BalanceBefore() Money       { return e.balanceBefore }
func (e *WalletLedgerEntry) BalanceAfter() Money        { return e.balanceAfter }
func (e *WalletLedgerEntry) CreatedAt() time.Time       { return e.createdAt }
