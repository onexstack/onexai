-- Rename <schema>.<table>.workspace -> namespace (PostgreSQL).
--
-- WHY. The Go field has always been Namespace, but the column was spelled
-- workspace to keep the business-line partition's old name in the database. The
-- models now declare `column:namespace` explicitly and their JSON tags use
-- namespace, so the physical column has to follow.
--
-- AUTO-MIGRATE WILL NOT DO THIS. registry.Migrate (GORM AutoMigrate) never
-- renames a column: started against the old schema it would ADD an empty
-- namespace column and leave workspace (with every existing value) in place.
-- Start the new binary only after the rename below has run.
--
-- ORDERING IS NOT OPTIONAL:
--     1. stop oai-agent-apiserver (and oai-chat-apiserver if it is running) --
--        the old binaries still read `workspace`
--     2. run this file against the onexai database
--     3. start the new binaries
--
-- IDEMPOTENT. Each table is renamed only when workspace exists and namespace
-- does not, so re-running a completed migration is a no-op. If both columns
-- exist -- the old binary was restarted between steps, or a previous run died
-- halfway -- the script raises instead of silently dropping data; reconcile by
-- hand and re-run.
--
-- RENAME COLUMN preserves the data, the column type, and every index or
-- constraint attached to the column; nothing is copied or dropped.
--
-- The MySQL deployment is a read-only rollback point and is deliberately not
-- touched here. If a MySQL environment must follow, run the equivalent
-- `ALTER TABLE onexai_<service>_<table> RENAME COLUMN workspace TO namespace`
-- there separately.

BEGIN;

DO $$
DECLARE
  t text;
  tables text[] := ARRAY[
    'agent.agent',
    'agent.custom_command',
    'agent.knowledge_base',
    'agent.mcp',
    'agent.revision',
    'agent.runtime_hosting',
    'agent.runtime_instance',
    'agent.runtime_profile',
    'agent.scheduled_execution',
    'agent.scheduled_run',
    'agent.session',
    'agent.skill',
    'chat.oncall_group'
  ];
BEGIN
  FOREACH t IN ARRAY tables LOOP
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = split_part(t, '.', 1)
                 AND table_name   = split_part(t, '.', 2)
                 AND column_name  = 'workspace')
       AND NOT EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = split_part(t, '.', 1)
                 AND table_name   = split_part(t, '.', 2)
                 AND column_name  = 'namespace')
    THEN
      EXECUTE format('ALTER TABLE %s RENAME COLUMN workspace TO namespace', t);
      RAISE NOTICE '%: renamed workspace -> namespace', t;
    ELSIF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = split_part(t, '.', 1)
                 AND table_name   = split_part(t, '.', 2)
                 AND column_name  = 'workspace')
       AND EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = split_part(t, '.', 1)
                 AND table_name   = split_part(t, '.', 2)
                 AND column_name  = 'namespace')
    THEN
      RAISE EXCEPTION 'both workspace and namespace exist on %; reconcile manually', t;
    ELSE
      RAISE NOTICE '%: no rename needed (namespace present, or workspace absent)', t;
    END IF;
  END LOOP;
END
$$;

-- The model's index name comes from the Go field (Namespace), so every table
-- already has idx_<table>_namespace. The *_workspace twins are leftovers on the
-- tables created before the field was renamed; drop only those duplicates.
DROP INDEX IF EXISTS agent.idx_agent_agent_workspace;
DROP INDEX IF EXISTS agent.idx_agent_custom_command_workspace;
DROP INDEX IF EXISTS agent.idx_agent_knowledge_base_workspace;
DROP INDEX IF EXISTS agent.idx_agent_mcp_workspace;
DROP INDEX IF EXISTS agent.idx_agent_skill_workspace;
DROP INDEX IF EXISTS chat.idx_chat_oncall_group_workspace;

COMMIT;
