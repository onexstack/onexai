-- PostgreSQL schemas used by the onexai services.
--
-- GORM AutoMigrate creates the tables at each service's startup, but it does not
-- create schemas. pkg/dbschema qualifies every table with one of these names, so
-- a fresh database needs them before the services start.
--
-- This script runs only when the PostgreSQL data directory is empty. After
-- changing it, re-create the stack with `docker compose down -v`.

CREATE SCHEMA IF NOT EXISTS agent;
CREATE SCHEMA IF NOT EXISTS chat;
CREATE SCHEMA IF NOT EXISTS job;
CREATE SCHEMA IF NOT EXISTS mirror;

GRANT ALL ON SCHEMA agent, chat, job, mirror TO CURRENT_USER;
