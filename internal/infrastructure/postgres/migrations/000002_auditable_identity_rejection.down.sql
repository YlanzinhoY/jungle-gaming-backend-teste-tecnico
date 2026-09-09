DROP TRIGGER IF EXISTS wager_wallet_identity_guard ON wager_transactions;
DROP FUNCTION IF EXISTS enforce_wager_wallet_identity();

ALTER TABLE wager_transactions
    DROP CONSTRAINT wager_wallet_fk;

ALTER TABLE wager_transactions
    ADD CONSTRAINT wager_wallet_identity_fk FOREIGN KEY (wallet_id, player_id, currency)
        REFERENCES wallets(id, player_id, currency) NOT VALID;

DO $$
BEGIN
    ALTER TABLE wager_transactions VALIDATE CONSTRAINT wager_wallet_identity_fk;
EXCEPTION WHEN foreign_key_violation THEN
    NULL;
END;
$$;
