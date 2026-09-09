package application

import (
	"context"
	"time"

	"github.com/enzom/jungle-gaming/internal/domain"
)

type InboxDisposition int

const (
	InboxInserted InboxDisposition = iota + 1
	InboxAlreadyCompleted
)

type Validator interface {
	Struct(any) error
	Var(any, string) error
}

type Metrics interface {
	ObserveWager(status string, replay bool, duration time.Duration)
	IncConcurrencyConflict()
	IncSQSRetry(reason string)
	IncSQSDLQHandoff()
	ObserveOutboxLag(time.Duration)
	IncReconciliationDivergence()
}

type LedgerCursor struct {
	CreatedAt time.Time
	ID        string
}

type LedgerRecord struct {
	ID, TransactionID    string
	Direction            domain.LedgerDirection
	Money, Before, After domain.Money
	CreatedAt            time.Time
}

type OutboxEvent struct {
	EventID, AggregateID, EventType string
	Payload                         []byte
	OccurredAt                      time.Time
}

type ClaimedOutboxEvent struct {
	OutboxEvent
	Attempts int
}

type Store interface {
	WithinTransaction(context.Context, func(TxStore) error) error
	WithinReadOnlyTransaction(context.Context, func(TxStore) error) error
	GetWallet(context.Context, string) (*domain.Wallet, error)
	GetTransaction(context.Context, string) (*domain.WagerTransaction, error)
	GetProviderTransaction(context.Context, string, string) (*domain.WagerTransaction, error)
	ListLedger(context.Context, string, *LedgerCursor, int) ([]LedgerRecord, error)
	FindDuePendingReferences(context.Context, int, time.Time) ([]string, error)
	ClaimOutbox(context.Context, string, int, time.Duration, time.Time) ([]ClaimedOutboxEvent, error)
	MarkOutboxPublished(context.Context, string, string, time.Time) error
	ReleaseOutbox(context.Context, string, string, time.Time, string) error
	Ping(context.Context) error
}

type TxStore interface {
	AcquireIdempotencyLock(context.Context, string, string) error
	ClaimInbox(context.Context, InboxInput, time.Time) (InboxDisposition, error)
	CompleteInbox(context.Context, InboxInput, time.Time) error
	InsertWallet(context.Context, *domain.Wallet) error
	ReadWallet(context.Context, string) (*domain.Wallet, error)
	LockWallet(context.Context, string) (*domain.Wallet, error)
	UpdateWallet(context.Context, *domain.Wallet) error
	InsertOpening(context.Context, string, *domain.Wallet, domain.Money, time.Time) error
	FindByIdempotency(context.Context, string, string) (*domain.WagerTransaction, error)
	FindByExternalID(context.Context, string, string) (*domain.WagerTransaction, error)
	LockTransaction(context.Context, string) (*domain.WagerTransaction, error)
	InsertTransaction(context.Context, *domain.WagerTransaction) error
	UpdateTransaction(context.Context, *domain.WagerTransaction) error
	SuccessfulReversalExists(context.Context, string) (bool, error)
	InsertLedger(context.Context, *domain.WalletLedgerEntry) error
	InsertOutbox(context.Context, OutboxEvent) error
	SumLedger(context.Context, string, string) (credits int64, debits int64, count int64, err error)
}
