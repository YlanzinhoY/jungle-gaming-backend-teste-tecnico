package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/enzom/jungle-gaming/internal/domain"
)

type ReferencePolicy struct {
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

type WagerService struct {
	store    Store
	validate Validator
	metrics  Metrics
	log      *slog.Logger
	policy   ReferencePolicy
}

func NewWagerService(
	store Store,
	validate Validator,
	metrics Metrics,
	log *slog.Logger,
	policy ReferencePolicy,
) *WagerService {
	return &WagerService{
		store:    store,
		validate: validate,
		metrics:  metrics,
		log:      log,
		policy:   policy,
	}
}

func (s *WagerService) Process(
	ctx context.Context,
	command ProcessWagerCommand,
) (result ProcessWagerResult, err error) {
	ctx, span := otel.Tracer("github.com/enzom/jungle-gaming/application").Start(ctx, "WagerService.Process")
	span.SetAttributes(
		attribute.String("app.provider_id", command.ProviderID),
		attribute.String("app.wallet_id", command.WalletID),
		attribute.String("app.correlation_id", command.CorrelationID),
	)
	started := time.Now()
	defer func() {
		s.metrics.ObserveWager(string(result.Status), result.IdempotentReplay, time.Since(started))
		if errors.Is(err, ErrConflict) {
			s.metrics.IncConcurrencyConflict()
		}
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "wager processing failed")
		} else {
			span.SetAttributes(
				attribute.String("app.transaction_id", result.TransactionID),
				attribute.String("app.transaction_status", string(result.Status)),
				attribute.Bool("app.idempotent_replay", result.IdempotentReplay),
			)
		}
		span.End()
	}()

	if err := s.validate.Struct(command); err != nil {
		return ProcessWagerResult{}, fmt.Errorf("%w: %v", domain.ErrInvalidTransaction, err)
	}
	payloadHash, err := wagerPayloadHash(command)
	if err != nil {
		return ProcessWagerResult{}, err
	}
	now := time.Now().UTC()
	correlationID := command.CorrelationID
	if correlationID == "" {
		correlationID = uuid.NewString()
	}
	causationID := ""
	if command.Inbox != nil {
		causationID = command.Inbox.MessageID
	}
	err = s.store.WithinTransaction(ctx, func(tx TxStore) error {
		if command.Inbox != nil {
			disposition, err := tx.ClaimInbox(ctx, *command.Inbox, now)
			if err != nil {
				return err
			}
			if disposition == InboxAlreadyCompleted {
				existing, err := tx.FindByIdempotency(ctx, command.ProviderID, command.IdempotencyKey)
				if err != nil {
					return err
				}
				if existing == nil || existing.PayloadHash() != payloadHash {
					return ErrInboxPayloadConflict
				}
				result, err = s.replayResult(ctx, tx, existing)
				return err
			}
		}

		if err := tx.AcquireIdempotencyLock(ctx, command.ProviderID, command.IdempotencyKey); err != nil {
			return err
		}
		existing, err := tx.FindByIdempotency(ctx, command.ProviderID, command.IdempotencyKey)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.PayloadHash() != payloadHash {
				return ErrIdempotencyPayloadConflict
			}
			result, err = s.replayResult(ctx, tx, existing)
			if err != nil {
				return err
			}
			return s.completeInbox(ctx, tx, command.Inbox, now)
		}
		byExternal, err := tx.FindByExternalID(ctx, command.ProviderID, command.ExternalTransactionID)
		if err != nil {
			return err
		}
		if byExternal != nil {
			return ErrExternalTransactionConflict
		}

		wallet, err := tx.LockWallet(ctx, command.WalletID)
		if err != nil {
			return err
		}
		transaction, err := domain.NewWagerTransaction(domain.NewExternalTransaction{
			ID:                  uuid.NewString(),
			ProviderID:          command.ProviderID,
			ExternalID:          command.ExternalTransactionID,
			IdempotencyKey:      command.IdempotencyKey,
			PayloadHash:         payloadHash,
			WalletID:            command.WalletID,
			PlayerID:            command.PlayerID,
			RoundID:             command.RoundID,
			GameID:              command.GameID,
			Kind:                command.Kind,
			Money:               command.Money,
			ReferenceExternalID: command.ReferenceExternalTransactionID,
			Now:                 now,
		})
		if err != nil {
			return err
		}

		if wallet.PlayerID() != command.PlayerID || wallet.Balance().Currency() != command.Money.Currency() {
			code := domain.FailureWalletOwnershipOrCurrencyMismatch
			if err := s.reject(ctx, tx, transaction, wallet, code, correlationID, causationID, now, true); err != nil {
				return err
			}
			result = resultFrom(transaction, wallet.Balance(), false)
			return s.completeInbox(ctx, tx, command.Inbox, now)
		}

		var reference *domain.WagerTransaction
		if command.ReferenceExternalTransactionID != "" {
			reference, err = tx.FindByExternalID(ctx, command.ProviderID, command.ReferenceExternalTransactionID)
			if err != nil {
				return err
			}
			if referenceIsPending(reference) {
				if err := transaction.MarkPendingReference(now.Add(s.backoff(transaction.AttemptCount()+1)), now); err != nil {
					return err
				}
				if err := tx.InsertTransaction(ctx, transaction); err != nil {
					return err
				}
				if err := s.pendingEvent(ctx, tx, transaction, correlationID, causationID, now); err != nil {
					return err
				}
				result = resultFrom(transaction, wallet.Balance(), false)
				return s.completeInbox(ctx, tx, command.Inbox, now)
			}
			if err := transaction.ResolveReference(reference.ID(), now); err != nil {
				return err
			}
			if reference.Status() != domain.StatusProcessed {
				if err := s.reject(
					ctx,
					tx,
					transaction,
					wallet,
					domain.FailureReferenceNotProcessed,
					correlationID,
					causationID,
					now,
					true,
				); err != nil {
					return err
				}
				result = resultFrom(transaction, wallet.Balance(), false)
				return s.completeInbox(ctx, tx, command.Inbox, now)
			}
		}

		code, err := s.referenceFailure(ctx, tx, transaction, reference)
		if err != nil {
			return err
		}
		if code != "" {
			if err := s.reject(ctx, tx, transaction, wallet, code, correlationID, causationID, now, true); err != nil {
				return err
			}
			result = resultFrom(transaction, wallet.Balance(), false)
			return s.completeInbox(ctx, tx, command.Inbox, now)
		}
		if err := s.finalize(
			ctx,
			tx,
			transaction,
			wallet,
			reference,
			correlationID,
			causationID,
			now,
			true,
		); err != nil {
			return err
		}
		result = resultFrom(transaction, wallet.Balance(), false)
		return s.completeInbox(ctx, tx, command.Inbox, now)
	})
	if err != nil {
		return ProcessWagerResult{}, err
	}
	s.log.InfoContext(
		ctx,
		"wager handled",
		"correlationId", correlationID,
		"transactionId", result.TransactionID,
		"walletId", command.WalletID,
		"providerId", command.ProviderID,
		"status", result.Status,
		"idempotentReplay", result.IdempotentReplay,
	)
	return result, nil
}

func (s *WagerService) ResolvePendingReference(ctx context.Context, transactionID string) error {
	now := time.Now().UTC()
	return s.store.WithinTransaction(ctx, func(tx TxStore) error {
		transaction, err := tx.LockTransaction(ctx, transactionID)
		if err != nil {
			return err
		}
		if transaction.Status() != domain.StatusPendingReference {
			return nil
		}
		wallet, err := tx.LockWallet(ctx, transaction.WalletID())
		if err != nil {
			return err
		}
		reference, err := tx.FindByExternalID(ctx, transaction.ProviderID(), transaction.ReferenceExternalID())
		if err != nil {
			return err
		}
		correlationID := transaction.ID()
		if referenceIsPending(reference) {
			if transaction.AttemptCount() >= s.policy.MaxAttempts {
				return s.reject(ctx, tx, transaction, wallet, domain.FailureReferenceNotFound, correlationID, "", now, false)
			}
			if err := transaction.MarkPendingReference(now.Add(s.backoff(transaction.AttemptCount()+1)), now); err != nil {
				return err
			}
			return tx.UpdateTransaction(ctx, transaction)
		}
		if err := transaction.ResolveReference(reference.ID(), now); err != nil {
			return err
		}
		if reference.Status() != domain.StatusProcessed {
			return s.reject(ctx, tx, transaction, wallet, domain.FailureReferenceNotProcessed, correlationID, "", now, false)
		}
		code, err := s.referenceFailure(ctx, tx, transaction, reference)
		if err != nil {
			return err
		}
		if code != "" {
			return s.reject(ctx, tx, transaction, wallet, code, correlationID, "", now, false)
		}
		return s.finalize(ctx, tx, transaction, wallet, reference, correlationID, "", now, false)
	})
}

// RecordPendingReferenceFailure durably postpones a technical failure. A
// repeatedly failing operation becomes terminal without moving money.
func (s *WagerService) RecordPendingReferenceFailure(ctx context.Context, transactionID string) error {
	now := time.Now().UTC()
	return s.store.WithinTransaction(ctx, func(tx TxStore) error {
		transaction, err := tx.LockTransaction(ctx, transactionID)
		if err != nil {
			return err
		}
		if transaction.Status() != domain.StatusPendingReference {
			return nil
		}
		wallet, err := tx.LockWallet(ctx, transaction.WalletID())
		if err != nil {
			return err
		}
		if transaction.AttemptCount() < s.policy.MaxAttempts {
			if err := transaction.MarkPendingReference(now.Add(s.backoff(transaction.AttemptCount()+1)), now); err != nil {
				return err
			}
			return tx.UpdateTransaction(ctx, transaction)
		}
		if err := transaction.Fail(domain.FailureProcessingAttemptsExhausted, wallet.Balance(), now); err != nil {
			return err
		}
		if err := tx.UpdateTransaction(ctx, transaction); err != nil {
			return err
		}
		event, err := newEvent(
			EventWagerTransactionFailed,
			transaction.ID(),
			eventMetadata{CorrelationID: transaction.ID(), OccurredAt: now},
			WagerTransactionFailedData{
				TransactionID:         transaction.ID(),
				ExternalTransactionID: transaction.ExternalID(),
				ProviderID:            transaction.ProviderID(),
				WalletID:              transaction.WalletID(),
				Kind:                  transaction.Kind(),
				Status:                transaction.Status(),
				FailureCode:           transaction.FailureCode(),
				Balance:               ViewMoney(wallet.Balance()),
			},
		)
		if err != nil {
			return err
		}
		return tx.InsertOutbox(ctx, event)
	})
}

func (s *WagerService) finalize(
	ctx context.Context,
	tx TxStore,
	transaction *domain.WagerTransaction,
	wallet *domain.Wallet,
	reference *domain.WagerTransaction,
	correlationID, causationID string,
	now time.Time,
	insert bool,
) error {
	var direction domain.LedgerDirection
	var before, after domain.Money
	movement := transaction.Kind() != domain.KindLoss
	var movementErr error
	switch transaction.Kind() {
	case domain.KindBet:
		direction = domain.DirectionDebit
		before, after, movementErr = wallet.Debit(transaction.Money(), now)
	case domain.KindWin, domain.KindRefund:
		direction = domain.DirectionCredit
		before, after, movementErr = wallet.Credit(transaction.Money(), now)
	case domain.KindRollback:
		if reference == nil {
			return domain.ErrReferenceNotProcessed
		}
		if reference.Kind() == domain.KindBet {
			direction = domain.DirectionCredit
			before, after, movementErr = wallet.Credit(transaction.Money(), now)
		} else {
			direction = domain.DirectionDebit
			before, after, movementErr = wallet.Debit(transaction.Money(), now)
		}
	case domain.KindLoss:
		before, after = wallet.Balance(), wallet.Balance()
	default:
		return domain.ErrInvalidTransaction
	}
	if movementErr != nil {
		if errors.Is(movementErr, domain.ErrInsufficientFunds) {
			code := domain.FailureInsufficientFunds
			if transaction.Kind() == domain.KindRollback {
				code = domain.FailureInsufficientFundsReversal
			}
			return s.reject(ctx, tx, transaction, wallet, code, correlationID, causationID, now, insert)
		}
		return movementErr
	}
	if err := transaction.MarkProcessed(after, now); err != nil {
		return err
	}
	if insert {
		if err := tx.InsertTransaction(ctx, transaction); err != nil {
			return err
		}
	} else if err := tx.UpdateTransaction(ctx, transaction); err != nil {
		return err
	}
	if movement {
		if err := tx.UpdateWallet(ctx, wallet); err != nil {
			return err
		}
		entry, err := domain.NewWalletLedgerEntry(
			uuid.NewString(),
			wallet.ID(),
			transaction.ID(),
			direction,
			transaction.Money(),
			before,
			after,
			now,
		)
		if err != nil {
			return err
		}
		if err := tx.InsertLedger(ctx, entry); err != nil {
			return err
		}
	}
	processed, err := newEvent(
		EventWagerTransactionProcessed,
		transaction.ID(),
		eventMetadata{
			CorrelationID: correlationID,
			CausationID:   causationID,
			OccurredAt:    now,
		},
		WagerTransactionProcessedData{
			TransactionID:          transaction.ID(),
			ExternalTransactionID:  transaction.ExternalID(),
			ProviderID:             transaction.ProviderID(),
			WalletID:               wallet.ID(),
			PlayerID:               transaction.PlayerID(),
			RoundID:                transaction.RoundID(),
			GameID:                 transaction.GameID(),
			Kind:                   transaction.Kind(),
			Money:                  ViewMoney(transaction.Money()),
			Status:                 transaction.Status(),
			Balance:                ViewMoney(after),
			ReferenceTransactionID: transaction.ReferenceTransactionID(),
			ReferenceExternalID:    transaction.ReferenceExternalID(),
		},
	)
	if err != nil {
		return err
	}
	if err := tx.InsertOutbox(ctx, processed); err != nil {
		return err
	}
	if movement {
		changed, err := newEvent(
			EventWalletBalanceChanged,
			wallet.ID(),
			eventMetadata{
				CorrelationID: correlationID,
				CausationID:   transaction.ID(),
				OccurredAt:    now,
			},
			WalletBalanceChangedData{
				WalletID:      wallet.ID(),
				TransactionID: transaction.ID(),
				Direction:     direction,
				Money:         ViewMoney(transaction.Money()),
				BalanceBefore: ViewMoney(before),
				BalanceAfter:  ViewMoney(after),
				WalletVersion: wallet.Version(),
			},
		)
		if err != nil {
			return err
		}
		return tx.InsertOutbox(ctx, changed)
	}
	return nil
}

func (s *WagerService) reject(
	ctx context.Context,
	tx TxStore,
	transaction *domain.WagerTransaction,
	wallet *domain.Wallet,
	code, correlationID, causationID string,
	now time.Time,
	insert bool,
) error {
	if err := transaction.Reject(code, wallet.Balance(), now); err != nil {
		return err
	}
	if insert {
		if err := tx.InsertTransaction(ctx, transaction); err != nil {
			return err
		}
	} else if err := tx.UpdateTransaction(ctx, transaction); err != nil {
		return err
	}
	event, err := newEvent(
		EventWagerTransactionRejected,
		transaction.ID(),
		eventMetadata{
			CorrelationID: correlationID,
			CausationID:   causationID,
			OccurredAt:    now,
		},
		WagerTransactionRejectedData{
			TransactionID:         transaction.ID(),
			ExternalTransactionID: transaction.ExternalID(),
			ProviderID:            transaction.ProviderID(),
			WalletID:              wallet.ID(),
			Kind:                  transaction.Kind(),
			Status:                transaction.Status(),
			FailureCode:           code,
			Balance:               ViewMoney(wallet.Balance()),
		},
	)
	if err != nil {
		return err
	}
	return tx.InsertOutbox(ctx, event)
}

func (s *WagerService) pendingEvent(
	ctx context.Context,
	tx TxStore,
	transaction *domain.WagerTransaction,
	correlationID, causationID string,
	now time.Time,
) error {
	event, err := newEvent(
		EventWagerTransactionPendingReference,
		transaction.ID(),
		eventMetadata{
			CorrelationID: correlationID,
			CausationID:   causationID,
			OccurredAt:    now,
		},
		WagerTransactionPendingReferenceData{
			TransactionID:                  transaction.ID(),
			ExternalTransactionID:          transaction.ExternalID(),
			ProviderID:                     transaction.ProviderID(),
			WalletID:                       transaction.WalletID(),
			ReferenceExternalTransactionID: transaction.ReferenceExternalID(),
			Status:                         transaction.Status(),
			NextAttemptAt:                  transaction.NextAttemptAt(),
		},
	)
	if err != nil {
		return err
	}
	return tx.InsertOutbox(ctx, event)
}

func (s *WagerService) referenceFailure(
	ctx context.Context,
	tx TxStore,
	transaction, reference *domain.WagerTransaction,
) (string, error) {
	if reference == nil {
		if transaction.Kind() == domain.KindRefund || transaction.Kind() == domain.KindRollback {
			return domain.FailureReferenceNotFound, nil
		}
		return "", nil
	}
	if reference.ProviderID() != transaction.ProviderID() || reference.PlayerID() != transaction.PlayerID() ||
		reference.WalletID() != transaction.WalletID() || reference.RoundID() != transaction.RoundID() ||
		reference.Money().Currency() != transaction.Money().Currency() {
		return domain.FailureReferenceMismatch, nil
	}
	if (transaction.Kind() == domain.KindRefund || transaction.Kind() == domain.KindRollback) &&
		!reference.Money().Equal(transaction.Money()) {
		return domain.FailureReferenceAmountMismatch, nil
	}
	switch transaction.Kind() {
	case domain.KindWin:
		if reference.Kind() != domain.KindBet {
			return domain.FailureInvalidReferenceKind, nil
		}
	case domain.KindRefund:
		if reference.Kind() != domain.KindBet {
			return domain.FailureInvalidReferenceKind, nil
		}
	case domain.KindRollback:
		if reference.Kind() != domain.KindBet && reference.Kind() != domain.KindWin && reference.Kind() != domain.KindRefund {
			return domain.FailureInvalidReferenceKind, nil
		}
	}
	if transaction.Kind() == domain.KindRefund || transaction.Kind() == domain.KindRollback {
		exists, err := tx.SuccessfulReversalExists(ctx, reference.ID())
		if err != nil {
			return "", err
		}
		if exists {
			return domain.FailureReferenceAlreadyReversed, nil
		}
	}
	return "", nil
}

func (s *WagerService) replayResult(
	ctx context.Context,
	tx TxStore,
	transaction *domain.WagerTransaction,
) (ProcessWagerResult, error) {
	balance, ok := transaction.ResultBalance()
	if !ok {
		wallet, err := tx.LockWallet(ctx, transaction.WalletID())
		if err != nil {
			return ProcessWagerResult{}, err
		}
		balance = wallet.Balance()
	}
	return resultFrom(transaction, balance, true), nil
}

func (s *WagerService) completeInbox(ctx context.Context, tx TxStore, inbox *InboxInput, now time.Time) error {
	if inbox == nil {
		return nil
	}
	return tx.CompleteInbox(ctx, *inbox, now)
}

func (s *WagerService) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := s.policy.BaseBackoff
	for i := 1; i < attempt && delay < s.policy.MaxBackoff; i++ {
		if delay > s.policy.MaxBackoff/2 {
			return s.policy.MaxBackoff
		}
		delay *= 2
	}
	if delay > s.policy.MaxBackoff {
		return s.policy.MaxBackoff
	}
	return delay
}

func resultFrom(transaction *domain.WagerTransaction, balance domain.Money, replay bool) ProcessWagerResult {
	return ProcessWagerResult{
		TransactionID:    transaction.ID(),
		Status:           transaction.Status(),
		Balance:          ViewMoney(balance),
		FailureCode:      transaction.FailureCode(),
		IdempotentReplay: replay,
	}
}

func wagerPayloadHash(command ProcessWagerCommand) (string, error) {
	payload, err := json.Marshal(map[string]string{
		"amount":                         command.Money.Amount(),
		"currency":                       command.Money.Currency(),
		"externalTransactionId":          command.ExternalTransactionID,
		"gameId":                         command.GameID,
		"kind":                           string(command.Kind),
		"playerId":                       command.PlayerID,
		"providerId":                     command.ProviderID,
		"referenceExternalTransactionId": command.ReferenceExternalTransactionID,
		"roundId":                        command.RoundID,
		"walletId":                       command.WalletID,
	})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), nil
}

func referenceIsPending(reference *domain.WagerTransaction) bool {
	return reference == nil || reference.Status() == domain.StatusPending ||
		reference.Status() == domain.StatusPendingReference
}
