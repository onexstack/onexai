-- fz-visitor: the agent behind a speaker the platform cannot identify as a member.
--
-- Why this file exists. A conversation whose speaker has no agent binding falls
-- back to an audience default (chat.external-default / chat.internal-default,
-- resolved in internal/chat/biz/v1/bot/deciders.go). For an outside speaker that
-- default is fz-visitor: the zero-capability assistant an account with no
-- membership meets. It is the counterpart of the shared `onex-x-max-tier` — one
-- answers "what does a paying top-tier member retrieve", this one answers "what
-- does somebody with nothing get".
--
-- Three tables have to agree before "this agent exists and runs" is true, and
-- none of them is created by this file's siblings:
--
--   agent.agent         the agent itself, so it can be resolved and run
--   agent.authz_rule    its grants, so oai-agent-apiserver does not refuse it
--   chat.agent_command  the rule that hands it slash commands
--
-- The last one is deliberately **not** here, and the "no" is now a real one:
-- this agent runs no slash command at all — no menu, and no command by name.
-- It used to inherit the account-level commands (chat.AccountLevel, e.g.
-- /bind-onex) for free, but that flag came off /bind-onex on 2026-10-14 and the
-- command is a grant like any other. The one command a visitor does need,
-- binding their own Feishu account, is granted to this agent **by name** in
-- hack/onex-bind-command-seed.sql — that file and this one are a pair, and a
-- deployment that runs only this one leaves an outside speaker unable to bind.
-- Anything beyond it would be a capability this agent is defined not to have.
--
-- Dialect: PostgreSQL. The live deployment stores these as agent.agent and
-- agent.authz_rule (dbschema splits per-service schemas on PG). On MySQL the
-- names are onexai_agent_agent / onexai_agent_authz_rule, and this file needs
-- backtick quoting — do not run it there as-is.
--
-- Idempotent: both tables carry a unique index on `name`, so re-running refreshes
-- the row rather than duplicating it.
--
-- Rollback:
--   DELETE FROM agent.authz_rule WHERE name LIKE 'allow-fz-visitor-%';
--   DELETE FROM agent.agent      WHERE name = 'fz-visitor';
--
-- Note that removing this file's rows takes the *fallback* away rather than
-- shrinking it: chat.external-default would then name an agent that does not
-- exist, which resolves to nothing and leaves an outside speaker with no answer
-- at all. Replace the configuration first, or retire the two together.

-- ── The agent ────────────────────────────────────────────────────────────────
--
-- Zero capability, on purpose. No skills, no MCP servers, no knowledge bases:
-- everything this agent could reach would be something a person with no
-- membership was never sold. `owner` is onexai (the platform's built-ins) and
-- `namespace` is empty, which reads as model.DefaultNamespace — see
-- internal/agent/model/namespace.go.
--
-- The agent's prompt (bound in the control plane, not stored in this table) says
-- what the agent is, so that a visitor is told plainly how to get more rather
-- than being answered as though they had subscribed.
INSERT INTO agent.agent
  (name, display_name, status, description, owner, skills, mcps,
   knowledge_bases, require_approval, authz_backends, namespace,
   created_at, updated_at)
VALUES
  ('fz-visitor', 'FZ Visitor', 'enabled',
   'Zero-capability fallback assistant for speakers with no membership.',
   'onexai',
   '[]', '[]', '[]', '[]', '', '',
   (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint)
ON CONFLICT (name) DO UPDATE SET
  display_name     = EXCLUDED.display_name,
  status           = EXCLUDED.status,
  description      = EXCLUDED.description,
  owner            = EXCLUDED.owner,
  skills           = EXCLUDED.skills,
  mcps             = EXCLUDED.mcps,
  knowledge_bases  = EXCLUDED.knowledge_bases,
  namespace        = EXCLUDED.namespace,
  updated_at       = EXCLUDED.updated_at;

-- ── The authorization grants ─────────────────────────────────────────────────
--
-- Two rows, and the reason there are only two is the agent above: the run path
-- checks the agent's *declared* references (AuthorizeResourceRefs over skills,
-- MCPs and knowledge bases), and this agent declares none — so those dimensions
-- are never consulted for it. What remains is the pair every agent needs:
--
--   · the workspace gate, which AuthorizeAgent evaluates *before* the agent gate
--     and which is fail-closed (all-NoOpinion folds to Deny before the agent
--     dimension is read) — so without it the agent grant below is never reached;
--   · the agent gate itself.
--
-- ⚠️ Adding a skill, MCP server or knowledge base to the `agent.agent` row above
-- means adding the matching grant here (or a wildcard). The two lists are checked
-- independently and a mismatch is a 403 with no owner.
--
-- `subject_id` is `["*"]`: this is the agent for speakers the platform cannot
-- name, so scoping the grant to anybody in particular would defeat it. The gate
-- that actually keeps outsiders out of *internal* resources is the audience split
-- (an external speaker resolves to this agent, not to chat.internal-default) and
-- the per-user membership grants — not this row.
INSERT INTO agent.authz_rule
  (name, subject_type, subject_id, resource_kind, resource_name,
   resource_attributes, agent_names, verbs, effect, status, description, owner,
   created_at, updated_at)
VALUES
  ('allow-fz-visitor-agent', 'user', '["*"]', 'agent', '["fz-visitor"]',
   '', '', '["read"]', 'allow', 'enabled',
   'chat 终端用户可使用内置 fz-visitor agent（访客默认）', 'ops',
   (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint),

  ('allow-fz-visitor-workspace', 'user', '["*"]', 'workspace', '["*"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-visitor agent 所属 workspace 放行（workspace 门在 agent 门之前）', 'ops',
   (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint)
ON CONFLICT (name) DO UPDATE SET
  subject_type   = EXCLUDED.subject_type,
  subject_id     = EXCLUDED.subject_id,
  resource_kind  = EXCLUDED.resource_kind,
  resource_name  = EXCLUDED.resource_name,
  agent_names    = EXCLUDED.agent_names,
  verbs          = EXCLUDED.verbs,
  effect         = EXCLUDED.effect,
  status         = EXCLUDED.status,
  description    = EXCLUDED.description,
  owner          = EXCLUDED.owner,
  updated_at     = EXCLUDED.updated_at;
