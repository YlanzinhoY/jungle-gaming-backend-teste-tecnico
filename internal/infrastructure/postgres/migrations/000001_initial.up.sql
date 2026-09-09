-- This migration owns schema only. It must never create domain records: the
-- manual environment starts empty and E2E fixtures exist only in Testcontainers.
CREATE TABLE IF NOT EXISTS wallets (
    id uuid PRIMARY KEY,
    player_id uuid NOT NULL,
    currency varchar(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance_minor bigint NOT NULL CHECK (balance_minor >= 0),
    version bigint NOT NULL CHECK (version >= 1),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (player_id, currency),
    UNIQUE (id, player_id, currency),
    UNIQUE (id, currency)
);

CREATE TABLE IF NOT EXISTS wager_transactions (
    id uuid PRIMARY KEY,
    origin varchar(8) NOT NULL CHECK (origin IN ('INTERNAL', 'EXTERNAL')),
    provider_id text,
    external_transaction_id text,
    idempotency_key text,
    payload_hash char(64),
    wallet_id uuid NOT NULL,
    player_id uuid NOT NULL,
    round_id text,
    game_id text,
    kind varchar(10) NOT NULL CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    money_minor bigint NOT NULL CHECK (money_minor >= 0),
    currency varchar(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    reference_external_transaction_id text,
    reference_transaction_id uuid REFERENCES wager_transactions(id),
    status varchar(20) NOT NULL CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    failure_code text,
    result_balance_minor bigint CHECK (result_balance_minor >= 0),
    result_currency varchar(3) CHECK (result_currency IS NULL OR result_currency ~ '^[A-Z]{3}$'),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT wager_origin_fields CHECK (
        (origin = 'INTERNAL' AND kind = 'OPENING' AND provider_id IS NULL AND external_transaction_id IS NULL
            AND idempotency_key IS NULL AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL
            AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL)
        OR
        (origin = 'EXTERNAL' AND kind <> 'OPENING' AND provider_id IS NOT NULL AND external_transaction_id IS NOT NULL
            AND idempotency_key IS NOT NULL AND payload_hash IS NOT NULL AND round_id IS NOT NULL AND game_id IS NOT NULL)
    ),
    CONSTRAINT wager_amount_policy CHECK (
        (kind = 'LOSS' AND money_minor = 0) OR (kind <> 'LOSS' AND money_minor > 0)
    ),
    CONSTRAINT wager_reference_policy CHECK (
        (kind IN ('REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NOT NULL)
        OR (kind = 'WIN')
        OR (kind NOT IN ('WIN', 'REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL)
    ),
    CONSTRAINT wager_result_policy CHECK (
        (status IN ('PROCESSED', 'REJECTED', 'FAILED') AND result_balance_minor IS NOT NULL AND result_currency IS NOT NULL)
        OR (status IN ('PENDING', 'PENDING_REFERENCE') AND result_balance_minor IS NULL AND result_currency IS NULL)
    ),
    CONSTRAINT wager_failure_policy CHECK (
        (status IN ('REJECTED', 'FAILED') AND failure_code IS NOT NULL)
        OR (status NOT IN ('REJECTED', 'FAILED') AND failure_code IS NULL)
    ),
    CONSTRAINT wager_wallet_identity_fk FOREIGN KEY (wallet_id, player_id, currency)
        REFERENCES wallets(id, player_id, currency)
);

CREATE UNIQUE INDEX wager_provider_idempotency_uq
    ON wager_transactions(provider_id, idempotency_key) WHERE origin = 'EXTERNAL';
CREATE UNIQUE INDEX wager_provider_external_uq
    ON wager_transactions(provider_id, external_transaction_id) WHERE origin = 'EXTERNAL';
CREATE UNIQUE INDEX wager_opening_wallet_uq
    ON wager_transactions(wallet_id) WHERE origin = 'INTERNAL' AND kind = 'OPENING';
CREATE UNIQUE INDEX wager_successful_reversal_uq
    ON wager_transactions(reference_transaction_id)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');
CREATE INDEX wager_pending_reference_idx
    ON wager_transactions(next_attempt_at, id) WHERE status = 'PENDING_REFERENCE';

CREATE TABLE IF NOT EXISTS wallet_ledger_entries (
    id uuid PRIMARY KEY,
    wallet_id uuid NOT NULL,
    transaction_id uuid NOT NULL REFERENCES wager_transactions(id),
    direction varchar(6) NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    money_minor bigint NOT NULL CHECK (money_minor > 0),
    currency varchar(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance_before_minor bigint NOT NULL CHECK (balance_before_minor >= 0),
    balance_after_minor bigint NOT NULL CHECK (balance_after_minor >= 0),
    created_at timestamptz NOT NULL,
    UNIQUE (wallet_id, transaction_id),
    CONSTRAINT ledger_wallet_currency_fk FOREIGN KEY (wallet_id, currency) REFERENCES wallets(id, currency),
    CONSTRAINT ledger_arithmetic CHECK (
        (direction = 'DEBIT' AND balance_after_minor = balance_before_minor - money_minor)
        OR (direction = 'CREDIT' AND balance_after_minor = balance_before_minor + money_minor)
    )
);
CREATE INDEX wallet_ledger_page_idx ON wallet_ledger_entries(wallet_id, created_at DESC, id DESC);

CREATE OR REPLACE FUNCTION prevent_ledger_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'wallet ledger is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER wallet_ledger_no_update
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION prevent_ledger_mutation();

CREATE TABLE IF NOT EXISTS inbox_messages (
    consumer_name text NOT NULL,
    message_id text NOT NULL,
    payload_hash char(64) NOT NULL,
    received_at timestamptz NOT NULL,
    completed_at timestamptz,
    PRIMARY KEY (consumer_name, message_id)
);

CREATE TABLE IF NOT EXISTS outbox_events (
    event_id uuid PRIMARY KEY,
    aggregate_id text NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL,
    locked_by text,
    locked_until timestamptz,
    published_at timestamptz,
    last_error text
);
CREATE INDEX outbox_pending_idx ON outbox_events(next_attempt_at, occurred_at)
    WHERE published_at IS NULL;

CREATE OR REPLACE FUNCTION prevent_outbox_payload_mutation() RETURNS trigger AS $$
BEGIN
    IF (
        NEW.event_id <> OLD.event_id OR NEW.aggregate_id <> OLD.aggregate_id OR
        NEW.event_type <> OLD.event_type OR NEW.payload <> OLD.payload OR NEW.occurred_at <> OLD.occurred_at
    ) THEN
        RAISE EXCEPTION 'outbox event payload is immutable';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER outbox_payload_no_update
    BEFORE UPDATE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION prevent_outbox_payload_mutation();
