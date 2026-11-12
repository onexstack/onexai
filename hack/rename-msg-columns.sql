-- Rename the two *_msg columns whose names followed their Go fields (PostgreSQL).
--
-- WHY. The message-naming cleanup expanded every Msg identifier to Message, and
-- these two columns are named after their fields:
--
--   agent.interaction.error_msg        <- InteractionM.ErrorMsg  (derived: GORM's
--                                         naming strategy turned ErrorMsg into
--                                         error_msg; the field is now
--                                         ErrorMessage, so the derived column is
--                                         error_message)
--   chat.oncall_group.welcome_msg      <- OncallGroupM.WelcomeMsg (explicit
--                                         `column:welcome_msg` tag, now
--                                         `column:welcome_message`)
--
-- AUTO-MIGRATE WILL NOT DO THIS. registry.Migrate (GORM AutoMigrate) never
-- renames a column: started against the old schema it would ADD an empty
-- error_message / welcome_message column and leave the old one (with every
-- existing value) in place, so every interaction would look error-free and every
-- on-call group would lose its welcome text. Start the new binaries only after
-- the renames below have run.
--
-- ORDERING IS NOT OPTIONAL:
--     1. stop oai-agent-apiserver and oai-chat-apiserver (the only binaries that
--        register InteractionM and OncallGroupM, hence the only ones whose
--        AutoMigrate touches these tables)
--     2. run this file against the onexai database
--     3. start the new binaries
--
-- IDEMPOTENT. Each DO block handles all three states: old-only (rename), both
-- present (copy the old values into the new column, then drop the old one), and
-- new-only (no-op). Re-running it is safe. RENAME COLUMN preserves the data, the
-- column type, and any index or constraint attached to it; nothing is copied or
-- dropped except in the both-present case, where the old column is dropped only
-- after its values have been copied.
--
-- The MySQL deployment is a read-only rollback point and is deliberately not
-- touched (onexai_agent_interaction.error_msg and
-- onexai_chat_oncall_group.welcome_msg there keep their names and data).

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'agent'
               AND table_name   = 'interaction'
               AND column_name  = 'error_msg')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'agent'
               AND table_name   = 'interaction'
               AND column_name  = 'error_message')
  THEN
    ALTER TABLE agent.interaction RENAME COLUMN error_msg TO error_message;
    RAISE NOTICE 'agent.interaction: renamed error_msg -> error_message';
  ELSIF EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'agent'
               AND table_name   = 'interaction'
               AND column_name  = 'error_msg')
     AND EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'agent'
               AND table_name   = 'interaction'
               AND column_name  = 'error_message')
  THEN
    -- Both columns exist: a binary started before this script ran, so
    -- AutoMigrate added the new column empty. Carry the old values over.
    UPDATE agent.interaction SET error_message = error_msg
     WHERE error_message IS NULL;
    ALTER TABLE agent.interaction DROP COLUMN error_msg;
    RAISE NOTICE 'agent.interaction: copied error_msg -> error_message, dropped error_msg';
  ELSE
    RAISE NOTICE 'agent.interaction: no rename needed (error_message already present, or error_msg absent)';
  END IF;
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'chat'
               AND table_name   = 'oncall_group'
               AND column_name  = 'welcome_msg')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'chat'
               AND table_name   = 'oncall_group'
               AND column_name  = 'welcome_message')
  THEN
    ALTER TABLE chat.oncall_group RENAME COLUMN welcome_msg TO welcome_message;
    RAISE NOTICE 'chat.oncall_group: renamed welcome_msg -> welcome_message';
  ELSIF EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'chat'
               AND table_name   = 'oncall_group'
               AND column_name  = 'welcome_msg')
     AND EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'chat'
               AND table_name   = 'oncall_group'
               AND column_name  = 'welcome_message')
  THEN
    UPDATE chat.oncall_group SET welcome_message = welcome_msg
     WHERE welcome_message IS NULL;
    ALTER TABLE chat.oncall_group DROP COLUMN welcome_msg;
    RAISE NOTICE 'chat.oncall_group: copied welcome_msg -> welcome_message, dropped welcome_msg';
  ELSE
    RAISE NOTICE 'chat.oncall_group: no rename needed (welcome_message already present, or welcome_msg absent)';
  END IF;
END
$$;
