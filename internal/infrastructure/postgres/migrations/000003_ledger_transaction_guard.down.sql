ALTER TABLE wager_transactions
    DROP CONSTRAINT IF EXISTS wager_result_currency_guard;

DROP TRIGGER IF EXISTS ledger_transaction_guard ON wallet_ledger_entries;
DROP FUNCTION IF EXISTS enforce_ledger_transaction();
