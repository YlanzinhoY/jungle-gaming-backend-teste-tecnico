DROP TRIGGER IF EXISTS processed_transaction_ledger_required ON wager_transactions;
DROP FUNCTION IF EXISTS enforce_processed_transaction_ledger();

DROP TRIGGER IF EXISTS wallet_balance_ledger_consistency ON wallets;
DROP FUNCTION IF EXISTS enforce_wallet_balance_ledger_consistency();
