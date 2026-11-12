-- The long-term memory prototype stored entries in agent.memory. The new schema
-- uses agent.memory_entry plus the declarative Memory object. Back the old table
-- up first if it contains anything worth keeping, then drop it.
--
-- This file is intentionally not wired into AutoMigrate: the prototype table has
-- no columns in common with the new model, and a migration that guessed would be
-- worse than an explicit operator decision.
DROP TABLE IF EXISTS agent.memory;
