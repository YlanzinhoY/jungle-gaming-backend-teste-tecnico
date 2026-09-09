package application

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/enzom/jungle-gaming/internal/domain"
)

type IntegrationEventType string

const (
	EventWagerTransactionProcessed        IntegrationEventType = "WagerTransactionProcessed"
	EventWagerTransactionRejected         IntegrationEventType = "WagerTransactionRejected"
	EventWagerTransactionFailed           IntegrationEventType = "WagerTransactionFailed"
	EventWagerTransactionPendingReference IntegrationEventType = "WagerTransactionPendingReference"
	EventWalletBalanceChanged             IntegrationEventType = "WalletBalanceChanged"
)

type eventEnvelope[T any] struct {
	EventID       string               `json:"eventId"`
	EventType     IntegrationEventType `json:"eventType"`
	AggregateID   string               `json:"aggregateId"`
	CorrelationID string               `json:"correlationId"`
	CausationID   string               `json:"causationId,omitempty"`
	OccurredAt    string               `json:"occurredAt"`
	Version       int                  `json:"version"`
	Data          T                    `json:"data"`
}

type eventMetadata struct {
	CorrelationID string
	CausationID   string
	OccurredAt    time.Time
}

type WagerTransactionProcessedData struct {
	TransactionID          string                   `json:"transactionId"`
	ExternalTransactionID  string                   `json:"externalTransactionId,omitempty"`
	ProviderID             string                   `json:"providerId,omitempty"`
	WalletID               string                   `json:"walletId"`
	PlayerID               string                   `json:"playerId"`
	RoundID                string                   `json:"roundId,omitempty"`
	GameID                 string                   `json:"gameId,omitempty"`
	Kind                   domain.TransactionKind   `json:"kind"`
	Money                  MoneyView                `json:"money"`
	Status                 domain.TransactionStatus `json:"status"`
	Balance                MoneyView                `json:"balance"`
	ReferenceTransactionID string                   `json:"referenceTransactionId,omitempty"`
	ReferenceExternalID    string                   `json:"referenceExternalTransactionId,omitempty"`
}

type WagerTransactionRejectedData struct {
	TransactionID         string                   `json:"transactionId"`
	ExternalTransactionID string                   `json:"externalTransactionId"`
	ProviderID            string                   `json:"providerId"`
	WalletID              string                   `json:"walletId"`
	Kind                  domain.TransactionKind   `json:"kind"`
	Status                domain.TransactionStatus `json:"status"`
	FailureCode           string                   `json:"failureCode"`
	Balance               MoneyView                `json:"balance"`
}

type WagerTransactionFailedData struct {
	TransactionID         string                   `json:"transactionId"`
	ExternalTransactionID string                   `json:"externalTransactionId"`
	ProviderID            string                   `json:"providerId"`
	WalletID              string                   `json:"walletId"`
	Kind                  domain.TransactionKind   `json:"kind"`
	Status                domain.TransactionStatus `json:"status"`
	FailureCode           string                   `json:"failureCode"`
	Balance               MoneyView                `json:"balance"`
}

type WagerTransactionPendingReferenceData struct {
	TransactionID                  string                   `json:"transactionId"`
	ExternalTransactionID          string                   `json:"externalTransactionId"`
	ProviderID                     string                   `json:"providerId"`
	WalletID                       string                   `json:"walletId"`
	ReferenceExternalTransactionID string                   `json:"referenceExternalTransactionId"`
	Status                         domain.TransactionStatus `json:"status"`
	NextAttemptAt                  *time.Time               `json:"nextAttemptAt"`
}

type WalletBalanceChangedData struct {
	WalletID      string                 `json:"walletId"`
	TransactionID string                 `json:"transactionId"`
	Direction     domain.LedgerDirection `json:"direction"`
	Money         MoneyView              `json:"money"`
	BalanceBefore MoneyView              `json:"balanceBefore"`
	BalanceAfter  MoneyView              `json:"balanceAfter"`
	WalletVersion int64                  `json:"walletVersion"`
}

func newEvent[T any](
	eventType IntegrationEventType,
	aggregateID string,
	metadata eventMetadata,
	data T,
) (OutboxEvent, error) {
	eventID := uuid.NewString()
	envelope := eventEnvelope[T]{
		EventID:       eventID,
		EventType:     eventType,
		AggregateID:   aggregateID,
		CorrelationID: metadata.CorrelationID,
		CausationID:   metadata.CausationID,
		OccurredAt:    metadata.OccurredAt.UTC().Format(time.RFC3339Nano),
		Version:       1,
		Data:          data,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{
		EventID:     eventID,
		AggregateID: aggregateID,
		EventType:   string(eventType),
		Payload:     payload,
		OccurredAt:  metadata.OccurredAt.UTC(),
	}, nil
}
