-- Copyright 2026 Lingfei Kong <colin404@foxmail.com>
--
-- Drop the legacy business-line row table after the Namespace cut-over.
--
-- Run this only after:
--   1. hack/namespace-backfill has been executed and verified;
--   2. the new binary has served reads/writes for at least one release;
--   3. the backup table below has been checked.
--
-- GORM AutoMigrate never drops a table. Removing model.NamespaceM from the
-- registry only stops the binary from creating/checking it, so this explicit
-- script is the only way to reclaim the storage.

-- PostgreSQL
BEGIN;

-- Keep a full copy. The IF NOT EXISTS makes a second run harmless; delete the
-- backup only after the cut-over is considered final.
CREATE TABLE IF NOT EXISTS agent.namespace_backup AS
    SELECT * FROM agent.namespace;

-- Sanity check before dropping: the counts must match.
-- SELECT
--     (SELECT count(*) FROM agent.namespace)        AS live_rows,
--     (SELECT count(*) FROM agent.namespace_backup) AS backup_rows;

DROP TABLE IF EXISTS agent."namespace";

COMMIT;

-- MySQL
-- CREATE TABLE IF NOT EXISTS onexai_agent_namespace_backup AS
--     SELECT * FROM onexai_agent_namespace;
-- DROP TABLE IF EXISTS onexai_agent_namespace;
