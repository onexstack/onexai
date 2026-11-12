-- Rename agent.agent.mc_ps -> mcps (PostgreSQL).
--
-- WHY. The column holds the JSON array of MCP servers an agent is granted, and
-- its name is derived, not declared: the Go field is AgentRecord.MCPs and GORM's
-- naming strategy turns that into mc_ps (the trailing "s" starts a new word).
-- records.go now carries an explicit `column:mcps` tag, so new code addresses
-- mcps; the physical column has to follow.
--
-- AUTO-MIGRATE WILL NOT DO THIS. registry.Migrate (GORM AutoMigrate) never
-- renames a column: started against the old schema it would ADD an empty mcps
-- column and leave mc_ps (with every existing grant) in place, so every agent
-- would look as if it lost its MCP servers. Start the new binary only after the
-- rename below has run.
--
-- ORDERING IS NOT OPTIONAL:
--     1. stop oai-agent-apiserver (it is the only binary that registers
--        AgentRecord, hence the only one whose AutoMigrate touches this table)
--     2. run this file against the onexai database
--     3. start the new binary
--
-- IDEMPOTENT. The DO block renames only when mc_ps exists and mcps does not, so
-- re-running it (or running it after the rename) is a no-op. RENAME COLUMN
-- preserves the data, the column type, and any index or constraint attached to
-- it; nothing is copied or dropped.
--
-- The MySQL deployment is a read-only rollback point and is deliberately not
-- touched (onexai_agent_agent.mc_ps there keeps its name and data).

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'agent'
               AND table_name   = 'agent'
               AND column_name  = 'mc_ps')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'agent'
               AND table_name   = 'agent'
               AND column_name  = 'mcps')
  THEN
    ALTER TABLE agent.agent RENAME COLUMN mc_ps TO mcps;
    RAISE NOTICE 'agent.agent: renamed mc_ps -> mcps';
  ELSE
    RAISE NOTICE 'agent.agent: no rename needed (mcps already present, or mc_ps absent)';
  END IF;
END
$$;
