-- Rename the tables and authz subjects owned by the two services that were
-- renamed: oai-chatserver -> oai-chat-apiserver and oai-llmserver ->
-- oai-agent-apiserver.
--
-- WHY THIS IS NEEDED. dbschema.mysqlPrefix derives the physical table name from
-- the Service *value* ("chatserver"/"llmserver" before, "chat"/"agent" after),
-- so releasing the renamed code without this migration would let GORM
-- AutoMigrate create EMPTY onexai_chat_* / onexai_agent_* tables and orphan the
-- existing rows. Run this BEFORE starting the new binaries, with the old ones
-- stopped.
--
-- POSTGRESQL NEEDS NO TABLE RENAME. The PG schemas were already "chat" and
-- "agent" (see pkg/dbschema.schemaByService), so only the Go identifier and the
-- MySQL prefix change. Only the authz-subject UPDATE below applies to PG.
--
-- Ordering, per service: stop old process -> RENAME TABLE -> start new binary.

-- ---------------------------------------------------------------------------
-- 1. MySQL table renames.
--    chatserver -> chat: 9 tables (see internal/chat/*/table_names_test.go)
--    llmserver  -> agent: 11 tables (see internal/agent/store/sqlstore)
-- ---------------------------------------------------------------------------
RENAME TABLE
  `onexai_chatserver_session`           TO `onexai_chat_session`,
  `onexai_chatserver_ticket`            TO `onexai_chat_ticket`,
  `onexai_chatserver_chat_message`      TO `onexai_chat_chat_message`,
  `onexai_chatserver_bot_sre`           TO `onexai_chat_bot_sre`,
  `onexai_chatserver_lock`              TO `onexai_chat_lock`,
  `onexai_chatserver_metadata`          TO `onexai_chat_metadata`,
  `onexai_chatserver_message_record`    TO `onexai_chat_message_record`,
  `onexai_chatserver_conv_agent_record` TO `onexai_chat_conv_agent_record`,
  `onexai_chatserver_oncall_group`      TO `onexai_chat_oncall_group`,
  `onexai_llmserver_agent`              TO `onexai_agent_agent`,
  `onexai_llmserver_skill`              TO `onexai_agent_skill`,
  `onexai_llmserver_mcp`                TO `onexai_agent_mcp`,
  `onexai_llmserver_knowledge_base`     TO `onexai_agent_knowledge_base`,
  `onexai_llmserver_secret`             TO `onexai_agent_secret`,
  `onexai_llmserver_prompt`             TO `onexai_agent_prompt`,
  `onexai_llmserver_model`              TO `onexai_agent_model`,
  `onexai_llmserver_interaction`        TO `onexai_agent_interaction`,
  `onexai_llmserver_setting`            TO `onexai_agent_setting`,
  `onexai_llmserver_authz_rule`         TO `onexai_agent_authz_rule`,
  `onexai_llmserver_k8sgpt_endpoint`    TO `onexai_agent_k8sgpt_endpoint`;

-- ---------------------------------------------------------------------------
-- 2. Authorization subjects. The renamed services appear as MCP/resource names
--    inside JSON-array text columns, and as the MCP identity the agent side
--    advertises. Renaming the code without this leaves every existing rule
--    pointing at a name that no longer exists -> agent-level authz falls to
--    Deny (no HITL). Restart the services afterwards to drop the OPA decision
--    cache.
--
--    Inspect the affected rows first:
--      SELECT id, name, subject_type, subject_id, resource_name
--        FROM <table>
--       WHERE subject_id LIKE '%oai-chatserver%' OR resource_name LIKE '%oai-chatserver%'
--          OR subject_id LIKE '%oai-llmserver%' OR resource_name LIKE '%oai-llmserver%';
--
--    A rule whose `name` (unique index) encodes the old string needs its own
--    UPDATE; the seed file hack/authz-rules-seed.sql has already been updated to
--    the new names.
-- ---------------------------------------------------------------------------

-- PostgreSQL (live): table is agent.authz_rule
UPDATE agent.authz_rule
   SET subject_id    = REPLACE(subject_id,    '"oai-chatserver"', '"oai-chat-apiserver"'),
       resource_name = REPLACE(resource_name, '"oai-chatserver"', '"oai-chat-apiserver"')
 WHERE subject_id LIKE '%oai-chatserver%' OR resource_name LIKE '%oai-chatserver%';

UPDATE agent.authz_rule
   SET subject_id    = REPLACE(subject_id,    '"oai-llmserver"', '"oai-agent-apiserver"'),
       resource_name = REPLACE(resource_name, '"oai-llmserver"', '"oai-agent-apiserver"')
 WHERE subject_id LIKE '%oai-llmserver%' OR resource_name LIKE '%oai-llmserver%';

-- MySQL (read-only rollback deployment): table is onexai_agent_authz_rule
-- (rename the table first with section 1, then run these)
UPDATE `onexai_agent_authz_rule`
   SET `subject_id`    = REPLACE(`subject_id`,    '"oai-chatserver"', '"oai-chat-apiserver"'),
       `resource_name` = REPLACE(`resource_name`, '"oai-chatserver"', '"oai-chat-apiserver"')
 WHERE `subject_id` LIKE '%oai-chatserver%' OR `resource_name` LIKE '%oai-chatserver%';

-- ---------------------------------------------------------------------------
-- 3. AK/SK identity. chatserver signs its /v1 calls with the secret whose
--    secret_id is the api-key in ~/.onexai/oai-chat-apiserver.yaml
--    (agent-aksk.api-key: "oai-chat-apiserver"). That same string is the
--    serviceaccount name the authz rules match on, so the secret row must move
--    with it or every signed call is rejected.
--    PG: agent.secret ; MySQL: onexai_agent_secret (secret_id has a unique index).
-- ---------------------------------------------------------------------------
UPDATE agent.secret    SET secret_id = 'oai-chat-apiserver' WHERE secret_id = 'oai-chatserver';

UPDATE `onexai_agent_secret` SET `secret_id` = 'oai-chat-apiserver' WHERE `secret_id` = 'oai-chatserver';

UPDATE `onexai_agent_authz_rule`
   SET `subject_id`    = REPLACE(`subject_id`,    '"oai-llmserver"', '"oai-agent-apiserver"'),
       `resource_name` = REPLACE(`resource_name`, '"oai-llmserver"', '"oai-agent-apiserver"')
 WHERE `subject_id` LIKE '%oai-llmserver%' OR `resource_name` LIKE '%oai-llmserver%';
