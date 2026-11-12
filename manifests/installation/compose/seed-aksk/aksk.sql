-- Caller secrets for the secure profile.
--
-- oai-agent-apiserver resolves an AK/SK pair by secret_id from agent.secret.
-- The service has already run AutoMigrate by the time this runs (the seed
-- service waits for its /readyz), so the table exists.
--
-- Idempotent: re-running updates the same rows.

INSERT INTO agent.secret (secret_id, secret_key, description, created_at, updated_at)
VALUES
  (
    'oai-chat-apiserver',
    :'chat_secret',
    'compose chat caller',
    (EXTRACT(EPOCH FROM now()) * 1000)::bigint,
    (EXTRACT(EPOCH FROM now()) * 1000)::bigint
  ),
  (
    'oai-job-controller',
    :'job_secret',
    'compose job controller caller',
    (EXTRACT(EPOCH FROM now()) * 1000)::bigint,
    (EXTRACT(EPOCH FROM now()) * 1000)::bigint
  )
ON CONFLICT (secret_id) DO UPDATE
  SET secret_key = EXCLUDED.secret_key,
      updated_at = EXCLUDED.updated_at;
