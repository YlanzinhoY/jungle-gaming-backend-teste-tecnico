package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/enzom/jungle-gaming/internal/application"
	"github.com/enzom/jungle-gaming/internal/domain"
)

type Store struct{ pool *pgxpool.Pool }
type txStore struct{ tx pgx.Tx }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) WithinTransaction(ctx context.Context, fn func(application.TxStore) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := fn(&txStore{tx: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) WithinReadOnlyTransaction(ctx context.Context, fn func(application.TxStore) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := fn(&txStore{tx: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) GetWallet(ctx context.Context, id string) (*domain.Wallet, error) {
	wallet, err := scanWallet(s.pool.QueryRow(ctx, walletSelect+` WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, application.ErrNotFound
	}
	return wallet, err
}

func (s *Store) GetTransaction(ctx context.Context, id string) (*domain.WagerTransaction, error) {
	transaction, err := scanTransaction(s.pool.QueryRow(ctx, transactionSelect+` WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, application.ErrNotFound
	}
	return transaction, err
}

func (s *Store) GetProviderTransaction(ctx context.Context, providerID, externalID string) (*domain.WagerTransaction, error) {
	transaction, err := scanTransaction(s.pool.QueryRow(ctx, transactionSelect+` WHERE provider_id = $1 AND external_transaction_id = $2`, providerID, externalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, application.ErrNotFound
	}
	return transaction, err
}

func (s *Store) ListLedger(ctx context.Context, walletID string, cursor *application.LedgerCursor, limit int) ([]application.LedgerRecord, error) {
	query := `SELECT id::text, transaction_id::text, direction, money_minor, currency, balance_before_minor, balance_after_minor, created_at
        FROM wallet_ledger_entries WHERE wallet_id = $1`
	args := []any{walletID}
	if cursor != nil {
		query += ` AND (created_at, id) < ($2, $3::uuid)`
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	args = append(args, limit)
	query += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]application.LedgerRecord, 0, limit)
	for rows.Next() {
		var record application.LedgerRecord
		var direction string
		var minor, beforeMinor, afterMinor int64
		var currency string
		if err := rows.Scan(&record.ID, &record.TransactionID, &direction, &minor, &currency, &beforeMinor, &afterMinor, &record.CreatedAt); err != nil {
			return nil, err
		}
		record.Direction = domain.LedgerDirection(direction)
		record.Money, err = domain.NewMoneyFromMinor(minor, currency)
		if err != nil {
			return nil, err
		}
		record.Before, err = domain.NewMoneyFromMinor(beforeMinor, currency)
		if err != nil {
			return nil, err
		}
		record.After, err = domain.NewMoneyFromMinor(afterMinor, currency)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) FindDuePendingReferences(ctx context.Context, limit int, now time.Time) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM wager_transactions
        WHERE status = 'PENDING_REFERENCE' AND next_attempt_at <= $1 ORDER BY next_attempt_at, id LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) ClaimOutbox(ctx context.Context, workerID string, limit int, lease time.Duration, now time.Time) ([]application.ClaimedOutboxEvent, error) {
	rows, err := s.pool.Query(ctx, `WITH candidates AS (
            SELECT event_id FROM outbox_events
            WHERE published_at IS NULL AND next_attempt_at <= $1 AND (locked_until IS NULL OR locked_until < $1)
            ORDER BY occurred_at, event_id FOR UPDATE SKIP LOCKED LIMIT $2
        )
        UPDATE outbox_events o SET locked_by = $3, locked_until = $1 + $4::interval
        FROM candidates c WHERE o.event_id = c.event_id
        RETURNING o.event_id::text, o.aggregate_id, o.event_type, o.payload, o.occurred_at, o.attempts`,
		now, limit, workerID, lease.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]application.ClaimedOutboxEvent, 0, limit)
	for rows.Next() {
		var event application.ClaimedOutboxEvent
		if err := rows.Scan(&event.EventID, &event.AggregateID, &event.EventType, &event.Payload, &event.OccurredAt, &event.Attempts); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) MarkOutboxPublished(ctx context.Context, eventID, workerID string, now time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE outbox_events SET published_at = $3, locked_by = NULL, locked_until = NULL, last_error = NULL
        WHERE event_id = $1 AND locked_by = $2 AND published_at IS NULL`, eventID, workerID, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return application.ErrConflict
	}
	return nil
}

func (s *Store) ReleaseOutbox(ctx context.Context, eventID, workerID string, next time.Time, reason string) error {
	_, err := s.pool.Exec(ctx, `UPDATE outbox_events SET attempts = attempts + 1, next_attempt_at = $3,
        locked_by = NULL, locked_until = NULL, last_error = left($4, 1000)
        WHERE event_id = $1 AND locked_by = $2 AND published_at IS NULL`, eventID, workerID, next, reason)
	return err
}

func (t *txStore) AcquireIdempotencyLock(ctx context.Context, providerID, key string) error {
	payload := make([]byte, 0, len(providerID)+len(key)+1)
	payload = append(payload, providerID...)
	payload = append(payload, 0)
	payload = append(payload, key...)
	digest := sha256.Sum256(payload)
	lockKey := int64(binary.BigEndian.Uint64(digest[:8]))
	_, err := t.tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey)
	return err
}

func (t *txStore) ClaimInbox(ctx context.Context, input application.InboxInput, now time.Time) (application.InboxDisposition, error) {
	var inserted int
	err := t.tx.QueryRow(ctx, `INSERT INTO inbox_messages(consumer_name, message_id, payload_hash, received_at)
        VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING RETURNING 1`, input.ConsumerName, input.MessageID, input.PayloadHash, now).Scan(&inserted)
	if err == nil {
		return application.InboxInserted, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	var existingHash string
	var completedAt pgtype.Timestamptz
	if err := t.tx.QueryRow(ctx, `SELECT payload_hash, completed_at FROM inbox_messages
        WHERE consumer_name = $1 AND message_id = $2 FOR UPDATE`, input.ConsumerName, input.MessageID).Scan(&existingHash, &completedAt); err != nil {
		return 0, err
	}
	if existingHash != input.PayloadHash {
		return 0, application.ErrInboxPayloadConflict
	}
	if completedAt.Valid {
		return application.InboxAlreadyCompleted, nil
	}
	return application.InboxInserted, nil
}

func (t *txStore) CompleteInbox(ctx context.Context, input application.InboxInput, now time.Time) error {
	tag, err := t.tx.Exec(ctx, `UPDATE inbox_messages SET completed_at = $4
        WHERE consumer_name = $1 AND message_id = $2 AND payload_hash = $3`, input.ConsumerName, input.MessageID, input.PayloadHash, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return application.ErrInboxPayloadConflict
	}
	return nil
}

func (t *txStore) InsertWallet(ctx context.Context, wallet *domain.Wallet) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO wallets(id, player_id, currency, balance_minor, version, created_at, updated_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7)`, wallet.ID(), wallet.PlayerID(), wallet.Balance().Currency(), wallet.Balance().Minor(), wallet.Version(), wallet.CreatedAt(), wallet.UpdatedAt())
	return translateWriteError(err)
}

func (t *txStore) ReadWallet(ctx context.Context, id string) (*domain.Wallet, error) {
	wallet, err := scanWallet(t.tx.QueryRow(ctx, walletSelect+` WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, application.ErrNotFound
	}
	return wallet, err
}

func (t *txStore) LockWallet(ctx context.Context, id string) (*domain.Wallet, error) {
	wallet, err := scanWallet(t.tx.QueryRow(ctx, walletSelect+` WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, application.ErrNotFound
	}
	return wallet, err
}

func (t *txStore) UpdateWallet(ctx context.Context, wallet *domain.Wallet) error {
	tag, err := t.tx.Exec(ctx, `UPDATE wallets SET balance_minor = $2, version = $3, updated_at = $4
        WHERE id = $1 AND version = $3 - 1`, wallet.ID(), wallet.Balance().Minor(), wallet.Version(), wallet.UpdatedAt())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return application.ErrConflict
	}
	return nil
}

func (t *txStore) InsertOpening(ctx context.Context, transactionID string, wallet *domain.Wallet, amount domain.Money, now time.Time) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO wager_transactions(
        id, origin, wallet_id, player_id, kind, money_minor, currency, status,
        result_balance_minor, result_currency, created_at, updated_at)
        VALUES ($1, 'INTERNAL', $2, $3, 'OPENING', $4, $5, 'PROCESSED', $4, $5, $6, $6)`,
		transactionID, wallet.ID(), wallet.PlayerID(), amount.Minor(), amount.Currency(), now)
	return translateWriteError(err)
}

func (t *txStore) FindByIdempotency(ctx context.Context, providerID, key string) (*domain.WagerTransaction, error) {
	transaction, err := scanTransaction(t.tx.QueryRow(ctx, transactionSelect+` WHERE provider_id = $1 AND idempotency_key = $2`, providerID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return transaction, err
}

func (t *txStore) FindByExternalID(ctx context.Context, providerID, externalID string) (*domain.WagerTransaction, error) {
	transaction, err := scanTransaction(t.tx.QueryRow(ctx, transactionSelect+` WHERE provider_id = $1 AND external_transaction_id = $2`, providerID, externalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return transaction, err
}

func (t *txStore) LockTransaction(ctx context.Context, id string) (*domain.WagerTransaction, error) {
	transaction, err := scanTransaction(t.tx.QueryRow(ctx, transactionSelect+` WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, application.ErrNotFound
	}
	return transaction, err
}

func (t *txStore) InsertTransaction(ctx context.Context, transaction *domain.WagerTransaction) error {
	resultMinor, resultCurrency := resultValues(transaction)
	_, err := t.tx.Exec(ctx, `INSERT INTO wager_transactions(
        id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
        wallet_id, player_id, round_id, game_id, kind, money_minor, currency,
        reference_external_transaction_id, reference_transaction_id, status, failure_code,
        result_balance_minor, result_currency, attempt_count, next_attempt_at, created_at, updated_at)
        VALUES ($1, 'EXTERNAL', $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULLIF($13,''), NULLIF($14,'')::uuid,
            $15, NULLIF($16,''), $17, $18, $19, $20, $21, $22)`,
		transaction.ID(), transaction.ProviderID(), transaction.ExternalID(), transaction.IdempotencyKey(), transaction.PayloadHash(),
		transaction.WalletID(), transaction.PlayerID(), transaction.RoundID(), transaction.GameID(), transaction.Kind(), transaction.Money().Minor(),
		transaction.Money().Currency(), transaction.ReferenceExternalID(), transaction.ReferenceTransactionID(), transaction.Status(), transaction.FailureCode(),
		resultMinor, resultCurrency, transaction.AttemptCount(), transaction.NextAttemptAt(), transaction.CreatedAt(), transaction.UpdatedAt())
	return translateWriteError(err)
}

func (t *txStore) UpdateTransaction(ctx context.Context, transaction *domain.WagerTransaction) error {
	resultMinor, resultCurrency := resultValues(transaction)
	tag, err := t.tx.Exec(ctx, `UPDATE wager_transactions SET reference_transaction_id = NULLIF($2,'')::uuid,
        status = $3, failure_code = NULLIF($4,''), result_balance_minor = $5, result_currency = $6,
        attempt_count = $7, next_attempt_at = $8, updated_at = $9 WHERE id = $1`,
		transaction.ID(), transaction.ReferenceTransactionID(), transaction.Status(), transaction.FailureCode(), resultMinor,
		resultCurrency, transaction.AttemptCount(), transaction.NextAttemptAt(), transaction.UpdatedAt())
	if err != nil {
		return translateWriteError(err)
	}
	if tag.RowsAffected() != 1 {
		return application.ErrNotFound
	}
	return nil
}

func (t *txStore) SuccessfulReversalExists(ctx context.Context, referenceID string) (bool, error) {
	var exists bool
	err := t.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wager_transactions
        WHERE reference_transaction_id = $1 AND kind IN ('REFUND','ROLLBACK') AND status = 'PROCESSED')`, referenceID).Scan(&exists)
	return exists, err
}

func (t *txStore) InsertLedger(ctx context.Context, entry *domain.WalletLedgerEntry) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO wallet_ledger_entries(
        id, wallet_id, transaction_id, direction, money_minor, currency, balance_before_minor, balance_after_minor, created_at)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, entry.ID(), entry.WalletID(), entry.TransactionID(), entry.Direction(),
		entry.Money().Minor(), entry.Money().Currency(), entry.BalanceBefore().Minor(), entry.BalanceAfter().Minor(), entry.CreatedAt())
	return translateWriteError(err)
}

func (t *txStore) InsertOutbox(ctx context.Context, event application.OutboxEvent) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO outbox_events(event_id, aggregate_id, event_type, payload, occurred_at, next_attempt_at)
        VALUES ($1, $2, $3, $4::jsonb, $5, $5)`, event.EventID, event.AggregateID, event.EventType, event.Payload, event.OccurredAt)
	return translateWriteError(err)
}

func (t *txStore) SumLedger(ctx context.Context, walletID, currency string) (int64, int64, int64, error) {
	var credits, debits, count int64
	err := t.tx.QueryRow(ctx, `SELECT
        COALESCE(SUM(money_minor) FILTER (WHERE direction = 'CREDIT'), 0)::bigint,
        COALESCE(SUM(money_minor) FILTER (WHERE direction = 'DEBIT'), 0)::bigint,
        COUNT(*)::bigint FROM wallet_ledger_entries WHERE wallet_id = $1 AND currency = $2`, walletID, currency).Scan(&credits, &debits, &count)
	return credits, debits, count, err
}

const walletSelect = `SELECT id::text, player_id::text, balance_minor, currency, version, created_at, updated_at FROM wallets`

func scanWallet(row pgx.Row) (*domain.Wallet, error) {
	var id, playerID, currency string
	var minor, version int64
	var createdAt, updatedAt time.Time
	if err := row.Scan(&id, &playerID, &minor, &currency, &version, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	balance, err := domain.NewMoneyFromMinor(minor, currency)
	if err != nil {
		return nil, err
	}
	return domain.RehydrateWallet(id, playerID, balance, version, createdAt, updatedAt)
}

const transactionSelect = `SELECT id::text, COALESCE(provider_id,''), COALESCE(external_transaction_id,''),
    COALESCE(idempotency_key,''), COALESCE(payload_hash,''), wallet_id::text, player_id::text,
    COALESCE(round_id,''), COALESCE(game_id,''), kind, money_minor, currency,
    COALESCE(reference_external_transaction_id,''), COALESCE(reference_transaction_id::text,''), status,
    COALESCE(failure_code,''), result_balance_minor, result_currency, attempt_count, next_attempt_at, created_at, updated_at
    FROM wager_transactions`

func scanTransaction(row pgx.Row) (*domain.WagerTransaction, error) {
	var s domain.RehydratedTransaction
	var kind, status string
	var minor int64
	var currency string
	var resultMinor pgtype.Int8
	var resultCurrency pgtype.Text
	var next pgtype.Timestamptz
	err := row.Scan(&s.ID, &s.ProviderID, &s.ExternalID, &s.IdempotencyKey, &s.PayloadHash, &s.WalletID, &s.PlayerID,
		&s.RoundID, &s.GameID, &kind, &minor, &currency, &s.ReferenceExternalID, &s.ReferenceTransactionID,
		&status, &s.FailureCode, &resultMinor, &resultCurrency, &s.AttemptCount, &next, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	s.Kind = domain.TransactionKind(kind)
	s.Status = domain.TransactionStatus(status)
	s.Money, err = domain.NewMoneyFromMinor(minor, currency)
	if err != nil {
		return nil, err
	}
	if resultMinor.Valid && resultCurrency.Valid {
		s.ResultBalance, err = domain.NewMoneyFromMinor(resultMinor.Int64, resultCurrency.String)
		if err != nil {
			return nil, err
		}
		s.HasResultBalance = true
	}
	if next.Valid {
		value := next.Time
		s.NextAttemptAt = &value
	}
	return domain.RehydrateWagerTransaction(s)
}

func resultValues(transaction *domain.WagerTransaction) (any, any) {
	if result, ok := transaction.ResultBalance(); ok {
		return result.Minor(), result.Currency()
	}
	return nil, nil
}

func translateWriteError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s", application.ErrConflict, pgErr.ConstraintName)
	}
	return err
}

var _ application.Store = (*Store)(nil)
var _ application.TxStore = (*txStore)(nil)
