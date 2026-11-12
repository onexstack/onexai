-- Drop agent.namespace.status (PostgreSQL), after backing it up.
--
-- WHY. A business line used to carry an enabled | disabled status, editable in
-- the console and mirrored to the Namespace's onexai.io/status annotation. The
-- field is gone from the API and the model now: creating a business line
-- creates an enabled one, and there is no way to disable it. No current Go
-- model maps the column, so it is dead weight in the table.
--
-- AUTO-MIGRATE WILL NOT DROP IT. GORM only adds columns; the new binary will
-- neither read nor remove this one. This script is the one-time removal.
--
-- BACKUP FIRST. The script copies the column into its own date-stamped table
-- before dropping it and refuses to proceed if the backup row count does not
-- match the live table. An existing backup is not overwritten; the count check
-- is what stops a stale backup from being paired with a newer delete.
--
-- IDEMPOTENT. Re-running a completed migration is a no-op: the column is
-- absent, so the backup and the DROP are skipped.

BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'agent' AND table_name = 'namespace'
               AND column_name = 'status')
  THEN
    IF to_regclass('agent.namespace_status_backup_20261106') IS NULL THEN
      EXECUTE 'CREATE TABLE agent.namespace_status_backup_20261106 AS
               SELECT name, status, updated_at FROM agent."namespace"';
      RAISE NOTICE 'created agent.namespace_status_backup_20261106';
    END IF;
    IF (SELECT count(*) FROM agent.namespace_status_backup_20261106)
       <> (SELECT count(*) FROM agent."namespace")
    THEN
      RAISE EXCEPTION 'status backup row count mismatch; refusing to drop';
    END IF;
  END IF;
END
$$;

ALTER TABLE agent."namespace" DROP COLUMN IF EXISTS status;

-- Visible inside the transaction so a failure here rolls the drop back too.
SELECT count(*) AS namespace_rows FROM agent."namespace";

COMMIT;
