DROP TRIGGER IF EXISTS outbox_payload_no_update ON outbox_events;
DROP FUNCTION IF EXISTS prevent_outbox_payload_mutation();
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS inbox_messages;
DROP TRIGGER IF EXISTS wallet_ledger_no_update ON wallet_ledger_entries;
DROP FUNCTION IF EXISTS prevent_ledger_mutation();
DROP TABLE IF EXISTS wallet_ledger_entries;
DROP TABLE IF EXISTS wager_transactions;
DROP TABLE IF EXISTS wallets;
