-- chat.ticket field refactor: rename the identity / people / timestamp columns,
-- drop the columns nothing writes, and move the handling log into its own table.
--
-- Dialect: PostgreSQL. The live deployment stores this as chat.ticket (dbschema
-- splits per-service schemas on PG). The MySQL deployment is kept read-only as a
-- rollback point, so there is no MySQL half of this file; the equivalents are
-- ALTER TABLE ... CHANGE COLUMN and ALTER TABLE ... RENAME INDEX.
--
-- ORDERING IS NOT OPTIONAL. GORM AutoMigrate (registry.Migrate, called from
-- internal/chat/server.go NewDB) adds missing columns and indexes but never
-- renames or drops one, so this file is the only thing that can. The window is:
--
--   1. stop oai-chat-apiserver (every replica)
--   2. psql -f hack/chat-ticket-field-refactor.sql
--   3. start the new binary
--
-- and nothing may restart mid-window. A *new* binary started first would have
-- AutoMigrate ADD incident_id ... NOT NULL (which a non-empty table rejects) and
-- create a unique index on it, after which this file's RENAME COLUMN fails with
-- "column already exists". An *old* binary restarted after this file re-adds
-- ticket_number and then errors on every ticket query.

BEGIN;

-- 1. The handling log moves to its own table before the column carrying it is
--    dropped: nothing reads those notes yet, so this backfill is the only
--    chance to keep the history /mark recorded. The DDL is spelled out rather
--    than left to AutoMigrate so the rows can be copied in the same
--    transaction; the types are what the GORM model produces.
CREATE TABLE IF NOT EXISTS chat.ticket_note (
    id          bigserial PRIMARY KEY,
    ticket_id   text        NOT NULL,
    author_id   text        NOT NULL DEFAULT '',
    author_name text        NOT NULL DEFAULT '',
    note        text        NOT NULL,
    review      boolean     NOT NULL DEFAULT false,
    demand      boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT current_timestamp
);

INSERT INTO chat.ticket_note (ticket_id, author_id, author_name, note, review, demand, created_at)
SELECT ticket_number, '', '', processing_record, review, demand, updated_at
FROM chat.ticket
WHERE processing_record <> '' OR review OR demand;

CREATE INDEX IF NOT EXISTS idx_chat_ticket_note_ticket_id ON chat.ticket_note (ticket_id);

-- 2. Renames. Renaming a column preserves its data, and the unique index on
--    ticket_number follows the column while keeping its old name — step 3
--    renames that separately.
ALTER TABLE chat.ticket RENAME COLUMN ticket_number         TO ticket_id;
ALTER TABLE chat.ticket RENAME COLUMN group_code            TO group_id;
ALTER TABLE chat.ticket RENAME COLUMN level_id              TO level;
ALTER TABLE chat.ticket RENAME COLUMN creator_id            TO reporter_id;
ALTER TABLE chat.ticket RENAME COLUMN creator_name          TO reporter_name;
ALTER TABLE chat.ticket RENAME COLUMN primary_assignee_id   TO assignee_id;
ALTER TABLE chat.ticket RENAME COLUMN primary_assignee_name TO assignee_name;
ALTER TABLE chat.ticket RENAME COLUMN actual_resolve_time   TO resolved_at;
ALTER TABLE chat.ticket RENAME COLUMN close_time            TO closed_at;
ALTER TABLE chat.ticket RENAME COLUMN regen_incident_id     TO incident_id;

-- 3. Give the unique index the name GORM will look for: idx_<table>_<column>,
--    with the schema dot flattened to an underscore. If it keeps its old name
--    the next AutoMigrate finds no index under the new name and creates a
--    second, identical one beside it.
DO $$
DECLARE idx text;
BEGIN
    SELECT c.relname INTO idx
    FROM pg_class c
    JOIN pg_index i ON i.indexrelid = c.oid
    JOIN pg_class t ON t.oid = i.indrelid
    JOIN pg_namespace n ON n.oid = t.relnamespace
    WHERE n.nspname = 'chat'
      AND t.relname = 'ticket'
      AND i.indisunique
      AND i.indnatts = 1
      AND (SELECT attname FROM pg_attribute
           WHERE attrelid = t.oid AND attnum = i.indkey[0]) = 'ticket_id'
    LIMIT 1;
    IF idx IS NOT NULL AND idx <> 'idx_chat_ticket_ticket_id' THEN
        EXECUTE format('ALTER INDEX chat.%I RENAME TO idx_chat_ticket_ticket_id', idx);
    END IF;
END $$;

-- 4. The columns nothing writes go. assignees and follower_list were only ever
--    written by the opsassist import, initial_group_code by nothing at all, and
--    the three log columns have just been copied to chat.ticket_note.
ALTER TABLE chat.ticket
    DROP COLUMN IF EXISTS assignees,
    DROP COLUMN IF EXISTS follower_list,
    DROP COLUMN IF EXISTS initial_group_code,
    DROP COLUMN IF EXISTS processing_record,
    DROP COLUMN IF EXISTS review,
    DROP COLUMN IF EXISTS demand;

COMMIT;

-- The two new indexes (chat.ticket.group_id, chat.ticket.assignee_id) are left
-- to the next AutoMigrate, which creates missing indexes on start-up.
