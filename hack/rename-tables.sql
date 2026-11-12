-- HISTORICAL. This script renamed tables inside the MySQL schema and has
-- already been run. The tables now live in PostgreSQL, one schema per
-- service (agent / chat / job) with the service prefix dropped; see
-- pkg/dbschema and hack/onexai-mysql2pg. The MySQL deployment is kept
-- read-only as a rollback point, so this file is a record, not a runbook.
--
-- Rename the 22 tables shared by oai-chatserver / oai-llmserver / oai-jobserver
-- in the `zagent` schema so the name states which service owns the table.
--
-- WHY RENAME AND NOT RECREATE. These tables carry data: message_records 1154
-- rows, oai_session 9, oai_authz_rule 6, oai_ai_mcp 4, oai_ticket 2,
-- oai_ai_agent 1, oai_ai_secret 1. The schema is owned by GORM AutoMigrate
-- (registry.Migrate at service start), so simply releasing new code would have
-- AutoMigrate create EMPTY tables under the new names and orphan the old rows.
--
-- ORDERING IS NOT OPTIONAL: the old binaries query the old names, so the
-- maintenance window must be
--     1. stop oai-chatserver, oai-llmserver, oai-jobserver
--     2. run this file
--     3. start the new binaries
-- Starting a new binary BEFORE the rename makes AutoMigrate create empty
-- tables at the new names, after which RENAME fails with "table exists".
--
-- A single RENAME TABLE statement is atomic in MySQL/MariaDB: all 22 move or
-- none do. Column definitions, indexes, row data and the generated
-- `metadata.cluster_id` column are preserved verbatim.

RENAME TABLE
  `oai_session`           TO `onexai_chatserver_session`,
  `oai_ticket`            TO `onexai_chatserver_ticket`,
  `oai_chat_message`      TO `onexai_chatserver_chat_message`,
  `oai_bot_sre`           TO `onexai_chatserver_bot_sre`,
  `oai_lock`              TO `onexai_chatserver_lock`,
  `metadata`              TO `onexai_chatserver_metadata`,
  `message_records`       TO `onexai_chatserver_message_record`,
  `conv_agent_records`    TO `onexai_chatserver_conv_agent_record`,
  `oai_ai_module`         TO `onexai_chatserver_oncall_group`,
  `oai_job`               TO `onexai_jobserver_job`,
  `oai_cronjob`           TO `onexai_jobserver_cronjob`,
  `oai_ai_agent`          TO `onexai_llmserver_agent`,
  `oai_ai_skill`          TO `onexai_llmserver_skill`,
  `oai_ai_mcp`            TO `onexai_llmserver_mcp`,
  `oai_ai_knowledge_base` TO `onexai_llmserver_knowledge_base`,
  `oai_ai_secret`         TO `onexai_llmserver_secret`,
  `oai_ai_prompt`         TO `onexai_llmserver_prompt`,
  `oai_ai_model`          TO `onexai_llmserver_model`,
  `oai_interaction`       TO `onexai_llmserver_interaction`,
  `oai_setting`           TO `onexai_llmserver_setting`,
  `oai_authz_rule`        TO `onexai_llmserver_authz_rule`,
  `oai_k8sgpt_endpoint`   TO `onexai_llmserver_k8sgpt_endpoint`;

-- NOTE for oai-syncserver (deliberately left untouched by this change): it
-- mirrors `zagent` in whole-database mode keyed by source_table, so on its next
-- start it discovers the 15 renamed tables as new ones and provisions 15 new
-- Bitable tables. The old Bitable tables and their zagent_feishu_sync.oai_sync_*
-- rows become orphans and need manual cleanup.
