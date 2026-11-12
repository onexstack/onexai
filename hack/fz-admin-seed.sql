-- fz-admin: a built-in agent that may run every command oai-chat-apiserver offers.
--
-- Why this file exists. Three tables have to agree before that sentence is true,
-- and none of them is created by this file's siblings:
--
--   agent.agent         the agent itself, so it can be resolved and run a
--                       conversation (a conversation reaches it through
--                       chat.session.refs, which is resolved per speaker)
--   agent.authz_rule    its grants, so oai-agent-apiserver does not refuse it
--
-- Command grants are AgentBindings now (CommandGrant was removed); they are
-- seeded separately, because an AgentBinding is a control-plane object rather
-- than a row. They are *fail-closed*: `agentcmd.Resolver.Resolve` answers with the empty selection
-- for an agent that no rule covers (internal/chat/pkg/agentcmd/resolver.go), so
-- an agent nobody wrote a rule for runs nothing at all — no menu, and no
-- command by name either. fz-admin therefore gets a rule that covers it and
-- grants everything, and deleting that rule does not shrink it: it takes every
-- command away.
--
-- The authz half is the fail-*closed* side. AuthorizeAgent runs a Workspace-level
-- check before the Agent-level one, and a fail-closed union folds every missing
-- kind into Deny before the dimension-level default is consulted, so a rule set
-- carrying only <kind>=agent is already denied and the agent rule is never
-- reached. Hence one row per kind — workspace, agent, skill, mcp, knowledgebase
-- — plus the process-wide tool grant. Missing any one of them turns a 403 into
-- another 403 on the same card. See hack/authz-rules-seed.sql (which is MySQL
-- and predates the PostgreSQL move) for the full explanation of each dimension.
--
-- Dialect: PostgreSQL. The live deployment stores these as agent.agent and
-- agent.authz_rule (dbschema splits per-service schemas on PG). On MySQL the
-- names are onexai_agent_agent / onexai_agent_authz_rule and this file needs
-- backtick quoting — do not run it there as-is.
--
-- Idempotent: each table carries a unique index on `name`
-- (idx_agent_agent_name, idx_agent_authz_rule_name), so re-running refreshes
-- the row rather than duplicating it.
--
-- Rollback:
--   DELETE FROM agent.authz_rule WHERE name LIKE 'allow-fz-admin-%';
--   DELETE FROM agent.agent      WHERE name = 'fz-admin';
--   kubectl delete agentbinding fz-admin   # the command grant
--
-- Deleting the AgentBinding takes every command away from fz-admin: the default
-- is deny, and no binding is an empty grant.

-- ── The agent ────────────────────────────────────────────────────────────────
--
-- Field for field the fz-default row: the same MCP servers and knowledge base.
-- `owner` is onexai (the platform's built-ins) and `namespace` is empty, which
-- reads as model.DefaultNamespace ("helper-public")
-- — see internal/agent/model/namespace.go.
--
-- ⚠️ Adding an MCP server to the `mcps` column means adding its name to
-- allow-fz-admin-mcp-refs below (or to a wildcard grant); the two lists are
-- checked independently and a mismatch is another 403.
INSERT INTO agent.agent
  (name, display_name, status, description, owner, skills, mcps,
   knowledge_bases, require_approval, authz_backends, namespace,
   created_at, updated_at)
VALUES
  ('fz-admin', 'FZ Admin', 'enabled',
   'Built-in admin assistant; holds every chat command the platform compiles.',
   'onexai', '',
   '["zke-kubeagent","zadig-mcp","onexai","oai-chat-apiserver"]',
   '["kb-src-kbi-LIVE2"]', '', '', '',
   (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint)
ON CONFLICT (name) DO UPDATE SET
  display_name     = EXCLUDED.display_name,
  status           = EXCLUDED.status,
  description      = EXCLUDED.description,
  owner            = EXCLUDED.owner,
  mcps             = EXCLUDED.mcps,
  knowledge_bases  = EXCLUDED.knowledge_bases,
  namespace        = EXCLUDED.namespace,
  updated_at       = EXCLUDED.updated_at;

-- ── The authorization grants ─────────────────────────────────────────────────
--
-- Mirrors allow-fz-default-*: the subject is a Lark user, not a service account,
-- because oai-chat-apiserver forwards the end user's identity. agent_names is
-- left empty (applies to every agent) — the column exists to scope a *denial*,
-- which none of these is.
INSERT INTO agent.authz_rule
  (name, subject_type, subject_id, resource_kind, resource_name,
   resource_attributes, agent_names, verbs, effect, status, description, owner,
   created_at, updated_at)
VALUES
  ('allow-fz-admin-agent', 'user', '["*"]', 'agent', '["fz-admin"]',
   '', '', '["read"]', 'allow', 'enabled',
   'chat 终端用户可使用内置 fz-admin agent', 'ops',
   (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint),

  ('allow-fz-admin-mcp-refs', 'user', '["*"]', 'mcp',
   '["zke-kubeagent","zadig-mcp","onexai","oai-chat-apiserver"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-admin agent 引用的 MCP server 放行', 'ops',
   (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint),

  ('allow-fz-admin-tools', 'user', '["*"]', 'tool', '["*"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-admin agent 的工具调用放行（不再转人工审批）', 'ops',
   (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint),

  -- workspace 门排在 agent 门之前（AuthorizeAgent），且 union 会把 NoOpinion
  -- 折成 Deny，缺这条时同一张卡片上的其它规则一条都到不了。
  ('allow-fz-admin-workspace', 'user', '["*"]', 'workspace', '["*"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-admin agent 所属 workspace 放行（workspace 门在 agent 门之前）', 'ops',
   (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint),

  ('allow-fz-admin-skills', 'user', '["*"]', 'skill', '["*"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-admin agent 引用的 Skill 放行', 'ops',
   (EXTRACT(EPOCH FROM now()) * 1000)::bigint, (EXTRACT(EPOCH FROM now()) * 1000)::bigint),

  ('allow-fz-admin-kb-refs', 'user', '["*"]', 'knowledgebase', '["*"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-admin agent 引用的知识库放行', 'ops',
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

-- ── The command grant ───────────────────────────────────────────────────────
--
-- Command grants are AgentBindings now, not rows: CommandGrant was removed when
-- AgentBinding took over the selector-based grant. This file therefore seeds
-- only the agent and its authorization rules.
--
-- The equivalent grant lists every catalogued command by name (a binding names
-- commands; it does not select them):
--
--   apiVersion: agent.onexai.io/v1alpha1
--   kind: AgentBinding
--   metadata: {name: fz-admin, namespace: default}
--   spec:
--     agents: [fz-admin]
--     commands: [...]          # command.CommandNames(), or the output of
--                              # go run ./hack/commandgrant2binding
--
-- Delete the binding to take every command away: commands are deny-by-default,
-- and no binding is an empty grant. To shrink it, edit the binding — deleting
-- it is not a smaller version of the same thing, it is the smallest one.
