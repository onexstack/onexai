-- oai-agent-apiserver authorization rules: the baseline grant that makes the built-in
-- `default` Agent usable from oai-chat-apiserver.
--
-- Why this file exists. Authorization is fail-closed: an empty `onexai_agent_authz_rule`
-- makes OPA answer NoOpinion for everything, which the Union folds into Deny, so
-- every caller is refused. Rules can now be written through the service's
-- `/v1/agent/authorizationrules` CRUD API (see internal/agent/handler/authz_rule.go), which
-- reloads the in-memory rule set before returning; this file remains the way to
-- seed the fail-closed baseline on a fresh database in one statement, and to
-- restore it without a running service. Without either path the empty table is
-- exactly the kind of configuration that gets lost on a fresh database and then
-- presents as "你没有使用当前 Agent 的权限" on every Lark card.
--
-- What the rule set has to cover. PEP #1 is not a single check. AuthorizeAgent
-- runs a Workspace-level check *before* the Agent-level one: it resolves the
-- Agent's workspace column (empty normalizes to types.DefaultWorkspace,
-- "helper-public") and asks "read workspace:<ws>", so a rule set carrying only
-- <kind>=agent is already denied at that first dimension and the agent rule is
-- never reached — every dimension fails closed on all-NoOpinion (see
-- TestAuthorizeResourceRefsIsFailClosedWhetherOrNotAUnionIsInFront) and
-- evaluateLevels short-circuits on the first Deny. `authorizeRun` then runs the
-- same fail-closed AuthorizeResourceRefs check over the Agent's declared
-- Skills/MCPs/KnowledgeBases. The baseline therefore has to name every kind the
-- run path can ask about — workspace, agent, skill, mcp, knowledgebase — one
-- row per kind; missing any one of them turns the 403 into another 403 on the
-- same card.
--
-- The subject is a Lark user, not a service account: oai-chat-apiserver forwards the
-- end user's identity (SubjectFromUser, sender_id = the Lark user_id, e.g.
-- "go.kong"), so a subject_type=serviceaccount rule would never match this path.
-- A rule that names a person therefore writes their user_id — the id an operator
-- can read off the org directory — and not their per-application open_id.
--
-- Rules may name the Agents they apply to, through the `agent_names` column,
-- and a rule that names none applies to every Agent — the policy's historical
-- behaviour, kept so every row predating the column keeps its meaning. The
-- grants below therefore leave it empty, with one exception: **deny-fz-public-mcp
-- is scoped to `["fz-public"]` and is the only thing in this file that could
-- not be expressed before the column existed.** See its comment.
--
-- The tool grant is process-wide in the other sense — it is not scoped to any
-- resource either: it stops authorization from holding *any* tool call for a
-- human. A tool still listed in an Agent's or MCP server's require_approval
-- column keeps asking — that is the static approval policy, a different gate.
--
-- resource_name has two spellings, and the direction of the compatibility
-- matters. A bare name ("onexai", "onex-console") matches that resource in any
-- namespace — the historical contract, and what this file and OneX's reconcile
-- path write. A qualified "namespace/name" matches only that one object. At
-- runtime a namespaced reference is addressed as "namespace/name", so a bare
-- rule still matches it; a bare request never picks up a qualified rule. Deny
-- rules match the same way, which keeps a bare deny namespace-wide.
--
-- Maintenance: adding an MCP server to the `fz-default` Agent means adding its name
-- to `allow-fz-default-agent-mcp-refs`; workspace/skill/knowledgebase are wildcards,
-- so they need no per-resource upkeep; a new Agent means a new role/grant row set.
-- Rules written through `/v1/agent/authorizationrules` take effect on the next request: the
-- write path reloads the rule set before returning. Rows written directly to the
-- table (like this file) are picked up within rule-table-refresh-interval
-- (default 60s). Either way, a denial already cached for the same (subject,
-- verb, resource) survives up to the CachedAuthorizer's 5-minute TTL.
--
-- Idempotent: re-running overwrites the same-named rows rather than duplicating.

INSERT INTO `onexai_agent_authz_rule`
  (`name`, `subject_type`, `subject_id`, `resource_kind`, `resource_name`,
   `resource_attributes`, `agent_names`, `verbs`, `effect`, `status`, `description`, `owner`,
   `created_at`, `updated_at`)
VALUES
  ('allow-fz-default-agent', 'user', '["*"]', 'agent', '["fz-default"]',
   '', '', '["read"]', 'allow', 'enabled',
   'chat 终端用户可使用内置 fz-default agent', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  ('allow-fz-default-agent-mcp-refs', 'user', '["*"]', 'mcp',
   '["zke-kubeagent","zadig-mcp","onexai","oai-chat-apiserver"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-default agent 引用的 MCP server 放行', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  ('allow-fz-default-agent-tools', 'user', '["*"]', 'tool', '["*"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-default agent 的工具调用放行（不再转人工审批）', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  -- workspace 门排在 agent 门之前（AuthorizeAgent），且 union 会把 NoOpinion
  -- 折成 Deny，缺这条时同一张卡片上的其它规则一条都到不了。
  ('allow-fz-default-agent-workspace', 'user', '["*"]', 'workspace', '["*"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-default agent 所属 workspace 放行（workspace 门在 agent 门之前）', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  -- OneX 的平台 workspace 放行。name 与 onex 后端 model.WorkspaceAuthzRuleName
  -- 逐字节相同：OneX 开通 agent 时会对同一个名字先 GET 再 POST，所以两边收敛到
  -- 一条，而不是各写一条谁也不知道重复的规则。resource_name 是精确的 "onex"（不
  -- 用通配），因为这条只该覆盖 OneX 的 workspace。
  ('onex-workspace-grant', 'user', '["*"]', 'workspace', '["onex"]',
   '', '', '["read"]', 'allow', 'enabled',
   'OneX 平台 workspace 放行（workspace 门在 agent 门之前）', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  -- OneX 控制台的运营助手所用 MCP server 的引用放行。
  --
  -- # 为什么必须有这一条
  --
  -- 引用检查与其它维度一样是 fail-closed 的：没有规则就是 NoOpinion，NoOpinion
  -- 折成 Deny。所以一个 agent 声明了没人写规则的 MCP 时，运行会以
  --   `mcp:<name>: authorization: all levels NoOpinion (fail-closed)`
  -- 被拒——2026-10-12 OneX 接入运营助手时正是踩在这条上（first 403 on the
  -- console's first message）。
  --
  -- # name 与 resource_name 都由 OneX 侧写定
  --
  -- 规则名 = `<mcp 名>-grant`，与 onex 后端 model.MCPGrantRuleNameFor 逐字节相同
  -- （OneX 先 GET 再 POST，两边收敛到一条）；resource_name 是精确的
  -- "onex-console"，因为这条只该覆盖控制台那一个 server。subject 是 ["*"]，理由
  -- 与 onex-workspace-grant 相同：它放行的是**服务器**，而「谁可以运行带这个
  -- server 的 agent」由每个使用者各自的 agent 规则决定——那才是会因人而异的闸。
  ('onex-console-grant', 'user', '["*"]', 'mcp', '["onex-console"]',
   '', '', '["read"]', 'allow', 'enabled',
   'OneX 控制台运营助手所用 MCP server 放行（MCP 引用检查实际是 fail-closed 的）', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  -- 与上一条同形：OneX 的课程目录 MCP server，**每个会员 agent** 都会引用它
  -- （每用户 onex-u-<uid>、共享的 onex-x-max-tier 与 onex-x-public）。
  --
  -- 它比上一条更必要，不是因为更危险，恰恰是因为覆盖面更广：控制台那条只有运营助手
  -- 一个 agent 引用，漏了就是那一个功能不工作；这条漏了，是**每一个**会员 agent 的运行
  -- 都被拒（`mcp:onex-courses: authorization: all levels NoOpinion`）。
  --
  -- OneX 侧在 reconcile 路径上幂等创建同名规则（model.MCPGrantRuleNameFor），所以这条
  -- 种子是给全新库用的；两边收敛到同一行。
  ('onex-courses-grant', 'user', '["*"]', 'mcp', '["onex-courses"]',
   '', '', '["read"]', 'allow', 'enabled',
   'OneX 课程目录 MCP server 放行（会员 agent 的只读工具面；MCP 引用检查是 fail-closed 的）', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  ('allow-fz-default-agent-skills', 'user', '["*"]', 'skill', '["*"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-default agent 引用的 Skill 放行', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  ('allow-fz-default-agent-kb-refs', 'user', '["*"]', 'knowledgebase', '["*"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-default agent 引用的知识库放行', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  -- ── fz-public ────────────────────────────────────────────────────────────
  --
  -- 对外受众（含本租户以外的人）用的内建 agent。
  --
  -- **它与 fz-default 的差别只有一条：这里没有 MCP 的 allow 规则，并且第 12 行
  -- 显式 deny 掉 fz-public 对任何 MCP 的引用。**
  -- fz-default 把 zke-kubeagent / zadig-mcp 这类运维工具放给所有主体，是因为
  -- 它服务的是内部值班；而 fz-public 会被路由给**机器人认不出是谁**的人，把运
  -- 维工具链交给他们是不行的。
  --
  -- ⚠️ 这条规则里的 resource_name 必须与 internal/pkg/known/chat 的
  -- PublicAgentName 逐字相同，agent 行也要存在（本文件只管规则，agent 由控制台
  -- 或管理接口创建）。
  ('allow-fz-public-agent', 'user', '["*"]', 'agent', '["fz-public"]',
   '', '', '["read"]', 'allow', 'enabled',
   '对外受众可使用内置 fz-public agent', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  ('allow-fz-public-workspace', 'user', '["*"]', 'workspace', '["*"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-public agent 所属 workspace 放行（workspace 门在 agent 门之前）', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  ('allow-fz-public-tools', 'user', '["*"]', 'tool', '["*"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-public agent 的工具调用放行（不再转人工审批）', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  ('allow-fz-public-skills', 'user', '["*"]', 'skill', '["*"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-public agent 引用的 Skill 放行', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  ('allow-fz-public-kb-refs', 'user', '["*"]', 'knowledgebase', '["*"]',
   '', '', '["read"]', 'allow', 'enabled',
   'fz-public agent 引用的知识库放行', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  -- ── 唯一的 deny 规则，也是唯一 agent 作用域的规则 ─────────────────────────
  --
  -- 全表就这一条 deny，是为了把"机器人认不出是谁"的那条路径与运维工具链切开。
  --
  -- # 为什么必须是 deny，而不是"不给它写 allow"
  --
  -- MCP / Skill / KnowledgeBase 引用走的是
  -- `Coordinator.AuthorizeResourceRefs`，与其它维度一样 fail-closed（见
  -- TestAuthorizeResourceRefsIsFailClosedWhetherOrNotAUnionIsInFront）。所以：
  --   · 上面那条 `allow-fz-default-agent-mcp-refs` 是全局的——它把 zke-kubeagent
  --     放给 `subject_id=["*"]`，fz-public 引用同一个 MCP 时命中的正是它；
  --   · 今天把那条 allow 删掉，fz-public 确实会变成 NoOpinion → Deny，但那是
  --     **依赖别人不写全局 allow**，下一个人补一条 `["*"]` 就静默放开了。
  -- 显式 deny 表达的是"这个 agent 不许碰这个资源"，而 deny 要只对 fz-public
  -- 生效，就需要 agent 这一维。
  --
  -- # 为什么 resource_name 是 ["*"] 而不是那四个运维 MCP 的名字
  --
  -- 列名字等于要求"每新增一个运维 MCP 都要记得加进这张单子"，而漏掉的那一次
  -- 不会报错，只会静默放开一条本该被拒绝的引用（全局 allow 一写就命中）。
  -- ["*"] 把不变量说成一句不会腐烂的话：**fz-public 不引用任何 MCP**。它的
  -- mcps 列本来就是空的，所以今天这条规则（几乎）不会被触发——它存在的意义是
  -- 让"mcps 为空"从**数据属性**变成**授权属性**：谁往 fz-public 的 mcps 里加
  -- 名字，都不会因此拿到工具链。
  --
  -- # 它不覆盖 tool 调用
  --
  -- resource_kind 是 mcp，所以只拦 MCP 引用。tool 级检查是 fail-closed 的，且
  -- 转人工审批而非 403；那条路径由 fz-public 自己的 mcps 为空 + 这里拦下的引用
  -- 共同保证——它引用不到 MCP，就没有 MCP 的 tool 可调。
  ('deny-fz-public-mcp', 'user', '["*"]', 'mcp', '["*"]',
   '', '["fz-public"]', '["read"]', 'deny', 'enabled',
   'fz-public 不引用任何 MCP：对外受众不得触达运维工具链', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000)),

  -- 控制台资源守卫的读基线：所有登录用户可读所有资源。
  --
  -- additive 模式下没有这条规则时，未匹配的行仍然可见（旧行为）；有它之后，
  -- 操作员可以只写 Deny 规则收紧某个资源/命名空间，不必给现有资源逐条补
  -- Allow。切到 authoritative 前必须存在这样一条基线。
  ('baseline-read-all', 'user', '["*"]', '*', '["*"]',
   '', '', '["read"]', 'allow', 'enabled',
   '控制台资源守卫的读基线：所有登录用户可读所有资源；用 deny 规则收紧', 'ops',
   FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000), FLOOR(UNIX_TIMESTAMP(NOW(3)) * 1000))
ON DUPLICATE KEY UPDATE
  `subject_type`        = VALUES(`subject_type`),
  `subject_id`          = VALUES(`subject_id`),
  `resource_kind`       = VALUES(`resource_kind`),
  `resource_name`       = VALUES(`resource_name`),
  `resource_attributes` = VALUES(`resource_attributes`),
  `agent_names`         = VALUES(`agent_names`),
  `verbs`               = VALUES(`verbs`),
  `effect`              = VALUES(`effect`),
  `status`              = VALUES(`status`),
  `description`         = VALUES(`description`),
  `owner`               = VALUES(`owner`),
  `updated_at`          = VALUES(`updated_at`);

-- Rollback: restores the fail-closed baseline (every request denied).
-- DELETE FROM `onexai_agent_authz_rule` WHERE `name` IN
--   ('allow-fz-default-agent', 'allow-fz-default-agent-mcp-refs', 'allow-fz-default-agent-tools',
--    'allow-fz-default-agent-workspace', 'allow-fz-default-agent-skills', 'allow-fz-default-agent-kb-refs',
--    'allow-fz-public-agent', 'allow-fz-public-workspace', 'allow-fz-public-tools',
--    'allow-fz-public-skills', 'allow-fz-public-kb-refs',
--    'deny-fz-public-mcp', 'baseline-read-all',
--    'onex-workspace-grant', 'onex-console-grant');
