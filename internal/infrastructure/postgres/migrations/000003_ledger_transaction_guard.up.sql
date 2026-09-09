CREATE OR REPLACE FUNCTION enforce_ledger_transaction() RETURNS trigger AS $$
DECLARE
    transaction_status text;
    transaction_kind text;
    transaction_wallet uuid;
    transaction_currency text;
    transaction_money bigint;
    transaction_result_balance bigint;
    reference_id uuid;
    reference_kind text;
    expected_direction text;
BEGIN
    SELECT status, kind, wallet_id, currency, money_minor, result_balance_minor, reference_transaction_id
    INTO STRICT transaction_status, transaction_kind, transaction_wallet, transaction_currency,
        transaction_money, transaction_result_balance, reference_id
    FROM wager_transactions
    WHERE id = NEW.transaction_id;

    IF transaction_status <> 'PROCESSED' OR transaction_kind = 'LOSS' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', CONSTRAINT = 'ledger_processed_transaction_guard',
            MESSAGE = 'ledger requires a processed transaction with financial movement';
    END IF;

    IF NEW.wallet_id <> transaction_wallet OR NEW.currency <> transaction_currency
        OR NEW.money_minor <> transaction_money OR NEW.balance_after_minor <> transaction_result_balance THEN
        RAISE EXCEPTION USING ERRCODE = '23514', CONSTRAINT = 'ledger_transaction_values_guard',
            MESSAGE = 'ledger values do not match their transaction';
    END IF;

    expected_direction := CASE transaction_kind
        WHEN 'BET' THEN 'DEBIT'
        WHEN 'OPENING' THEN 'CREDIT'
        WHEN 'WIN' THEN 'CREDIT'
        WHEN 'REFUND' THEN 'CREDIT'
        WHEN 'ROLLBACK' THEN NULL
    END;

    IF transaction_kind = 'ROLLBACK' THEN
        SELECT kind INTO STRICT reference_kind
        FROM wager_transactions
        WHERE id = reference_id;
        expected_direction := CASE WHEN reference_kind = 'BET' THEN 'CREDIT' ELSE 'DEBIT' END;
    END IF;

    IF NEW.direction <> expected_direction THEN
        RAISE EXCEPTION USING ERRCODE = '23514', CONSTRAINT = 'ledger_transaction_direction_guard',
            MESSAGE = 'ledger direction does not match transaction kind';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER ledger_transaction_guard
    BEFORE INSERT ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION enforce_ledger_transaction();

ALTER TABLE wager_transactions
    ADD CONSTRAINT wager_result_currency_guard CHECK (
        result_currency IS NULL OR status = 'REJECTED' OR result_currency = currency
    );
