package application

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/enzom/jungle-gaming/internal/domain"
)

type WalletService struct {
	store    Store
	validate Validator
	metrics  Metrics
	log      *slog.Logger
}

func NewWalletService(
	store Store,
	validate Validator,
	metrics Metrics,
	log *slog.Logger,
) *WalletService {
	return &WalletService{store: store, validate: validate, metrics: metrics, log: log}
}

func (s *WalletService) Create(ctx context.Context, command CreateWalletCommand) (result WalletView, err error) {
	ctx, span := otel.Tracer("github.com/enzom/jungle-gaming/application").Start(ctx, "WalletService.Create")
	span.SetAttributes(
		attribute.String("app.player_id", command.PlayerID),
		attribute.String("app.correlation_id", command.CorrelationID),
	)
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "wallet creation failed")
		} else {
			span.SetAttributes(attribute.String("app.wallet_id", result.ID))
		}
		span.End()
	}()

	if err := s.validate.Struct(command); err != nil {
		return WalletView{}, fmt.Errorf("%w: %v", domain.ErrInvalidWallet, err)
	}
	if command.InitialBalance.IsNegative() {
		return WalletView{}, fmt.Errorf("%w: initial balance cannot be negative", domain.ErrInvalidWallet)
	}
	now := time.Now().UTC()
	wallet, err := domain.NewWallet(uuid.NewString(), command.PlayerID, command.InitialBalance, now)
	if err != nil {
		return WalletView{}, err
	}
	correlationID := command.CorrelationID
	if correlationID == "" {
		correlationID = uuid.NewString()
	}

	err = s.store.WithinTransaction(ctx, func(tx TxStore) error {
		if err := tx.InsertWallet(ctx, wallet); err != nil {
			return err
		}
		if command.InitialBalance.IsZero() {
			return nil
		}

		transactionID := uuid.NewString()
		if err := tx.InsertOpening(ctx, transactionID, wallet, command.InitialBalance, now); err != nil {
			return err
		}
		zero, err := domain.Zero(command.InitialBalance.Currency())
		if err != nil {
			return err
		}
		entry, err := domain.NewWalletLedgerEntry(
			uuid.NewString(),
			wallet.ID(),
			transactionID,
			domain.DirectionCredit,
			command.InitialBalance,
			zero,
			command.InitialBalance,
			now,
		)
		if err != nil {
			return err
		}
		if err := tx.InsertLedger(ctx, entry); err != nil {
			return err
		}

		processed, err := newEvent(
			EventWagerTransactionProcessed,
			transactionID,
			eventMetadata{CorrelationID: correlationID, OccurredAt: now},
			WagerTransactionProcessedData{
				TransactionID: transactionID,
				WalletID:      wallet.ID(),
				PlayerID:      wallet.PlayerID(),
				Kind:          domain.KindOpening,
				Money:         ViewMoney(command.InitialBalance),
				Status:        domain.StatusProcessed,
				Balance:       ViewMoney(command.InitialBalance),
			},
		)
		if err != nil {
			return err
		}
		changed, err := newEvent(
			EventWalletBalanceChanged,
			wallet.ID(),
			eventMetadata{
				CorrelationID: correlationID,
				CausationID:   transactionID,
				OccurredAt:    now,
			},
			WalletBalanceChangedData{
				WalletID:      wallet.ID(),
				TransactionID: transactionID,
				Direction:     domain.DirectionCredit,
				Money:         ViewMoney(command.InitialBalance),
				BalanceBefore: ViewMoney(zero),
				BalanceAfter:  ViewMoney(command.InitialBalance),
				WalletVersion: wallet.Version(),
			},
		)
		if err != nil {
			return err
		}
		if err := tx.InsertOutbox(ctx, processed); err != nil {
			return err
		}
		return tx.InsertOutbox(ctx, changed)
	})
	if err != nil {
		return WalletView{}, err
	}
	s.log.InfoContext(
		ctx,
		"wallet created",
		"walletId", wallet.ID(),
		"playerId", wallet.PlayerID(),
		"correlationId", correlationID,
	)
	return walletView(wallet), nil
}

func (s *WalletService) Get(ctx context.Context, walletID string) (WalletView, error) {
	wallet, err := s.store.GetWallet(ctx, walletID)
	if err != nil {
		return WalletView{}, err
	}
	return walletView(wallet), nil
}

func (s *WalletService) Ledger(ctx context.Context, walletID, cursorValue string, limit int) (LedgerPage, error) {
	if _, err := s.store.GetWallet(ctx, walletID); err != nil {
		return LedgerPage{}, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	var cursor *LedgerCursor
	var err error
	if cursorValue != "" {
		cursor, err = decodeLedgerCursor(cursorValue)
		if err != nil {
			return LedgerPage{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
		}
	}
	records, err := s.store.ListLedger(ctx, walletID, cursor, limit+1)
	if err != nil {
		return LedgerPage{}, err
	}
	page := LedgerPage{Items: make([]LedgerEntryView, 0, min(limit, len(records)))}
	for index, record := range records {
		if index == limit {
			last := records[index-1]
			page.NextCursor = encodeLedgerCursor(LedgerCursor{CreatedAt: last.CreatedAt, ID: last.ID})
			break
		}
		page.Items = append(page.Items, LedgerEntryView{
			ID: record.ID, TransactionID: record.TransactionID, Direction: record.Direction, Money: ViewMoney(record.Money),
			BalanceBefore: ViewMoney(record.Before), BalanceAfter: ViewMoney(record.After), CreatedAt: record.CreatedAt,
		})
	}
	return page, nil
}

func (s *WalletService) Reconcile(ctx context.Context, walletID string) (ReconciliationView, error) {
	var result ReconciliationView
	err := s.store.WithinReadOnlyTransaction(ctx, func(tx TxStore) error {
		wallet, err := tx.ReadWallet(ctx, walletID)
		if err != nil {
			return err
		}
		credits, debits, count, err := tx.SumLedger(ctx, walletID, wallet.Balance().Currency())
		if err != nil {
			return err
		}
		calculatedMinor := credits - debits
		calculated, err := domain.NewMoneyFromMinor(calculatedMinor, wallet.Balance().Currency())
		if err != nil {
			return err
		}
		difference, err := wallet.Balance().Subtract(calculated)
		if err != nil {
			return err
		}
		result = ReconciliationView{
			WalletID: wallet.ID(), StoredBalance: ViewMoney(wallet.Balance()), CalculatedBalance: ViewMoney(calculated),
			Difference: ViewMoney(difference), Consistent: difference.IsZero(), CheckedEntries: count,
		}
		return nil
	})
	if err != nil {
		return ReconciliationView{}, err
	}
	if !result.Consistent {
		s.metrics.IncReconciliationDivergence()
		s.log.ErrorContext(
			ctx,
			"wallet reconciliation divergence",
			"walletId", walletID,
			"difference", result.Difference.Amount,
		)
	}
	return result, nil
}

func walletView(wallet *domain.Wallet) WalletView {
	return WalletView{
		ID:        wallet.ID(),
		PlayerID:  wallet.PlayerID(),
		Balance:   ViewMoney(wallet.Balance()),
		Version:   wallet.Version(),
		CreatedAt: wallet.CreatedAt(),
		UpdatedAt: wallet.UpdatedAt(),
	}
}
