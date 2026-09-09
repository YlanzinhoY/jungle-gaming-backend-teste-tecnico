ALTER TABLE wager_transactions
    DROP CONSTRAINT wager_wallet_identity_fk;

ALTER TABLE wager_transactions
    ADD CONSTRAINT wager_wallet_fk FOREIGN KEY (wallet_id) REFERENCES wallets(id);

CREATE OR REPLACE FUNCTION enforce_wager_wallet_identity() RETURNS trigger AS $$
BEGIN
    IF NEW.status <> 'REJECTED' AND NOT EXISTS (
        SELECT 1 FROM wallets
        WHERE id = NEW.wallet_id AND player_id = NEW.player_id AND currency = NEW.currency
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23503',
            CONSTRAINT = 'wager_wallet_identity_guard',
            MESSAGE = 'active wager transaction does not match wallet identity and currency';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER wager_wallet_identity_guard
    BEFORE INSERT OR UPDATE OF wallet_id, player_id, currency, status ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION enforce_wager_wallet_identity();
