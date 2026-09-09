package domain

import (
	"fmt"
	"time"
)

const (
	KindOpening  TransactionKind = "OPENING"
	KindBet      TransactionKind = "BET"
	KindWin      TransactionKind = "WIN"
	KindLoss     TransactionKind = "LOSS"
	KindRefund   TransactionKind = "REFUND"
	KindRollback TransactionKind = "ROLLBACK"
)

const (
	StatusPending          TransactionStatus = "PENDING"
	StatusPendingReference TransactionStatus = "PENDING_REFERENCE"
	StatusProcessed        TransactionStatus = "PROCESSED"
	StatusRejected         TransactionStatus = "REJECTED"
	StatusFailed           TransactionStatus = "FAILED"
)

type TransactionKind string

type TransactionStatus string

type WagerTransaction struct {
	id, providerID, externalID, idempotencyKey, payloadHash string
	walletID, playerID, roundID, gameID                     string
	kind                                                    TransactionKind
	money                                                   Money
	referenceExternalID, referenceTransactionID             string
	status                                                  TransactionStatus
	failureCode                                             string
	resultBalance                                           Money
	hasResultBalance                                        bool
	attemptCount                                            int
	nextAttemptAt                                           *time.Time
	createdAt, updatedAt                                    time.Time
}

type NewExternalTransaction struct {
	ID, ProviderID, ExternalID, IdempotencyKey, PayloadHash string
	WalletID, PlayerID, RoundID, GameID                     string
	Kind                                                    TransactionKind
	Money                                                   Money
	ReferenceExternalID                                     string
	Now                                                     time.Time
}

type RehydratedTransaction struct {
	ID, ProviderID, ExternalID, IdempotencyKey, PayloadHash string
	WalletID, PlayerID, RoundID, GameID                     string
	Kind                                                    TransactionKind
	Money                                                   Money
	ReferenceExternalID, ReferenceTransactionID             string
	Status                                                  TransactionStatus
	FailureCode                                             string
	ResultBalance                                           Money
	HasResultBalance                                        bool
	AttemptCount                                            int
	NextAttemptAt                                           *time.Time
	CreatedAt, UpdatedAt                                    time.Time
}

func NewWagerTransaction(input NewExternalTransaction) (*WagerTransaction, error) {
	if input.ID == "" || input.ProviderID == "" || input.ExternalID == "" || input.IdempotencyKey == "" ||
		input.PayloadHash == "" || input.WalletID == "" || input.PlayerID == "" ||
		input.RoundID == "" || input.GameID == "" || input.Now.IsZero() {
		return nil, fmt.Errorf("%w: required metadata is missing", ErrInvalidTransaction)
	}
	if input.Kind == KindOpening || !input.Kind.External() {
		return nil, fmt.Errorf("%w: unsupported external kind", ErrInvalidTransaction)
	}
	if err := validateAmountForKind(input.Kind, input.Money); err != nil {
		return nil, err
	}
	if (input.Kind == KindRefund || input.Kind == KindRollback) && input.ReferenceExternalID == "" {
		return nil, fmt.Errorf("%w: reference is required", ErrInvalidTransaction)
	}
	if input.ReferenceExternalID != "" && input.Kind != KindWin && input.Kind != KindRefund && input.Kind != KindRollback {
		return nil, fmt.Errorf("%w: reference is not allowed for %s", ErrInvalidTransaction, input.Kind)
	}
	return &WagerTransaction{
		id:                  input.ID,
		providerID:          input.ProviderID,
		externalID:          input.ExternalID,
		idempotencyKey:      input.IdempotencyKey,
		payloadHash:         input.PayloadHash,
		walletID:            input.WalletID,
		playerID:            input.PlayerID,
		roundID:             input.RoundID,
		gameID:              input.GameID,
		kind:                input.Kind,
		money:               input.Money,
		referenceExternalID: input.ReferenceExternalID,
		status:              StatusPending,
		createdAt:           input.Now.UTC(),
		updatedAt:           input.Now.UTC(),
	}, nil
}

func RehydrateWagerTransaction(s RehydratedTransaction) (*WagerTransaction, error) {
	if s.ID == "" || s.WalletID == "" || s.PlayerID == "" || s.CreatedAt.IsZero() ||
		s.UpdatedAt.IsZero() || (!s.Kind.External() && s.Kind != KindOpening) {
		return nil, fmt.Errorf("%w: invalid persisted state", ErrInvalidTransaction)
	}
	if s.Kind == KindOpening && (s.ProviderID != "" || s.ExternalID != "" ||
		s.Status != StatusProcessed || !s.Money.IsPositive()) {
		return nil, fmt.Errorf("%w: invalid persisted opening transaction", ErrInvalidTransaction)
	}
	if s.Kind.External() && (s.ProviderID == "" || s.ExternalID == "" || s.IdempotencyKey == "" || s.PayloadHash == "") {
		return nil, fmt.Errorf("%w: invalid persisted external transaction metadata", ErrInvalidTransaction)
	}
	if err := s.Money.Validate(); err != nil {
		return nil, err
	}
	if s.Kind.External() {
		if err := validateAmountForKind(s.Kind, s.Money); err != nil {
			return nil, err
		}
	}
	if s.Status != StatusPending && s.Status != StatusPendingReference &&
		s.Status != StatusProcessed && s.Status != StatusRejected && s.Status != StatusFailed {
		return nil, fmt.Errorf("%w: invalid persisted status", ErrInvalidTransaction)
	}
	if (s.Status == StatusProcessed || s.Status == StatusRejected || s.Status == StatusFailed) != s.HasResultBalance {
		return nil, fmt.Errorf("%w: terminal result is inconsistent", ErrInvalidTransaction)
	}
	if s.HasResultBalance && (s.ResultBalance.Validate() != nil || s.ResultBalance.IsNegative()) {
		return nil, fmt.Errorf("%w: invalid terminal balance", ErrInvalidTransaction)
	}
	if (s.Status == StatusProcessed || s.Status == StatusFailed) && s.ResultBalance.Currency() != s.Money.Currency() {
		return nil, fmt.Errorf("%w: terminal balance currency does not match", ErrInvalidTransaction)
	}
	if (s.Status == StatusRejected || s.Status == StatusFailed) != (s.FailureCode != "") {
		return nil, fmt.Errorf("%w: failure code is inconsistent", ErrInvalidTransaction)
	}
	if s.Status == StatusPendingReference {
		if s.ReferenceExternalID == "" || s.NextAttemptAt == nil || s.AttemptCount < 1 {
			return nil, fmt.Errorf("%w: pending reference state is incomplete", ErrInvalidTransaction)
		}
	} else if s.NextAttemptAt != nil {
		return nil, fmt.Errorf("%w: next attempt is only valid for a pending reference", ErrInvalidTransaction)
	}
	if s.ReferenceTransactionID != "" && s.ReferenceExternalID == "" {
		return nil, fmt.Errorf("%w: resolved reference has no external identity", ErrInvalidTransaction)
	}
	return &WagerTransaction{
		id: s.ID, providerID: s.ProviderID, externalID: s.ExternalID, idempotencyKey: s.IdempotencyKey,
		payloadHash: s.PayloadHash, walletID: s.WalletID, playerID: s.PlayerID, roundID: s.RoundID,
		gameID: s.GameID, kind: s.Kind, money: s.Money, referenceExternalID: s.ReferenceExternalID,
		referenceTransactionID: s.ReferenceTransactionID, status: s.Status, failureCode: s.FailureCode,
		resultBalance: s.ResultBalance, hasResultBalance: s.HasResultBalance, attemptCount: s.AttemptCount,
		nextAttemptAt: s.NextAttemptAt, createdAt: s.CreatedAt.UTC(), updatedAt: s.UpdatedAt.UTC(),
	}, nil
}

func (k TransactionKind) External() bool {
	return k == KindBet || k == KindWin || k == KindLoss || k == KindRefund || k == KindRollback
}

func validateAmountForKind(kind TransactionKind, amount Money) error {
	if err := amount.Validate(); err != nil {
		return err
	}
	if kind == KindLoss && !amount.IsZero() {
		return fmt.Errorf("%w: LOSS requires zero money", ErrInvalidTransaction)
	}
	if kind != KindLoss && !amount.IsPositive() {
		return fmt.Errorf("%w: %s requires positive money", ErrInvalidTransaction, kind)
	}
	return nil
}

func (t *WagerTransaction) MarkPendingReference(next time.Time, now time.Time) error {
	if t.terminal() || (t.status != StatusPending && t.status != StatusPendingReference) ||
		t.referenceExternalID == "" || now.IsZero() || !next.After(now) {
		return ErrInvalidTransition
	}
	t.status = StatusPendingReference
	t.attemptCount++
	next = next.UTC()
	t.nextAttemptAt = &next
	t.updatedAt = now.UTC()
	return nil
}

func (t *WagerTransaction) ResolveReference(referenceID string, now time.Time) error {
	if (t.status != StatusPending && t.status != StatusPendingReference) ||
		t.referenceExternalID == "" || referenceID == "" || now.IsZero() {
		return ErrInvalidTransition
	}
	t.referenceTransactionID = referenceID
	t.status = StatusPending
	t.nextAttemptAt = nil
	t.updatedAt = now.UTC()
	return nil
}

func (t *WagerTransaction) MarkProcessed(balance Money, now time.Time) error {
	if t.status != StatusPending || now.IsZero() || balance.IsNegative() ||
		balance.Validate() != nil || balance.Currency() != t.money.Currency() {
		return ErrInvalidTransition
	}
	t.status = StatusProcessed
	t.resultBalance = balance
	t.hasResultBalance = true
	t.failureCode = ""
	t.nextAttemptAt = nil
	t.updatedAt = now.UTC()
	return nil
}

func (t *WagerTransaction) Reject(code string, balance Money, now time.Time) error {
	if t.terminal() || code == "" || now.IsZero() || balance.IsNegative() || balance.Validate() != nil {
		return ErrInvalidTransition
	}
	t.status = StatusRejected
	t.failureCode = code
	t.resultBalance = balance
	t.hasResultBalance = true
	t.nextAttemptAt = nil
	t.updatedAt = now.UTC()
	return nil
}

func (t *WagerTransaction) Fail(code string, balance Money, now time.Time) error {
	if t.terminal() || code == "" || now.IsZero() || balance.IsNegative() ||
		balance.Validate() != nil || balance.Currency() != t.money.Currency() {
		return ErrInvalidTransition
	}
	t.status = StatusFailed
	t.failureCode = code
	t.resultBalance = balance
	t.hasResultBalance = true
	t.nextAttemptAt = nil
	t.updatedAt = now.UTC()
	return nil
}

func (t *WagerTransaction) terminal() bool {
	return t.status == StatusProcessed || t.status == StatusRejected || t.status == StatusFailed
}

func (t *WagerTransaction) ID() string                     { return t.id }
func (t *WagerTransaction) ProviderID() string             { return t.providerID }
func (t *WagerTransaction) ExternalID() string             { return t.externalID }
func (t *WagerTransaction) IdempotencyKey() string         { return t.idempotencyKey }
func (t *WagerTransaction) PayloadHash() string            { return t.payloadHash }
func (t *WagerTransaction) WalletID() string               { return t.walletID }
func (t *WagerTransaction) PlayerID() string               { return t.playerID }
func (t *WagerTransaction) RoundID() string                { return t.roundID }
func (t *WagerTransaction) GameID() string                 { return t.gameID }
func (t *WagerTransaction) Kind() TransactionKind          { return t.kind }
func (t *WagerTransaction) Money() Money                   { return t.money }
func (t *WagerTransaction) ReferenceExternalID() string    { return t.referenceExternalID }
func (t *WagerTransaction) ReferenceTransactionID() string { return t.referenceTransactionID }
func (t *WagerTransaction) Status() TransactionStatus      { return t.status }
func (t *WagerTransaction) FailureCode() string            { return t.failureCode }
func (t *WagerTransaction) ResultBalance() (Money, bool)   { return t.resultBalance, t.hasResultBalance }
func (t *WagerTransaction) AttemptCount() int              { return t.attemptCount }
func (t *WagerTransaction) NextAttemptAt() *time.Time {
	if t.nextAttemptAt == nil {
		return nil
	}
	value := *t.nextAttemptAt
	return &value
}
func (t *WagerTransaction) CreatedAt() time.Time { return t.createdAt }
func (t *WagerTransaction) UpdatedAt() time.Time { return t.updatedAt }
