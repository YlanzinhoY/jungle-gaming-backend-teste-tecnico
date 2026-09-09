#!/bin/sh
set -eu

# Databases created by the former in-process migrator kept one row per version
# and an applied_at column. golang-migrate keeps only the latest version and a
# dirty flag. Convert that metadata once; domain tables and financial data are
# not touched. On fresh or already converted databases this is a no-op.
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
ALTER TABLE IF EXISTS schema_migrations
  ADD COLUMN IF NOT EXISTS dirty boolean NOT NULL DEFAULT false;

DO $$
DECLARE
  latest_version bigint;
BEGIN
  IF EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'schema_migrations'
      AND column_name = 'applied_at'
  ) THEN
    SELECT max(version) INTO latest_version FROM schema_migrations;
    DELETE FROM schema_migrations;
    IF latest_version IS NOT NULL THEN
      INSERT INTO schema_migrations(version, dirty) VALUES (latest_version, false);
    END IF;
    ALTER TABLE schema_migrations DROP COLUMN applied_at;
  END IF;
END
$$;
SQL

exec migrate -path=/migrations -database "$DATABASE_URL" "$@"
