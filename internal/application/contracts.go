package application

import (
	"time"

	"github.com/enzom/jungle-gaming/internal/domain"
)

type MoneyView struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func ViewMoney(m domain.Money) MoneyView {
	return MoneyView{Amount: m.Amount(), Currency: m.Currency()}
}

type CreateWalletCommand struct {
	PlayerID       string       `validate:"required,uuid"`
	InitialBalance domain.Money `validate:"validateFn"`
	CorrelationID  string       `validate:"omitempty,max=256"`
}

type WalletView struct {
	ID        string    `json:"id"`
	PlayerID  string    `json:"playerId"`
	Balance   MoneyView `json:"balance"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

type ProcessWagerCommand struct {
	ProviderID                     string                 `validate:"notblank,max=128"`
	ExternalTransactionID          string                 `validate:"notblank,max=256"`
	IdempotencyKey                 string                 `validate:"notblank,max=256"`
	PlayerID                       string                 `validate:"required,uuid"`
	WalletID                       string                 `validate:"required,uuid"`
	RoundID                        string                 `validate:"notblank,max=256"`
	GameID                         string                 `validate:"notblank,max=256"`
	Kind                           domain.TransactionKind `validate:"required,oneof=BET WIN LOSS REFUND ROLLBACK"`
	Money                          domain.Money           `validate:"validateFn"`
	ReferenceExternalTransactionID string                 `validate:"required_if=Kind REFUND,required_if=Kind ROLLBACK,max=256"`
	CorrelationID                  string                 `validate:"omitempty,max=256"`
	Inbox                          *InboxInput            `validate:"omitempty"`
}

type InboxInput struct {
	ConsumerName string `validate:"notblank,max=128"`
	MessageID    string `validate:"notblank,max=256"`
	PayloadHash  string `validate:"required,len=64,hexadecimal"`
}

type ProcessWagerResult struct {
	TransactionID    string                   `json:"transactionId"`
	Status           domain.TransactionStatus `json:"status"`
	Balance          MoneyView                `json:"balance"`
	FailureCode      string                   `json:"failureCode,omitempty"`
	IdempotentReplay bool                     `json:"idempotentReplay"`
}

type TransactionView struct {
	ID                             string                   `json:"id"`
	ProviderID                     string                   `json:"providerId,omitempty"`
	ExternalTransactionID          string                   `json:"externalTransactionId,omitempty"`
	WalletID                       string                   `json:"walletId"`
	PlayerID                       string                   `json:"playerId"`
	RoundID                        string                   `json:"roundId,omitempty"`
	GameID                         string                   `json:"gameId,omitempty"`
	Kind                           domain.TransactionKind   `json:"kind"`
	Money                          MoneyView                `json:"money"`
	ReferenceExternalTransactionID string                   `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         string                   `json:"referenceTransactionId,omitempty"`
	Status                         domain.TransactionStatus `json:"status"`
	FailureCode                    string                   `json:"failureCode,omitempty"`
	ResultBalance                  *MoneyView               `json:"resultBalance,omitempty"`
	AttemptCount                   int                      `json:"attemptCount"`
	NextAttemptAt                  *time.Time               `json:"nextAttemptAt,omitempty"`
	CreatedAt                      time.Time                `json:"createdAt"`
	UpdatedAt                      time.Time                `json:"updatedAt"`
}

type LedgerEntryView struct {
	ID            string                 `json:"id"`
	TransactionID string                 `json:"transactionId"`
	Direction     domain.LedgerDirection `json:"direction"`
	Money         MoneyView              `json:"money"`
	BalanceBefore MoneyView              `json:"balanceBefore"`
	BalanceAfter  MoneyView              `json:"balanceAfter"`
	CreatedAt     time.Time              `json:"createdAt"`
}

type LedgerPage struct {
	Items      []LedgerEntryView `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

type ReconciliationView struct {
	WalletID          string    `json:"walletId"`
	StoredBalance     MoneyView `json:"storedBalance"`
	CalculatedBalance MoneyView `json:"calculatedBalance"`
	Difference        MoneyView `json:"difference"`
	Consistent        bool      `json:"consistent"`
	CheckedEntries    int64     `json:"checkedEntries"`
}

func transactionView(t *domain.WagerTransaction) TransactionView {
	view := TransactionView{
		ID: t.ID(), ProviderID: t.ProviderID(), ExternalTransactionID: t.ExternalID(), WalletID: t.WalletID(),
		PlayerID: t.PlayerID(), RoundID: t.RoundID(), GameID: t.GameID(), Kind: t.Kind(), Money: ViewMoney(t.Money()),
		ReferenceExternalTransactionID: t.ReferenceExternalID(), ReferenceTransactionID: t.ReferenceTransactionID(),
		Status: t.Status(), FailureCode: t.FailureCode(), AttemptCount: t.AttemptCount(), NextAttemptAt: t.NextAttemptAt(),
		CreatedAt: t.CreatedAt(), UpdatedAt: t.UpdatedAt(),
	}
	if balance, ok := t.ResultBalance(); ok {
		value := ViewMoney(balance)
		view.ResultBalance = &value
	}
	return view
}
