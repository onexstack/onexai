-- Drop the two legacy columns agent.agent.sys_prompt and agent.agent.tags
-- (PostgreSQL), after backing them up.
--
-- WHY. sys_prompt was the pre-declarative single-value prompt; the runtime now
-- derives the prompt binding list from the Agent spec, and the model stopped
-- mapping the column. tags was the pre-declarative label set; labels now live on
-- the object's metadata, and the model stopped mapping it too. No current Go
-- model maps either column, so they are dead weight in the table.
--
-- AUTO-MIGRATE WILL NOT DROP THEM. GORM only adds columns; the new binary will
-- neither read nor remove these two. This script is the one-time removal.
--
-- BACKUP FIRST. The local database has 63 agent rows (63 non-empty sys_prompt,
-- 43 non-empty tags). The script copies each column into its own date-stamped
-- table before dropping it and refuses to proceed if the backup row count does
-- not match the live table. An existing backup is not overwritten; the count
-- check is what stops a stale backup from being paired with a newer delete.
--
-- ORDERING: run this after hack/rename-workspace-to-namespace.sql and before
-- starting the new binaries. The other `tags` columns (knowledge_base, mcp,
-- model, prompt, skill) are deliberately untouched -- only agent.agent loses it.
--
-- IDEMPOTENT. Re-running a completed migration is a no-op: the columns are
-- absent, so the backups and the DROP are skipped.

BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'agent' AND table_name = 'agent'
               AND column_name = 'sys_prompt')
  THEN
    IF to_regclass('agent.agent_legacy_sys_prompt_backup_20261106') IS NULL THEN
      EXECUTE 'CREATE TABLE agent.agent_legacy_sys_prompt_backup_20261106 AS
               SELECT name, sys_prompt, updated_at FROM agent.agent';
      RAISE NOTICE 'created agent.agent_legacy_sys_prompt_backup_20261106';
    END IF;
    IF (SELECT count(*) FROM agent.agent_legacy_sys_prompt_backup_20261106)
       <> (SELECT count(*) FROM agent.agent)
    THEN
      RAISE EXCEPTION 'sys_prompt backup row count mismatch; refusing to drop';
    END IF;
  END IF;

  IF EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'agent' AND table_name = 'agent'
               AND column_name = 'tags')
  THEN
    IF to_regclass('agent.agent_legacy_tags_backup_20261106') IS NULL THEN
      EXECUTE 'CREATE TABLE agent.agent_legacy_tags_backup_20261106 AS
               SELECT name, tags, updated_at FROM agent.agent';
      RAISE NOTICE 'created agent.agent_legacy_tags_backup_20261106';
    END IF;
    IF (SELECT count(*) FROM agent.agent_legacy_tags_backup_20261106)
       <> (SELECT count(*) FROM agent.agent)
    THEN
      RAISE EXCEPTION 'tags backup row count mismatch; refusing to drop';
    END IF;
  END IF;
END
$$;

ALTER TABLE agent.agent DROP COLUMN IF EXISTS sys_prompt;
ALTER TABLE agent.agent DROP COLUMN IF EXISTS tags;

-- Visible inside the transaction so a failure here rolls the drops back too.
SELECT count(*) AS agent_rows FROM agent.agent;

COMMIT;
