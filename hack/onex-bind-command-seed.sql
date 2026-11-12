-- /bind-onex: which agents offer Feishu account binding.
--
-- Why this file exists. /bind-onex used to be an account-level command
-- (chat.AccountLevel, x-account-level): always built, always executable, for
-- every conversation regardless of what its agent had been granted. That flag
-- came off on 2026-10-14, so binding is now an ordinary catalogued command and
-- needs a rule like every other one: an agent no rule covers runs nothing (see
-- internal/chat/pkg/agentcmd/resolver.go — the default is deny).
--
-- Three statements, because three different agents need it back.
--
--   onex / onex-x-max-tier — UPDATE
--     Both rules name the command as "bind", the name it had before the
--     2026-10-14 rename. A rule naming a command the catalog does not have is an
--     orphan: the resolver drops it (rule.go, "An orphan: the command was
--     renamed or removed after the rule was written"), it grants nothing, and
--     nothing fails loudly — which is why these two rows kept saying "bind" and
--     every agent they cover silently lost the binding command the moment the
--     command was renamed. These statements point them at the new name.
--
--     The first of the two is also where "OneX binds the command when it creates
--     an agent" is spelled: its selector is the tag the provisioner already puts
--     on every agent it creates (onex.example.com/onex — 30-odd onex-u-user-*
--     rows carry it), so a newly provisioned agent is covered the moment it
--     exists, and this file never has to grow a row per agent.
--
--   unbound-default-bind — INSERT
--     The two audience defaults, by name. An unbound speaker's conversation
--     resolves to one of them — fz-public for a tenant member without their own
--     agent, fz-visitor for a speaker the platform cannot identify
--     (chat.internal-default / chat.external-default) — and binding is exactly
--     what such a speaker needs. Neither is covered by any rule today, so both
--     run nothing at all; without this row, the people binding exists for are
--     the ones who cannot run it. They are the platform's own built-ins and no
--     provisioner creates them, so they are named rather than tagged.
--
-- fz-admin needs no statement: its rule's command_selector is the exhaustive
-- nine-group selector, and /bind-onex belongs to the core group, so it already
-- has the command.
--
-- ⚠️ Two footguns. (1) The two UPDATEs are no-ops on a deployment that has
-- neither rule — a fresh database has no rule covering onex.example.com/onex
-- unless someone writes one, and then OneX-provisioned agents offer no binding.
-- (2) The tag string is a contract with OneX: it is spelled in the selector
-- below and in whatever the provisioner puts in an agent's `tags` when it calls
-- POST /v1/agent/agents. A typo on either side selects nothing, silently.
--
-- Dialect: PostgreSQL — chat.agent_command (dbschema splits per-service schemas
-- on PG). On MySQL the table is onexai_chat_agent_command and this file needs
-- backtick quoting — do not run it there as-is.
--
-- Idempotent: the UPDATEs write the same value every time (a WHERE on the old
-- value would make re-running hit zero rows and hide a changed row), and the
-- INSERT uses the unique index on `name` (idx_chat_agent_command_name). The
-- commands column holds JSON text; the empty string is this service's way of
-- saying "no entries" (see handler.encodeList).
--
-- Rollback:
--   DELETE FROM chat.agent_command WHERE name = 'unbound-default-bind';
--   UPDATE chat.agent_command SET commands = '[]'
--    WHERE name IN ('onex', 'onex-x-max-tier');
--
-- Rolling back is not "put it back the way it was": '["bind"]' is the orphan
-- this file exists to fix, and it grants nothing. Empty is the same statement
-- made honestly. Either way, rollback takes binding away from every agent
-- except fz-admin and whatever a narrower rule still covers.

-- ── The two platform rules: the command, under its new name ──────────────────
--
-- The selectors and the group grants are left exactly as they are — the grant
-- this file adds is the binding command, not a redefinition of either rule.

UPDATE chat.agent_command
   SET commands   = '["bind-onex"]',
       updated_at = (EXTRACT(EPOCH FROM now()) * 1000)::bigint
 WHERE name = 'onex';

UPDATE chat.agent_command
   SET commands   = '["bind-onex"]',
       updated_at = (EXTRACT(EPOCH FROM now()) * 1000)::bigint
 WHERE name = 'onex-x-max-tier';

-- ── The unbound speakers: the two defaults, by name ──────────────────────────

INSERT INTO chat.agent_command
  (name, display_name, description, status, agent_selector, agent_field_selector,
   agents, command_selector, commands, created_at, updated_at)
VALUES
  ('unbound-default-bind',
   'Audience defaults: /bind-onex',
   '两个受众默认 agent（内部未绑定 = fz-public，平台认不出 = fz-visitor）可执行 /bind-onex。未绑定的说话人正是需要这条命令的人：他的会话解析到这两个 agent 之一，而绑定之前机器人无法以他的身份作答。两个都是平台内置、不走创建接口，所以按名字点名而不是打标签。',
   'enabled', '', '',
   '["fz-public","fz-visitor"]', '', '["bind-onex"]',
   (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint)
ON CONFLICT (name) DO UPDATE SET
  display_name         = EXCLUDED.display_name,
  description          = EXCLUDED.description,
  status               = EXCLUDED.status,
  agent_selector       = EXCLUDED.agent_selector,
  agent_field_selector = EXCLUDED.agent_field_selector,
  agents               = EXCLUDED.agents,
  command_selector     = EXCLUDED.command_selector,
  commands             = EXCLUDED.commands,
  updated_at           = EXCLUDED.updated_at;
