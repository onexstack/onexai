# Agent platform console (management-plane design and boundaries)

> The frontend lives in the `onex-console` repository's `apps/agent` (application name "智能体平台", attached to the OneX course platform under `实战项目 → Go 项目平台`). This document covers only the **server** side: the new routes, the authentication model, and a few deliberate boundaries.

## 1 Why this layer is needed

The entire `/v1` group of `oai-agent-apiserver` is guarded by AK/SK signatures, and a browser cannot hold a secret key; meanwhile 8 categories of resources (agent / skill / mcp / knowledge base / prompt / model / secret / authorization rule) could previously only be called service-to-service. To take a look at "which Skills exist on the platform, which knowledge bases a given agent is bound to", operations today can only dig through the database.

So a second surface is added: **the same resources, a different authentication scheme**.

```
浏览器（Bearer 令牌）
   ├── /v1/admin/*      → oai-agent-apiserver :9181   ← 本次新增（管理员）
   ├── /v1/auth/login   → oai-chat-apiserver  :9180   ← 唯一签发点
   ├── /v1/tickets      → oai-chat-apiserver  :9180   ← 本次新增（管理员，只读）
   └── /v1/oncall-groups、/v1/conversations …（JWT; duty-group reads are signed-in, writes are admin-only）
```

## 2 Two surfaces, two authentication schemes

| | Service-to-service surface | Management surface |
| --- | --- | --- |
| Prefix | `/v1/*` | `/v1/admin/*` |
| Authentication | AK/SK HMAC (`X-API-KEY`/`X-TIMESTAMP`/`X-NONCE`/`X-SIGN`) | `Authorization: Bearer` (HS256) |
| Principal | A service (`secretId`) | A person (`user_id`) |
| Authorization | none (signature is all the rights) | admin role |

Routes are registered on the two groups in `internal/agent/httpserver.go`, and middleware is **fixed into each route chain at registration time** — this is exactly the precondition for the two surfaces to coexist: management-surface requests never pass AK/SK validation (they cannot satisfy it), and service-to-service requests are never required to hold a token. The reverse also holds, so a management-surface route **cannot** fall outside the signed surface due to a missing line.

`internal/agent/handler/admin.go` and `admin_debug.go` use `RegisterAdmin` (a second registration table), not `Register`: `ApplyTo` attaches **all** registrars to the given group, and reusing it would register the entire service-to-service surface a second time under `/v1/admin`.

## 3 Authentication model: a single list, roles stamped at issuance

- **A single list, and it is not in configuration**: the SRE and admin lists are two rows of `chat.setting` (`onexai-sre-user-id` / `onexai-admin-user-id`, whose values are JSON arrays of Lark user_ids), cached by `internal/chat/pkg/roleset` and reread every minute; `admin = admin row ∪ SRE row`. Both surfaces (the Feishu/MCP `StaticRuntime` and the token-issuing `AuthBiz`) read **the same cache instance**, so there is never a state where "you are an admin on the Feishu surface but not on the management surface". Previously they came from `lark.sre-user-ids` / `chat.roles.admin-user-ids`, and changing them required editing the config and restarting.
- **Stamped at issuance**: `POST /v1/auth/login` looks up this cache by login identity and writes the result into the token's `is_sre` / `is_admin` claims. Downstream services read only the claims, not the list — **the role in the token is the role at issuance time**, independent of later changes to the list (`jwt.expired` defaults to 24h).
- **The two claims must not be merged**: the two sets are stored separately in the cache, because `x-sre-only` and `x-admin-only` gate different commands, and merging would turn the former into the latter. `RequireAdmin` in `internal/pkg/authn/bearer` therefore looks only at `is_admin`.
- **Empty table = nobody** (fail-closed): without the row there is no such role; `/doctor` is the only entry point that can show the current list sizes and read time.
- **Tokens work across services**: the two services' `jwt.key` must be the same value (this is noted in the config comments). Previously the four key values differed, so a token signed by chat would not validate on agent.

### 3.1 This identity scheme is **not** verification-based; do not treat it as authentication

None of the three login providers can verify credentials: `local` accepts any non-empty username and password, `lark` treats a string as an identity, and `apikey` likewise. So logging in proves "you know a name that the deployer listed in the list".

This was not introduced now, but **attaching it to the browser surface changed its weight**: previously a REST caller's `is_sre` was always false (the list was never read over REST), so this gap had no outlet; now it issues tokens carrying `is_admin`. Production must connect a real identity source (Feishu SSO, or trusting OneX's iam), otherwise the management surface is effectively open. Locally it is used because `chat.login.allow-local` is enabled only in that local config, and startup prints an INSECURE warning.

## 4 Debug surface (`/v1/admin/agent/*`)

`POST /v1/admin/agent/ask|approve|stop`, same shape and origin as `/v1/agent/*`, with three differences:

1. **Shares `AgentAskRequest`, but does not accept server-owned fields**. The three ask entries (`/v1/agent/ask`, `/v1/admin/agent/ask`, `/v1/user/agent/ask`) use one request message: content in `input`, targets in `selection`, generation parameters in `generation`. A browser that submits `subject`/`actor`/`sessionId`/`metering`/`options`/`metadata`/`model`/`selection.invokedSkills`, or any `generation` parameter other than `reasoning`, is refused with 400; the execution principal is derived server-side from the login or the agent's `owner`. The boundary has one implementation: `internal/agent/pkg/validation/console_ask.go`. The original surface could put `senderID` in the request body because the caller was a trusted service backed by AK/SK; the browser has no such precondition.
2. **Two identities recorded separately**. The run **executes** under the agent's owner (otherwise it would not even pass its authorization gate — one rule per user, and the admin has no rule), while the **audit** records the admin who pressed the button. `AskRequest.ActorID/ActorName/ActorIsSRE` exists for exactly this; without the distinction, the audit would attribute the admin's action to a user who did not do it.
3. **The runID is minted server-side with a `dbg-` prefix**. `/approve` and `/stop` accept only this prefix, because those two endpoints accept arbitrary runIDs and the deployment also holds real users' sessions — one mistaken approval would approve a tool call in someone else's session.

**Not persisted, not billed**: does not write `chat_session`/`chat_message` (debugging is no one's session) and does not write usage. The `interaction` audit records one row (`Source = "console"`) — an admin running as the platform identity is something that must be traceable.

Event shape: upstream uses **numbers** 0..10 for the type (`AgentStreamEvent.Type`, where the number is the enum's declaration order). The management surface maps it to a string union before sending; the mapping is in `internal/agent/pkg/agentstream/console.go` (the second `StreamSink`); the two streams thus have their own sinks, and `handler` shares only the connection/timeout part (`streamWithSink`).

| String event | Meaning |
| --- | --- |
| `delta` | Incremental text; `reasoning` distinguishes the reasoning stream from the answer |
| `step` | Tool step (name/status/elapsed/tokens/id/**result**) |
| `usage` | Cumulative usage (sent once separately before `Done`) |
| `interrupt` | Approval/question pause: this stream segment ends, **the run does not end**; call `/approve` based on this |
| `suspended` | Stream terminated due to a pause. Kept separate from `interrupt` because the word "ended" would cover up the only action still available in the UI |
| `final` | Terminal state; when stopped, `text` is empty, meaning "no more content" rather than "the answer is empty" |
| `error` | Terminal failure |

**`suspended` is a tier upstream does not have.** Upstream uses `EventSuspended` to mean "the stream ended but the run is waiting for an answer"; in numeric form it can only be distinguished from `EventDone` by the enum value; giving it a name in the string union is what allows the frontend not to render "pause" as "end".

**Tool `result` is sent on the management surface but not on the chat surface**: chat's sink converts steps into a generated proto message, which has no result field; the management surface maps it itself, so it can include it. `agent.ToolStep.Result` is populated on both sides (see its comment), so this is a shape difference, not missing data. Screenshot-style evidence: in a local live test, the debug run was asked for the time and the answer contained "刚才查到的是 2026-10-06 16:41，时区 +08:00" — that is the tool result entering the model context, and it also arrived at the browser in the `step` event.

## 5 Things deliberately not done

- **No ticket write surface**. The ticket lifecycle belongs to duty commands and Lark cards (`/open` `/hand` `/close` `/mark`), and those paths also mirror it to the event platform (Regen). A second writer would make the two sides inconsistent, and **neither side would notice** — the mirror is one-way by design. So `GET /v1/tickets` is read-only.
- **No change to Agent and knowledge-base read/write capabilities**. Their CRUD has always been on the service-to-service surface, and the management surface exposes them **symmetrically** (otherwise it would be security theater); but the console renders them as read-only, because OneX-side reconciliation (membership tier changes, content sync, next-round conversations) rewrites `prompts`/`namespace`/`status`/`knowledgeBases`. To truly forbid it, the biz permission check would have to change — that is a product decision, not something this layer can make for it.
- **No isolation added to the conversation surface**. `/v1/conversations` returns all groups the bot is in (the server discards the caller's identity), and `/v1/search` matches the whole database. These two facts are **written on the frontend page**, rather than avoided by not displaying them.
- **No Casbin**. The README says "Casbin-based authorization", but Casbin appears only in boilerplate, generator templates, and RAG corpora; `RoleUser`/`RoleAdmin` in `internal/pkg/known/role.go` are referenced nowhere in the repo. The real role model is the two lists in §3.

## 6 Verification (already run locally)

Temporary instances (chat :9280 / agent :9281, with a separate HOME to avoid the single-instance lock), using `-c` to point at a config copy with changed ports:

| Request | Result |
| --- | --- |
| `GET :9281/v1/admin/agents` (no token) | 401 |
| Same (non-admin token signed by chat) | 403 |
| Same (in-list token signed by chat) | 200, `{"agents":[…],"count":N}`, millisecond timestamps |
| `GET :9281/v1/agent/agents` (no AK/SK signature) | 401 (service-to-service surface unaffected) |
| `GET :9280/v1/conversations` (was always 403 before fixing `AlwaysDeny`) | 200 |
| `GET :9280/v1/tickets` (admin / non-admin) | 200 / 403 |
| `POST :9281/v1/admin/agent/{approve,stop}` (runID without `dbg-`) | 400 |

Unit tests: `internal/pkg/authn/bearer` (including "no claim must fail closed"), `internal/agent/handler` (the management-surface route set is a subset of the signed surface + every management-surface route returns 403 to non-admins), `internal/chat/biz/v1/auth` (role stamping for in-list/out-of-list).

### 6.1 Browser walkthrough (Playwright driving local Chrome)

The sub-application started independently (:3010, with `PUBLIC_ONEXAI_{CHAT,AGENT}_BASE_URL` pointing at the temporary instances' :9280/:9281, **without touching the running 9180/9181**), 18 items all passed, no console errors, no failed requests:

- Logged out renders the login page and gives a declarative identity hint; after login the sidebar has both groups with all 11 entries;
- Agents 10 rows, tickets 9 rows, duty groups 5 rows, conversations 200 rows, all **real data**;
- Agents are read-only (no create entry), Skills have a create entry and the dialog fields come from the descriptor;
- Agent detail has three tabs (basic info / related configuration / **debug**), with the reason for read-only written on the page;
- The conversations page explicitly states "no ownership isolation".

The debug surface ran end to end for real: enter a question → `POST /v1/admin/agent/ask` 200 → panel renders, model answer and usage (input 486/output 121/reasoning 81/calls 1) → "已结束". Stop path: `POST /v1/admin/agent/stop` 200, **the partial answer is preserved** (this was fixed in this round: the `final` event has empty text when stopped, and directly overwriting would erase what the model had already said).

The walkthrough also caught a real defect typecheck could not: when a descriptor declares an `idKey` field also as an extraColumn, it shares the same `key` as the framework's "ID/name" column, and antd deduplication **drops the entire column** — so the list had no link into the detail view. Fixed (and the exception documented in the `FieldSpec.column` comment).

### 6.2 Two things to know when driving this UI (saves time next time)

- **antd turns two-character button names into "登 录"** (`autoInsertSpace` inserts a space between two Chinese characters); to find buttons by name, use a regex tolerant of spaces.
- **For an antd button with an icon, the accessible name includes the icon's `aria-label`**: the "发送" button is actually named "send 发送", so anchored matching finds nothing.
- `Alert`'s `message` is deprecated in antd 6 (the app side has all been changed to `title`).

## 7 The ordinary-user surface

The console is no longer only a management surface: a signed-in user must be able to browse
the platform catalog, create their own resources and talk to them.

| Surface | Prefix | Authentication | Capability |
| --- | --- | --- | --- |
| Declarative resources | `/v1/agent/*`, `/v1/core/*` | Bearer (any signed-in user) | Reads the whole catalog; writes are owner-scoped |
| User run surface | `/v1/user/agent/*` | Bearer (any signed-in user) | Runs as the caller, subject to the run authorization gate |
| Management surface | `/v1/admin/*` | Bearer + `is_admin` | As before; debug runs execute as the agent's owner |

- **Authentication and authorization are split**: `UnifiedAuth` answers only "who are you";
  its Bearer branch no longer requires an administrator. The policy lives in
  `internal/agent/handler/user_guard.go` (`UserGuard`), so one route group can serve both the
  shared catalog and the governance resources.
- **Governance stays administrator-only**: `/v1/core/secrets` and
  `/v1/agent/authorizationrules` are refused by prefix in `governancePaths`; the frontend menu
  hides the credential, access-control and user-management entries to match.
- **Writes are owner-scoped**: resources with an `owner` column (agent, model, namespace,
  runtimeprofile, scheduledrun, webhook) can only be changed by their owner or an
  administrator. On create the server stamps the caller as the owner, so the rule does not
  depend on a well-behaved client. Resources without an owner column (skill, MCP, knowledge
  base, prompt, …) are shared building blocks, writable by any signed-in user.
- **An owner may run their own agent**: `authorizeRun` skips the agent-level policy check for
  the owner, but the skill/MCP/knowledge-base reference checks still apply — owning an agent
  is not owning what it references. An agent created from the console is therefore usable
  without an extra `oai_authz_rule` row.
- **Two run surfaces**: administrators keep `/v1/admin/agent/*` (executes as the agent's owner,
  which is what debugging somebody else's agent needs); ordinary users use
  `/v1/user/agent/*` (executes as themselves). The run-ID prefixes differ (`dbg-` / `usr-`),
  and approve/stop accept only identifiers their own surface minted.

## 8 Not yet done

- **The production gateway has no route for onexai**. `/onex/agent/*` points at the OneX platform's `ox-agent-apiserver:8188`, which is unrelated to onexai and cannot be reused. Local development uses the sub-application's built-in proxy (`apps/agent/rsbuild.config.ts`).
- **There is no admin row in the local `chat.setting`**. The list is now two rows in the table (see §3), and "no row = nobody", so the management surface and admin commands like `/agent` reject everyone. To use it, write one of the two rows first:

  ```sql
  INSERT INTO chat.setting (key, value, description, created_at, updated_at)
  VALUES ('onexai-admin-user-id', '["你的user_id"]', '管理员 user_id 列表(JSON 数组)',
          extract(epoch from now())::bigint, extract(epoch from now())::bigint)
  ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at;
  ```

  Takes effect within a minute, no restart needed; `/doctor` prints the current count and list read time.
- **The management surface has no API to read/write this list yet**. The store layer's `SettingStore` (Get/Set) is in place, but `/v1/admin/settings` has not been built, and it has a bootstrap problem to solve first: authentication itself reads this list.
