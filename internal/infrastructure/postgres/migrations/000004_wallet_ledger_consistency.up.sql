CREATE OR REPLACE FUNCTION enforce_wallet_balance_ledger_consistency() RETURNS trigger AS $$
DECLARE
    ledger_balance bigint;
BEGIN
    -- A transaction can update one wallet more than once. Only the deferred
    -- trigger for its final persisted value performs the aggregate check.
    IF NEW.balance_minor <> (
        SELECT balance_minor FROM wallets WHERE id = NEW.id
    ) THEN
        RETURN NULL;
    END IF;

    SELECT COALESCE(
        SUM(CASE WHEN direction = 'CREDIT' THEN money_minor ELSE -money_minor END),
        0
    )::bigint
    INTO ledger_balance
    FROM wallet_ledger_entries
    WHERE wallet_id = NEW.id;

    IF ledger_balance <> NEW.balance_minor THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            CONSTRAINT = 'wallet_balance_ledger_consistency',
            MESSAGE = 'wallet balance does not match its ledger';
    END IF;

    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER wallet_balance_ledger_consistency
    AFTER INSERT OR UPDATE OF balance_minor ON wallets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION enforce_wallet_balance_ledger_consistency();

CREATE OR REPLACE FUNCTION enforce_processed_transaction_ledger() RETURNS trigger AS $$
DECLARE
    persisted_status text;
    persisted_kind text;
BEGIN
    SELECT status, kind
    INTO persisted_status, persisted_kind
    FROM wager_transactions
    WHERE id = NEW.id;

    IF NOT FOUND OR persisted_status <> 'PROCESSED' OR persisted_kind = 'LOSS' THEN
        RETURN NULL;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM wallet_ledger_entries
        WHERE transaction_id = NEW.id
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            CONSTRAINT = 'processed_transaction_ledger_required',
            MESSAGE = 'processed financial transaction requires one ledger entry';
    END IF;

    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER processed_transaction_ledger_required
    AFTER INSERT OR UPDATE OF status, kind ON wager_transactions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION enforce_processed_transaction_ledger();
