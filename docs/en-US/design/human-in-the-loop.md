# Design: Human-in-the-Loop

> Status: implemented
> Scope: `oai-agent-apiserver` (eino/adk interrupt-resume + checkpoint) + `oai-chat-apiserver` (Lark card / plain text / HTTP / OpenAI-compatible path)
> Reference: eino-examples `adk/human-in-the-loop` (`InvokableApprovableTool` + `runner.ResumeWithParams`); cc-connect `core/engine.go` (`pendingPermission` three-level degraded rendering + `perm:`/`askq:` callback vocabulary)

## 1. Background and goals

Within an agent turn, some tool calls cannot be decided by the model alone: deleting a cluster, releasing a version, touching production data. Human-in-the-loop must answer four questions:

1. **How do we know a tool needs a human (trigger determination)**: tools must not declare it themselves, otherwise third-party MCP tools could never enter the flow.
2. **How to ask (presentation)**: a card with buttons in Lark; plain text, HTTP, and the OpenAI-compatible path outside Lark.
3. **How to wait (state)**: a person may answer minutes later or from another machine; waiting cannot be tied to a process or an SSE connection.
4. **How to continue after the answer (resume)**: approval/denial continues the same turn, not a new round.

Design principles: **determination is not in the tool, it is centralized in middleware**; **an interrupt is a suspension, and an answer is a resume**.

## 2. Trigger determination: how a tool is judged to need human involvement

### 2.1 Union of three declaration sources

| Declaration source | Location | Granularity |
|---|---|---|
| `agent.require-approval` | `pkg/options/agent_options.go` | server-wide default |
| `AgentM.RequireApproval` | `internal/agent/types/resource.go` (`agent.agent` row) | per-agent override |
| `MCPM.RequireApproval` | `internal/agent/types/resource.go` (`agent.mcp` row) | all tools brought by one MCP server |

Patterns are exact names (`delete_cluster`), prefix globs (`delete_*`), or match-all (`*`). The union cannot be narrowed downstream: an agent cannot exempt itself from gates declared by the MCP server it uses, otherwise the server's declaration would be meaningless.

**There is no exemption table**: don't list it if it doesn't need approval; if an entire server needs approval, use `["*"]`.

### 2.2 A single middleware centralizes it

`approvalHandler` in `internal/agent/biz/v1/agent/approval.go` is attached to the agent's tool node (`adk.ChatModelAgentMiddleware.WrapInvokableToolCall`) and is the sole entry point for all tool calls — built-in and MCP tools alike. It looks up the policy by `tCtx.Name`: only `DecisionAsk` interrupts; otherwise the original endpoint is handed back untouched.

The benefit is **zero changes for third-party tools**: one resource record change takes effect, no tool changes, no tool-list changes, no redeployment.

### 2.3 Relationship with `ask_user_question` (conceptual separation)

- **Approval (permission)**: policy adjudication for a tool that "was going to execute anyway", implemented centrally by middleware.
- **Elicitation**: the tool's contract is "return a human answer" (`ask_user_question`), which inherently belongs to the tool itself; middleware must not interfere.

Therefore `ask_user_question` keeps self-interruption, and its reserved tool name (`EngineConfig.Reserved`, same source as `mcpclient.Options.Reserved`) is always `DecisionAllow` in policy — otherwise it would ask the human "should we ask the human".

### 2.4 Adding a gate to a tool (operations action)

Row-level declarations modify DB rows, not code or config, so use `hack/mcpctl`:

```bash
# /pipeline 现在需要人类批准（oai-chat-apiserver 这行 MCP 服务端发布的工具）
MCP_NAME=oai-chat-apiserver MODE=set-require-approval=pipeline go run ./hack/mcpctl
MCP_NAME=oai-chat-apiserver MODE=show go run ./hack/mcpctl   # 读回
```

Three notes:

1. **The pattern is the name the model sees**, not the MCP wire name. The agent runs `SanitizeName` on every wire name before exposing it (`pkg/mcpclient`), and policy matches the result of that — i.e. the name the user sees on the card. `/pipeline` goes through cobrax's `toolName` (the command path minus the root command name) to get `pipeline`, with no rewriting needed, so write `pipeline` directly.
2. **Tools with the same name get renamed**: when names collide across servers, `Uniquify` renames the later one to `pipeline_2` (and logs a Warn). Then a gate written as `pipeline` misses; change the declaration to the new name from the log. This is the cost of a union policy matching only by name; renaming is safer than loosening to a wildcard.
3. **`MODE=apply` does not touch this column**: re-registering a server (changing url, changing tunnel port) should not incidentally remove a gate someone else installed, so removing a gate must explicitly use `MODE=clear-require-approval`. Effect timing is the same as other row changes: `store.refresh-interval` (5m) + `mcp-cache-ttl` (5m); restart oai-agent-apiserver to take effect immediately.

## 3. Layering

```
资源声明                      biz 解析              engine handler 链          渲染 / 答复
MCPM.RequireApproval    ─┐
AgentM.RequireApproval  ─┼→ approval.Policy  ─→ approvalHandler ─→ 中断 ─→ 结束本次 SSE ─→ chat
agent.require-approval  ─┘   （并集，biz）        （唯一收口）        │                    ├ Lark 卡片（同卡接续）
                                                                     │                    ├ 纯文本回复
                                             runner.ResumeWithParams ◄┴── POST /v1/agent/approve（SSE）
                                                        ▲                                  └ OpenAI 兼容路径（拒绝）
                                             Redis CheckPointStore（runID 为 key，TTL）
```

- Policy determination belongs to biz: `resolveApproval` (`biz/v1/agent/agent.go`), isomorphic and colocated with `resolveMCPs` / `resolveInstruction`; the result goes into `RunOptions.Approval`.
- Interception belongs to engine: the handler chain in `einoengine.go` is `[toolTrace, approval, contextGuard, summarization]` (`handlers[0]` is outermost; `toolTrace` must be on the outside so that after the interrupt is reported through it, that step remains `pending`).

## 4. Interrupt and resume

### 4.1 An interrupt is a suspension

After the engine receives `Action.Interrupted`, it emits `EventInterrupted` (carrying all pending requests for this turn), then emits the **terminal event `EventSuspended`**, and then ends this SSE. `EventSuspended` stands alongside `Done/Failed/Stopped` but has different semantics: **the run has not ended**, and the renderer must not treat it as a final result.

The checkpoint is written by eino's runner **before** the interrupt is emitted, so "suspension" is inherently cross-process and cross-replica.

### 4.2 Checkpoint and RunMeta

eino's checkpoint stores only execution state, which is insufficient to rebuild a run: instruction, tool set, and approval policy are all derived from the run's agent selection, and the selection is not in the checkpoint. So a **sibling key** is stored in the same `Store`:

| key | Contents |
|---|---|
| `<prefix>:agent:cp:<runID>` | eino's checkpoint blob |
| `<prefix>:agent:cpmeta:<runID>` | `RunMeta{Agents, SessionKey, UserMessage, UserID, Reasoning, AllowAll, ApprovalFallback, RequestID, Pending}` |
| `<prefix>:agent:cpclaim:<runID>:<requestID>` | one-time answer marker (`Claim` = SetNX, first replica wins) |

On resume, `RunMeta` is used to rebuild `RunOptions` verbatim (including `ApprovalFallback`): inconsistent rebuilding is not a minor flaw — the checkpoint records interrupt points by address, and if the tool set changes, the resumable point cannot be found.

`Store` interface: `Get/Set/Delete/PutMeta/GetMeta/Claim`. Two implementations, Redis and in-memory (`internal/agent/pkg/checkpoint`).

### 4.3 One decision covers the whole group

Multiple tool calls in one assistant message produce a composite interrupt. eino's `ResumeWithParams.Targets` is a map, and the documentation explicitly requires that **untargeted leaves must re-interrupt themselves**. Since resume reruns the ToolsNode as a whole, approving one by one would replay already-approved tools, creating semantic ambiguity.

So: **one decision covers all root causes of this interrupt**, and the card must list every pending tool individually so the decision is informed.

### 4.4 The middleware's four states

| State | Condition | Behavior |
|---|---|---|
| First entry | `!wasInterrupted` | `tool.StatefulInterrupt(ctx, PermissionInfo{...}, argsInJSON)` |
| Approved | `isTarget && hasData && Approved` | Call the original endpoint with **the arguments stored at interrupt time** |
| Denied | `isTarget && hasData && !Approved` | Return an ordinary tool result string ("user denied execution… do not retry"), letting the model explain and wrap up |
| Not targeted | `!isTarget` | **Re-interrupt** (otherwise the signal is swallowed in parallel scenarios and the run hangs forever) |

Targeted but without data (bare resume) is treated as denial — this engine always replies to the permission address with an `ApprovalResult`; reaching here means the answer was lost or malformed, and running the tool would mean executing without permission.

**Denial is an answer, not a failure**: the denial reason returns to the model inside the tool result, and the model explains it in its own words; this turn does not interrupt.

### 4.5 Fail-closed and idempotency

- **Fail closed**: when policy requires approval but there is no checkpoint store / no way to ask, always deny with an explanation, never silently allow (this is also the origin of the "if there are tools, there must be a store" guard in `prepare`).
- **Idempotent**: via checkpoint + claim. After the first resume, both checkpoint and claim have been consumed; repeated clicks / clicking an old card yield `ErrNotPending` ("already handled or expired"), replacing the old implementation's on-demand gate that could silently succeed.
- **Timeout**: there is no longer a blocking `Wait`; timeout semantics live in `agent.checkpoint-ttl` (default 24h) — it is the only garbage collection for such runs and must be longer than the slowest answer.

## 5. Protocol

`pkg/api/agent/v1/agent.proto`:

```proto
// 中断事件：requestID 标识"这一次"挂起，requests 是本次全部待批工具。
message AgentInterrupt {
    string requestID = 1;
    repeated AgentInterruptRequest requests = 2;
}
message AgentInterruptRequest {
    string address = 1;    // 该中断点的地址，答复按地址定位
    string kind = 2;       // permission | question（缺省即 permission）
    string toolName = 3;
    string question = 4;
    repeated string options = 5;
    bool multiSelect = 6;
    string arguments = 7;  // 模型打算传给该工具的 JSON 原文（见 §6.2）
}

// 答复即 resume：runID + requestID 定位，decision/answers 是内容，reason 是拒绝理由。
message AgentApproveRequest {
    string runID = 1;
    string decision = 2;              // allow | deny | allow_all
    string requestID = 3;             // 必须与当前挂起的 requestID 一致
    map<string, string> answers = 4;  // question 类中断按 address 给文本
    string reason = 5;
}
```

- `POST /v1/agent/approve` changes from "returning JSON" to **returning SSE**, symmetric with `POST /v1/agent/ask`: the answer triggers a resume, and continuation events come back on this stream. `AgentApproveStream` in `internal/pkg/clientset/agent` is symmetric with `AgentAskStream`.
- `AgentEventType` gains `agentEventTypeSuspended = 10` as the suspended terminal.
- The decision constants (`allow` / `deny` / `allow_all`) are a **cross-service wire contract**: `agent/pkg/agent` and `chat/pkg/agent` each hold a copy of the literals, with both sides pinned by `TestDecisionValuesAreWireValues` / `TestApprovalVocabularyIsWireValues`. This mirroring is intentional — the two binaries release independently, and sharing a Go constant would mean one side's upgrade silently changes the semantics for the other.

## 6. chat: one turn one card, decisions in place

`aiTurn` in `biz/v1/bot/ai_turn.go` distinguishes **suspension** from **ending**:

- `EventInterrupted` → record `PendingPermission`, **draw the decision on its own card**, `setSuspendedTurn`, stop the pending row's timer; do **not** finalize, do not write the Done footer.
- `EventSuspended` → merely "this stream has ended": the card stays as is, waiting for the answer.
- After the answer, continuation events come back through **the same sink** (the port keeps the sink on the backend session, see the comment on `Session.AnswerPermission`), so the second stream segment renders into the same card and finalizes on the real `EventDone`. A turn awakened by an answer calls `rearm()` to get a fresh deadline: a run answered after a timeout still needs somewhere to put its answer.

Card callbacks must return within 3 seconds (Lark callback budget), so a click only "delivers the answer" and returns immediately; continuation is pumped in the background.

### 6.1 Why "one card" (the origin of this rework)

The previous shape was **two cards that did not know about each other**: the streaming card was deliberately left unfinalized when suspended ("正在思考中，请稍候 ……" still being written, the stop button still clickable), while the decision was sent as **a separate message** via `SendCardModel` (approval card or question card). So one sentence produced two cards in the group, and readers did not know which one was live or whether they could continue speaking — this is "card confusion".

Now **all state of a turn is on this one card**, and the decision area is part of it, inserted before the stop-button row, forming the only live decision surface (following the existing invariant: approval first, question interrupts afterwards, never two answerable surfaces):

```
[卡片]  ▸ 深度思考过程（折叠，标题固定）
        ▾ 工具调用
          ○ pipeline · 等待人工确认        ← 暂停时不再计时
        —— 正文（模型输出，流式）——
        ? **需要你的批准**：pipeline（env=edu-onex service=content）
        [✓ 允许]  [✗ 拒绝]
        [✓ 总是允许]
        [停止]                            ← 右对齐，仍在最底
```

### 6.2 The decision area's two elements

Placed before the stop-button row (`CardElement.Create` + `insert_before` + `target_element_id`):

| Element | Shape | Description |
|---|---|---|
| `decision_log` | markdown, **append-only** | Pending = request description; decided = append the conclusion as a line; two pauses in one turn produce two lines, reading as a timeline |
| `decision_row_<n>` / `decision_grant_<n>` | column_set (buttons) | Approval = allow / deny; second row = always allow; elicitation = one button per option (reusing `askq:<i>`); **exists only while pending**, deleted on answer — "no pending, no buttons" is how "no dead buttons" is implemented. Ids must be short: Feishu caps `element_id` at 20 characters, validated when **inserting components into an existing card** (see §6.8) |

```
? **请选择**：要发布到哪个环境？
✓ 已选择：edu-onex · 14:32
? **需要你的批准**：pipeline（env=edu-onex）
✗ 已拒绝：pipeline · 14:33
```

- Each pause uses a new pair of ids (`<n>` = the pause count within that turn), avoiding the semantic ambiguity of "delete then reuse the same id".
- The **arguments** in the log line come from the protocol's new `arguments` (§5): `einoengine` fills it from `PermissionInfo.Arguments`, and on the chat side `larkcard.ArgumentsDigest` compresses it into a one-line summary (top-level scalars as `k=v`, nested objects as `{}`, lists joined by commas; **redact before truncating** to 80 characters). `ArgumentsDigest` returns an empty string for `{}` / `null` / all-empty members — "this call has no arguments to mention", rather than printing `null` on the line.
- When no decision was made (cancelled / expired / superseded), they all share `○` and are distinguished by text; what they have in common is precisely "this decision was not made and the tool did not execute".

### 6.3 Four states

| State | Trigger | Card change |
|---|---|---|
| **Paused** | `EventInterrupted` | Write the decision area (request + buttons); clear the body placeholder (**only if the body is still empty**); tool row changes to `○ 等待人工确认` and the timer stops; summary changes to `? 等待你的选择`; **no new message** |
| **Answered** | Button click / text answer | Delete buttons and append the conclusion line only **after** delivery succeeds; body continues writing into the same card; tool row timer restarts; finalize only on the real `EventDone` |
| **Takeover** | A new message arrives while pending | Cancel the run **still parked on a decision** (the suspended tool **does not execute**) → write `○ 已取消：收到新消息` and delete buttons → finalize this card as `已被新消息接管` (the footer states the reason, no longer sharing a bare `已停止` with `/stop`) → the new message opens a **new** turn and a new card |
| **Expired** | Wait exceeds `approvalWaitGrace` (30 minutes) | Write `○ 已失效：等待超时，本次执行未继续` and delete buttons; finalize the card as ended; late clicks for that run are thereafter rejected (`decisionExpired`) |

- **Superseded by a new pause**: the previous pause's buttons are still on the card and a new interrupt arrives — the backend (the CLI-style "stream stays open, the process itself holds the request" form) can interrupt again without waiting for an answer. The old buttons are then **withdrawn in place**, the log appends `○ 已被新的确认替代，本次执行未继续`, so the card does not keep a decision that cannot be clicked or delivered. This is the fourth exit path besides "expired"; the criterion is "the card still has live buttons" (`takePendingActions`), not time.
- **The takeover choice**: suspension releases the session slot, so a new message naturally starts a new turn — rather than pretending it won't happen, make it **allowed and explained**. The takeover action is performed in `runAI`, **before** `sess.Submit` / `SetCurrentRun`, otherwise it races with the old run's `ClearCurrentRun`.
- **Only runs still parked on a decision get taken over**: after resuming, a paused turn **remains** in the per-session suspended table (`clearSuspendedTurn` deletes only when the turn `finish()`es), so "it is in the table" does not mean "it is waiting for a person". Removing this criterion would stop a **currently executing** run — the tool the user just approved gets cancelled, the card finalizes as `已停止`, and a new card opens alongside it; this is exactly the origin of card 1 in the "one message two cards" incident. The resumed half does not hold the session lock (continuation in `pkg/agent` is detached, so a 3-second card click is not dragged into minutes), so new messages arriving during that half open their own turn **in parallel** rather than queueing — this is a known residual.
- **The expired state did not exist at all before this change**: on expiry only the turn's context died, and nobody watched `ctx.Done()`; the card just stood there with buttons forever clickable. Now `aiTurn` installs a `time.AfterFunc` per pause, and on expiry sends with a detached 2s context (the turn's ctx is already dead) and sets the `expired` flag so late clicks no longer revive the turn. The turn has already been deregistered by `finish()`, so "this run's wait has expired" is additionally recorded in the session-level `expiredPauses` (cleared by the next pause).

### 6.4 Message rules while pending

- Commands starting with `/` behave as usual (`/stop` is always available).
- Plain text + approval class: hitting the decision vocabulary → answer; an **explicit cancel word** (`取消` / `算了` / `cancel`, **not including `停止`**, which is `/stop`'s job) → cancel; otherwise → `false, "", nil`, falling back to the prompt tier, and `runAI` takes over as a new request.
- The vocabulary is matched by **whole-message equivalence after trim**, not substring/prefix — otherwise "允许，但别删 namespace" would be swallowed as an answer, while "先别允许，改成构建 X" could be misjudged as takeover.
- Plain text + elicitation class: with @bot → new request; otherwise treated as an answer (free text is a legitimate answer anyway).
- Images / other messages: treated as new requests.

### 6.5 Characters

Only five monochrome characters, **no emoji, hourglass, pause/stop squares, or colored checks**: `?` pending, `✓` allowed/selected/completed, `✗` denied/failed, `○` not made (cancelled/expired/superseded), `⟳` in progress. Defined in `larkcard` (`Glyph*`), shared by cards and text messages.
The characters on buttons can only be **bare characters** (card 2.0's `text` is `plain_text` and cannot hold `<font color=…>`); color is carried by `type` (allow = `primary`, deny = `danger`, always allow = `default`); `icon` is not in the button whitelist (`card2schema.go`) and is not used this time.

### 6.6 Capabilities, degradation, and the unverified parts

Adding components to a running card, then deleting them, is official cardkit capability (`CardElement.Create/Delete/Settings/Content`, sharing a strictly increasing `sequence`). At delivery:

- Card creation `config` adds `update_multi: true` — element-level interfaces require it not to be false, and it only affects multi-person collaborative updates.
- **Use element-level interfaces rather than whole-card `Card.Update`**: a whole-card rewrite resets the client's collapsed/expanded state, and the decision area must stay visible while paused. For the same reason `BatchUpdate` was not used (ambiguous envelope semantics): writing the log and deleting buttons are two calls.
- The decision area must survive finalization: `finish()` goes through `CloseStreamingCard` → `BuildRichCard`, rebuilding the whole card, which **wipes the decision area entirely**. So `rich.go` gains `BuildRichCardWithDecisions(...)`, with `BuildRichCard`'s signature unchanged and forwarding `nil`, so no existing call site or golden changes.
- **Every card write step is non-fatal**: failure is only logged, falling back to the old path of "send a separate decision card" (`sendApprovalCard` / `sendAskQuestionCard` are retained as degradation paths; removing one `inline` marker from the button value routes to the replacement-message path). Reason below.
- **The card write operations have not been verified on real devices**: this environment has no real Feishu tenant, so "insert components into the streaming card / keep writing the body after insertion / callback carrying `session_key`" are covered only by unit tests and goldens, with no real-device screenshots. If "a streaming card cannot have components added" holds on a real device, the degradation path takes over automatically, and the four-state rules are unchanged.
  > First live-run annotation on 2026-09-28: the element-level interfaces **do work** — the `decision_log` insertion on pause landed successfully on the card, and `update_multi` did take effect; the two approvals (`decision_actions_1_last` and `decision_actions_2_last`, one each) were not both drawn on the same card — the reason was not "a streaming card cannot have components added" but the component's own `element_id` exceeding the limit (§6.8). Another rule unique to real devices: `CardElement.Content` rejects **empty content** (`code=99992402`), so "clear the body placeholder" actually writes a zero-width space (`placeholderCleared`), not an empty string.
- The button value **does not carry `card_id`**: rewriting the decision log depends on in-process turn state (`suspended` / `expiredPauses`); carrying `card_id` would not buy cross-process rewriting and would add a field that can expire.

### 6.7 Other existing conventions (unchanged)

- **Multiple requests**: all pending tools are listed, and the copy states that one click decides all (allow = all execute, deny = none execute); `perm_*` values carry `run_id` / `req_id` / `perm_subject`.
- **Identity check**: `answererAllowed` is based on the initiator (`PendingPermission.AskedBy`), with SRE/Admin as fallback; two incomparable unknown identities (missing clicker, old record) are allowed rather than denied — denying would lock a run forever.
  > Note that `runtime.IsSREUser` / `IsAdminUser` are currently **pinned to true** (`TODO(role-gate)` in `pkg/runtime/static.go`), so the role branch currently allows, and what actually works is the initiator comparison. Restoring real determination is out of scope for this round.
- **Delivery failure does not touch the card**: the card is the only place that still has live buttons; if a failed delivery deleted the buttons, there would be no way to answer anymore.
- **The stop button on a parked card**: `Stop` must not only cancel the running run but also **delete the parked checkpoint** — otherwise an answer sent before stopping could arrive after the stop and execute the tool the user just stopped. Stop and takeover share one implementation (`stopRun`).

### 6.8 Two hard rules learned on real devices (2026-09-28)

The "not verified on real devices" of §6.6 got its first batch of measurements on 2026-09-28; two rules only speak on a real tenant:

1. **`element_id` has a 20-character cap**, validated at `CardElement.Create` (inserting components into an existing card):

   ```
   300315: elementID format error. Only alphabets, numbers, and underscores are
   allowed. It must start with an alphabet and not exceed 20 characters.
   ElementID:decision_actions_1_last
   ```

   The id at delivery was `<pattern>_<n>` plus a `_last` suffix (24 characters), so **every approval with an "always allow" row failed to insert**, `presentDecision` returned an error, and `pause()` took the degradation path → `sendApprovalCard` sent a separate message. Elicitation has no second row, is 19 characters and legal, so it worked — "same card" only held when there was no second button row. The ids are now `decision_row_<n>` / `decision_grant_<n>` (`decision.go`), short enough not to overflow even at thousands of slots.

   Such ids are built at runtime and **are not in any golden**, so offline validation must cover both paths: `Card2Violations` now checks every `element_id` (`TestCard2SchemaGuardCatchesAnElementIDThePlatformWouldRefuse`), and the test double `fakeLarkServices.InsertCardElements` runs the same validation on every insertion — all HITL unit tests therefore rest on this rule.

2. **`CardElement.Content` rejects empty content** (`code=99992402, msg=field validation failed`). Clearing the body placeholder on pause originally wrote an empty string, which never took effect on a real device (the card kept showing "正在思考中，请稍候 ……"). It now writes `placeholderCleared` (a zero-width space).

The same batch of measurements also confirmed that the premise §6.6 worried about **does not hold**: inserting components into a running card and continuing to write the body afterwards is feasible on a real tenant.

### 6.9 Two things the delivery layer relies on for "one message one turn" (2026-09-28)

The invariant of §6.1 (one message → one turn → one card) depends on two more things at the delivery layer. The cause of "one @ mention produces two cards" in the group on 2026-09-28 was that neither was done, and **each layer guessed on its own** (handler checking a probabilistic cache by event_id, session queueing when busy, biz stopping a "suspended turn" on sight) — all entry filters that can always be bypassed. Now the responsibility is centralized in one place.

1. **The WS message-receive callback must return immediately**. The SDK hands the event to the handler first and writes the response frame **after the handler returns** (`oapi-sdk-go/v3/ws/client.go`), while the WS callback originally ran the whole turn synchronously, so every turn that ran longer than Feishu's ACK timeout was **redelivered** by the platform. The logs line up: `aa619a7c…` 20:11:26 → 20:11:45, `ec7bdd72…` 20:12:51 → 20:13:11 (both first deliveries had `AI turn started` in the same second), while a card callback dispatched within 1 second in the same period was **not** redelivered. Now the WS callback dispatches asynchronously with `runtime.GoSafe`, like the webhook path (`handler/bot.go`), and `c.eventHandler.Do` returns immediately — redelivery no longer happens. **Falsifiable criterion**: `grep -c 'lark sdk \[receive message'` increments by only 1 per message (before the fix, every turn >20s incremented by 2). Other callbacks remain synchronous: a card callback's return value is the result given to Feishu (toast/card) and cannot be asynchronous.
2. **What is claimed is the message, not the event id**. Whether redelivery happens should not be up to us (platform behavior, process restart, another delivery channel), so the guard is placed where **turns can be produced, and only there**: `claimTrigger` at the start of `runAI` (`biz/v1/bot/admission.go`), keyed by `conversation + om_…`, TTL 1 hour, not evicted before expiry (group traffic cannot evict other conversations), failing open on empty `MessageId`. A hit returns immediately after a WARN + `opsassist_duplicate_triggers_total`, **without queueing** — a queued duplicate would still run a turn after the previous one ends. Now that the claim table is in place, the handler's ristretto gate (`handler/handler.go`) is no longer load-bearing, and losing a key is harmless; its implementation was not changed this round.

   Boundary: the claim table is in-memory ⇒ the precondition is **single replica**, shared with the session lock (`pkg/session`'s `currentRun`/`pending` are also in-memory and not stored). When scaling out replicas in the future, claiming must move into the store together with the session lock.

## 7. Non-Lark scenarios

| Surface | Implementation |
|---|---|
| Plain-text reply | Pending-approval funnel + decision vocabulary; answers go through `AnswerPermission`, and the continuation renders into the same card |
| HTTP/API direct call | `POST /v1/agent/approve` (SSE). Contract: `runID` + `requestID` must match the current pending ones, otherwise `ErrNotPending`; answers are one-time, and repeated calls also yield `ErrNotPending` |
| OpenAI-compatible path | Callers of `/v1/chat/completions` have neither a card nor an approve call, so it is handled by `agent.compat-approval-mode`: `deny` (default) does not execute the tool and passes the denial to the model as a tool result for explanation; `error` fails the request; `allow` applies no gate on that endpoint. The config value is validated at startup; a typo is not read as `allow` |
| CLI backend | The CLI process itself holds the IPC permission request, so the run does not `EventSuspended`: its stream stays alive, and subsequent events continue arriving on the original stream after the answer. The port's promise to callers is therefore phrased as "`EventInterrupted` is a suspension, `EventSuspended` is merely this stream ending" |
| TUI/Web approval frame | **Not done this round** (see §9) |

## 8. Three things discovered during implementation (all real bugs)

1. **Interrupt payloads must be registered as gob types**: checkpoints are gob-encoded, and `PermissionInfo` / `AskQuestionInfo` land on disk as interface values with the interrupt state; without registration, `saveCheckPoint` fails **before** the interrupt is emitted, manifesting as "the first call needing approval directly reports a run failure". Registered with `schema.RegisterName` (type names get written into the checkpoint stream: they may be added, but not renamed or reused), and `TestInterruptPayloadsSurviveGob` pins both "encodable/decodable" and "the name is unchanged".
2. **A run with tools must have a checkpoint store**: when `store == nil`, `adk.Runner` returns the iterator as-is — no error, no checkpoint. Then a self-interrupting tool "asks a question nobody can answer" (the answer returns `ErrNotPending`). `prepare` now relaxes the guard to "if tools exist, a store is mandatory".
3. **The OpenAI-compatible path's degradation must be done in middleware** (not rendered after the fact in the handler): only by deciding "don't interrupt, deny directly" at the gate can the already-tested denial path be reused and the model give a coherent answer; otherwise the stream ends with a question while the caller gets an empty answer.

## 9. Deviations from the plan draft and explicit deferrals

| Item | Description |
|---|---|
| `core.CheckPointDeleter` | eino v0.8.13 has no such interface; deletion is provided by this repo's `checkpoint.Store.Delete` |
| Decision constants | The plan said "merge into one copy in `pkg/agent`"; the implementation changed to **each of the two services holding its own literals** (reason in §5) |
| `updatedInput` (approval with revised arguments) | Not implemented: the protocol has no such field, and approval can only "execute with the arguments at interrupt time". The reason is no caller needs it, and its semantics (whether the address remains valid after changing arguments) needs separate design |
| `agent.approval-timeout` config item | Still not made configurable. The upper bound of waiting is now `approvalWaitGrace` (30 minutes, hard-coded, counted from the pause moment); after that the card writes `○ 已失效` in place and finalizes; `checkpoint-ttl` is a different thing (the backend's own expiry). There is **no** proactive notification (no separate message, no @mention), only the card becoming unanswerable |
| Per-tool approval | Explicitly not done (reason in §4.3) |
| Automatic resume after agent restart | Not done: the checkpoint has the prerequisites, but a re-driver/sweeper is needed |
| `wshub` TUI/Web approval frame | Not done; the port is ready, just reuse the same sink |
| Role gate | Left as is (`TODO(role-gate)`, role determination still pinned to true), see §6 |

## 10. File inventory

**New**

- `internal/agent/pkg/approval/{policy.go,policy_test.go}` —— declarative policy (the sole determination)
- `internal/agent/pkg/checkpoint/{checkpoint.go,checkpoint_test.go}` —— Redis/in-memory implementations of `Store` + the `RunMeta` sibling key
- `internal/agent/biz/v1/agent/{approval.go,approval_test.go}` —— middleware and four-state coverage
- `internal/agent/pkg/agent/approval_test.go`, `internal/chat/pkg/agent/approval_test.go` —— cross-service contract pinning (decision words, gob names)
- `internal/agent/handler/chat_completions_test.go` —— the compatible path's three outcomes
- `internal/chat/biz/v1/bot/decision.go` —— decision area: `decisionLog`, pure functions for decision rows, `presentDecision` / `resolveDecision` / `cancelDecision` / `expireDecision`, the per-pause expiry timer
- `internal/chat/biz/v1/bot/decision_test.go` —— four states, takeover, expiry, two-pause accumulation, arguments on card, degradation path
- `docs/design/human-in-the-loop.md` —— this document

**Modified (highlights)**

- `internal/agent/biz/v1/agent/{agent.go,einoengine.go,engine.go,emit.go}` —— `resolveApproval`, `approvalPlanFor` (compat mode), attaching the handler, suspended terminal, `Approve` as an event stream, checkpoint injection, removal of `awaitApproval`
- `internal/agent/pkg/agent/{agent.go,approval.go}` —— interrupt/resume types, gob registration, decision constants, `RunOptions.Approval`/`ApprovalFallback`, `Resumable`
- `internal/agent/providers.go` —— `ProvideCheckpointStore`; removal of `ProvideRunStateRegistry`
- `internal/agent/handler/{agent_stream.go,chat_completions.go}` —— `approve` as SSE; the compatible path explicitly denying per `compat-approval-mode`
- `pkg/api/agent/v1/agent.proto` (+ regenerated pb), `internal/pkg/clientset/agent/agent.go`
- `internal/chat/biz/v1/bot/{ai_turn.go,ai.go,approval.go,card_action.go,funnel.go,bot.go,progress.go,streaming_card.go}` —— suspended state, same-card continuation, decisions in place (§6), multi-request card, identity check, stop on parked card, plain-text funnel and takeover, timer stop on pause, `update_multi` and element-level write interfaces
- `internal/chat/pkg/larkcard/{rich.go,render.go,tools_display.go,card2schema_test.go}` —— `BuildRichCardWithDecisions`, `ElementMarkdown` / `ElementButtonRow`, `ArgumentsDigest`, `Glyph*`, offline schema validation of decision-area elements
- `internal/chat/pkg/agent/{agent.go,event.go}` + `llm/{session.go,translate.go}` + `cli/session.go` —— ports and events (`AnswerPermission` that keeps the sink)
- `internal/chat/pkg/session/permission.go` —— `PendingPermission.AskedBy`
- `pkg/options/agent_options.go`, `configs/examples/oai-agent-apiserver.example.yaml` —— `require-approval` / `checkpoint-ttl` / `compat-approval-mode`
- `hack/mcpctl/main.go` —— the `set-require-approval=` / `clear-require-approval` modes; `show` prints each row's gate (§2.4)
- `internal/agent/biz/v1/agent/{approval_test.go,instruction_test.go}` —— `TestResolveApproval` / `TestResolvedPolicyHoldsTheNamedTool`: the union chain from row-level declaration → policy → `DecisionAsk` (`fakeAIStore` therefore gained `MCPs()`)

**Deleted**

- `internal/agent/pkg/runstate/` (the whole package) —— the root of having two state systems, gate and checkpoint, coexisting

## 11. Verification

Unit tests (do not run `go test ./...`):

```
go test -v -run 'TestPolicy'          ./internal/agent/pkg/approval
go test -v -run 'TestCheckpoint|TestRunMeta' ./internal/agent/pkg/checkpoint
go test -v -run 'TestDecisionValuesAreWireValues|TestInterruptPayloadsSurviveGob' ./internal/agent/pkg/agent
go test -v -run 'TestApprovalHandler|TestResolveApproval|TestResolvedPolicyHoldsTheNamedTool|TestEinoEngine|TestAgentBiz_ResolvesCompatApproval' ./internal/agent/biz/v1/agent
go test -v -run 'TestDrainEvents|TestPausedAnswer' ./internal/agent/handler
go test -v -run 'TestRunAITurn|TestApprovalVocabularyIsWireValues' ./internal/chat/biz/v1/bot ./internal/chat/pkg/agent
go test -v -run 'TestAnswerPermission' ./internal/chat/pkg/agent/llm
go test -v -run 'TestPendingPermission|TestApprovalWord' ./internal/chat/biz/v1/bot
# 卡片交互（§6）
go test -v -run 'TestDecision|TestPause|TestAnExpired|TestTakeover|TestANewMessage|TestStoppingAParked|TestSecondPause|TestAnsweringA' ./internal/chat/biz/v1/bot
# 一条消息一个 turn 的不变量（§6.9）：同一消息投两次只产生 1 张卡、1 次 agent 请求
go test -v -run 'TestClaimTrigger|TestRunAIDrops|TestRunAIRunsADifferentMessage|TestRunAIRunsAMessageWithNoID' ./internal/chat/biz/v1/bot
go test -v -run 'TestArgumentsDigest|TestCard2SchemaGuard|TestRedactInlineSecrets' ./internal/chat/pkg/larkcard
```

Among them, `TestRunAITurnKeepsTheCardOpenForTheContinuation` and `TestApprovalHandlerPausesThenRunsOnApproval` are end-to-end tests for this chain: the former covers "suspend without finalizing → answer → second stream segment renders into the same card", and the latter covers "Run → interrupt → Resume → the same tool executes with its original arguments".

The four card-interaction states each have a unit test: `TestPauseDrawsTheDecisionOnTheTurnsOwnCard` (the pause draws on the same card, button values complete, body placeholder cleared, summary reworded, **no** separate message), `TestAnsweringADecisionLeavesOneLineAndNoButtons`, `TestTakeoverCancelsTheParkedRun`, `TestAnExpiredWaitClosesTheCardAndRefusesLateClicks`, `TestStoppingAParkedTurnRecordsWhyTheDecisionWentUnmade`, `TestSecondPauseExtendsTheSameLog`, `TestAPauseThatIsSupersededWithdrawsItsButtons`, `TestTheDecisionLineNamesTheCallItIsAskingAbout`, `TestTheDecisionIDsStayInsideThePlatformsLimit`. Card JSON is validated offline against the 2.0 whitelist by the `TestCard2SchemaGuard` `decision block elements` / `question option row` fixtures, and `element_id` validity is checked at that step too (§6.8). **Note: the live device (probes in §3.1 and the end-to-end in §11) has only run one round, and on a build from before the `element_id` fix** — see §6.6, §6.8.

End-to-end (`agent.engine=eino` + an MCP tool hitting `RequireApproval`, `/pipeline` being the ready-made one):

0. `MCP_NAME=oai-chat-apiserver MODE=set-require-approval=pipeline go run ./hack/mcpctl` (§2.4); after a refresh or restart, confirm in the logs that the run's policy contains `pipeline`.
1. `@机器人 请构建 onex pipeline 中的 content 服务` → **only one card**; when the model asks for the environment, the buttons appear in **the same card**, the body placeholder ("正在思考中，请稍候……") is cleared, and the summary changes to "? 等待你的选择".
2. Click `edu-onex` → the decision area becomes a "✓ 已选择" line, the body continues in the same card; then the `pipeline` approval appears in the same card in the same way.
3. Click "✓ 允许" → the tool executes and **the same** card continues outputting; click "✗ 拒绝" → the tool does not execute, and after the model explains, the same card finalizes normally.
4. One assistant message triggers two tools needing approval → the decision area lists both tools and their arguments; one approval executes both.
5. Send another message while suspended → the old card becomes "○ 已取消：收到新消息" and finalizes (footer says `已被新消息接管`, no longer a bare `已停止`), and the new message gets a new card (the suspended tool **did not** execute).
6. While suspended, send "允许"/"不允许"/"随便看看" → the first two are answers, the last takes over as a new request.
7. While suspended, `/stop` → the card finalizes as `你停止了本次运行`, and the decision area states it was cancelled.
8. Do not click, wait past the grace period → the decision area becomes "○ 已失效", buttons disappear; clicking again (if any remains) is rejected.
9. Repeated clicks / clicking an expired old card → explicitly reports "already handled or expired".
10. `curl` directly against `POST /v1/agent/approve` (with `requestID`) → receive SSE.
11. `/v1/chat/completions` hitting a tool needing approval → explicitly denied per `compat-approval-mode` with readable copy.
12. **Redelivery (§6.9)**: send **one** `@机器人 请构建 onex pipeline 中的 content 服务` in the group (this turn is necessarily >20s, landing right in the pre-fix redelivery window) → only one card, all four decisions drawn on the same card; during this, `grep -c 'lark sdk \[receive message'` **increments by only 1 per message** (by 2 before the fix), the console has **no** `Duplicate Lark event ignored`, `duplicate trigger dropped`, or `taking over a parked run`. The slow run must behave the same way.

Steps 1–8 require a real Feishu tenant. One round was run on 2026-09-28: the flow itself works (element-level insertion, body continuing to write, card buttons clickable), but the approvals in steps 1–3 were **not all drawn on the same card**; the reason is in §6.8 — it was the `element_id` overflow, not "a streaming card cannot have components added". After the fix these steps still need another run to confirm.

Formatting: use **scoped gofmt/goimports** for a subset of files; `make format` covers the whole repository.
