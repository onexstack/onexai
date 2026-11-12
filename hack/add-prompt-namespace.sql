-- Add agent.prompt.namespace (PostgreSQL), the column a prompt now carries so
-- several business lines can each keep a prompt of the same name.
--
-- WHY. Prompts used to be global: the store wrote every one into the configured
-- (or default) namespace and their rows had no namespace column. The declarative
-- control plane has always supported namespaced Prompt objects, so the row now
-- maps metadata.namespace and the console scopes the list by business line.
--
-- ORDERING. AutoMigrate adds this column and its index on the next
-- oai-agent-apiserver start. This script exists for deployments whose service
-- account cannot run DDL, and as the explicit, auditable record of the change.
-- Running it first is safe: AutoMigrate then sees the column and index and does
-- nothing.
--
-- IDEMPOTENT. Re-running is a no-op.
--
-- READ-ONLY TABLE. agent.prompt is a legacy/history table; new prompts are
-- written to oai-declserver and projected into <mirror schema>.object_prompt.
-- Do not backfill it and do not expect new rows here.
--
-- NOT IN A TRANSACTION: CREATE INDEX CONCURRENTLY cannot run inside one.

SET lock_timeout = '5s';
SET statement_timeout = '120s';

ALTER TABLE agent.prompt
    ADD COLUMN IF NOT EXISTS namespace varchar(128);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_prompt_namespace
    ON agent.prompt (namespace);
