-- Copyright 2026 Lingfei Kong <colin404@foxmail.com>
--
-- Drop the legacy AK/SK table after the core Secret cut-over.
--
-- Do not run this in the same release that switches reads. Keep the table for
-- one release as the rollback snapshot; then run:
--   1. hack/secret-migrator --dry-run and verify the core Secrets;
--   2. the new binary with secret reads/writes on core Secrets for one release;
--   3. this script.
--
-- Removing model.SecretM from the GORM registry only stops AutoMigrate from
-- creating/checking the table; it never drops it.

-- PostgreSQL
BEGIN;

CREATE TABLE IF NOT EXISTS agent.secret_backup AS
    SELECT * FROM agent.secret;

-- Sanity check before dropping: the counts must match.
-- SELECT
--     (SELECT count(*) FROM agent.secret)        AS live_rows,
--     (SELECT count(*) FROM agent.secret_backup) AS backup_rows;

DROP TABLE IF EXISTS agent.secret;

COMMIT;

-- MySQL
-- CREATE TABLE IF NOT EXISTS onexai_agent_secret_backup AS
--     SELECT * FROM onexai_agent_secret;
-- DROP TABLE IF EXISTS onexai_agent_secret;
