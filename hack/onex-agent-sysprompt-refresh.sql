-- HISTORICAL: sys_prompt was dropped from agent.agent on 2026-11-06
-- (hack/drop-agent-legacy-columns.sql). Do not run this script against a
-- migrated database; it is kept only as a record of the old data shape.
--
-- Point the OneX platform's agents at agent.prompt('onex') instead of carrying
-- the prompt inline.
--
-- Why this file exists. onex used to send the course assistant's system prompt
-- as *text*, one full copy per agent row, so changing a sentence meant
-- rebuilding ox-agent-apiserver. It now sends the record's *name* (see
-- onex/internal/agent/biz/v1/agentaccount/constants.go, SysPromptName), and
-- onexai resolves it at run time (internal/agent/biz/v1/agent/resolve.go,
-- agentPrompt: a value matching a prompt record's name resolves to that row's
-- content; anything else is inline text). The code change only governs what the
-- provisioner writes next; this file moves the rows that are already there.
--
-- What it touches — and what it deliberately does not. The predicate names the
-- two kinds of agent OneX provisions: the per-user `onex-u-*` and the shared
-- `onex-x-max-tier`. It matches on the *name*, not on the old text, because the
-- existing rows are in three different states (the current 1690-character text,
-- an older 1529-character one, and empty) and matching on content would miss
-- two of them. Matching on the name also keeps this script no stricter than the
-- provisioner, which overwrites sysPrompt unconditionally on every write.
--
--   onex-ops-console   the console's operations assistant. Another persona, and
--                      a prompt the Go side validates at compile time against
--                      the tool name it must mention; it stays inline on
--                      purpose. The predicate excludes it — do not widen this to
--                      `LIKE 'onex-%'` or that agent's prompt is replaced by the
--                      course assistant's.
--   fz-*               onexai's own seeded agents (hack/fz-*-seed.sql), not
--                      OneX's to rewrite. `__builtin__` and the visitor text
--                      stay as they are.
--
-- Order matters: the preflight refuses to run if the record the rows are about
-- to point at is missing, because a name that resolves to nothing is not an
-- error at run time — it silently becomes the four-character system prompt
-- "onex". The backup is taken before the update, and only once.
--
-- Running it. After this, restart oai-agent-apiserver: a direct write bypasses
-- the cache refresh that its own write path performs, and the resource cache is
-- an SWR snapshot (cache/resource_cache.go), so a running process keeps the old
-- rows for up to store.refresh-interval (5m) rather than forever.
--
-- Rollback. There is no single "previous value" to restore — the rows held
-- three different ones — so the way back is the backup table this script
-- creates, and a note that those rows were provisioned by whatever binary was
-- deployed at the time. Both halves of the change (the constant and this column)
-- revert independently.

BEGIN;

-- Preflight: the reference has to exist, with content, before anything points
-- at it.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM agent.prompt WHERE name = 'onex' AND content <> '') THEN
        RAISE EXCEPTION 'no agent.prompt row named ''onex'' with content';
    END IF;
END $$;

-- The way back. IF NOT EXISTS so a second run cannot overwrite the first one's
-- capture of the original values.
CREATE TABLE IF NOT EXISTS agent.agent_sysprompt_backup_20261015 AS
SELECT name, sys_prompt, updated_at
  FROM agent.agent
 WHERE name LIKE 'onex-u-%' OR name = 'onex-x-max-tier';

-- What is about to change. Expect every row below to be a 1690- or 1529-length
-- text or an empty string, and none of them to be the name already.
SELECT name, status, length(sys_prompt) AS len, left(sys_prompt, 40) AS head
  FROM agent.agent
 WHERE name LIKE 'onex-u-%' OR name = 'onex-x-max-tier'
 ORDER BY name;

UPDATE agent.agent
   SET sys_prompt = 'onex',
       -- unix millis, the unit the model gives this column (model/agent.go).
       updated_at = (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT
 WHERE (name LIKE 'onex-u-%' OR name = 'onex-x-max-tier')
   AND sys_prompt <> 'onex';

COMMIT;
