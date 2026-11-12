# 设计：人机协作（Human-in-the-Loop）

> 状态：已实现
> 范围：`oai-agent-apiserver`（eino/adk 中断-恢复 + checkpoint）+ `oai-chat-apiserver`（Lark 卡片 / 纯文本 / HTTP / OpenAI 兼容路径）
> 参考：eino-examples `adk/human-in-the-loop`（`InvokableApprovableTool` + `runner.ResumeWithParams`）；cc-connect `core/engine.go`（`pendingPermission` 三级降级渲染 + `perm:`/`askq:` 回调词汇）

## 1. 背景与目标

一次 agent turn 里，某些工具调用不能由模型自己决定是否执行：删集群、发版本、动生产数据。人机协作要回答四个问题：

1. **怎么知道一个工具需要人（触发判定）**：不能让每个工具自己声明，否则第三方 MCP 工具永远进不了流程。
2. **怎么问（呈现）**：Lark 里是带按钮的卡片，非 Lark 场景是纯文本、HTTP、OpenAI 兼容路径。
3. **怎么等（状态）**：人能隔几分钟甚至换一台机器再答，等待不能绑在某个进程或 SSE 连接上。
4. **答完之后怎么继续（恢复）**：批准/拒绝是同一次 turn 的继续，不是新的一轮。

设计原则：**判定不在工具里，收口在中间件**；**中断即挂起，答案即一次 resume**。

## 2. 触发判定：一个工具怎么被判定需要人机协作

### 2.1 三个声明源取并集

| 声明源 | 位置 | 粒度 |
|---|---|---|
| `agent.require-approval` | `pkg/options/agent_options.go` | 全服务器默认 |
| `AgentM.RequireApproval` | `internal/agent/types/resource.go`（`agent.agent` 行） | 单个 agent 覆盖 |
| `MCPM.RequireApproval` | `internal/agent/types/resource.go`（`agent.mcp` 行） | 一个 MCP 服务端带来的全部工具 |

模式是精确名（`delete_cluster`）、前缀 glob（`delete_*`）或全匹配（`*`）。并集不可被下游收窄：agent 无法豁免它所用 MCP 服务端声明的门禁，否则服务端的声明就形同虚设。

**没有豁免表**：不需要审批就不列；整台服务器都要审批就 `["*"]`。

### 2.2 一个中间件收口

`internal/agent/biz/v1/agent/approval.go` 的 `approvalHandler` 挂在 agent 的 tool node 上（`adk.ChatModelAgentMiddleware.WrapInvokableToolCall`），是所有工具调用的唯一入口——内置工具与 MCP 工具一视同仁。它按 `tCtx.Name` 查策略：`DecisionAsk` 才中断，否则把原 endpoint 原封不动交还。

好处是**第三方工具零改造**：改一行资源记录即生效，不用改工具、不用改工具列表、不用重新部署。

### 2.3 与 `ask_user_question` 的关系（概念分离）

- **审批（permission）**：对一个"本来就要执行"的工具做策略裁决，由中间件集中实施。
- **征询（elicitation）**：工具的契约就是"返回人类的答案"（`ask_user_question`），天然属于工具本体，中间件不得干预。

因此 `ask_user_question` 保留自中断，且保留工具名（`EngineConfig.Reserved`，与 `mcpclient.Options.Reserved` 同源）在策略里一律 `DecisionAllow`——否则会问人类"要不要问人类"。

### 2.4 给一个工具加门禁（运维动作）

行级声明改 DB 行，不由代码或 config 承载，因此用 `hack/mcpctl`：

```bash
# /pipeline 现在需要人类批准（oai-chat-apiserver 这行 MCP 服务端发布的工具）
MCP_NAME=oai-chat-apiserver MODE=set-require-approval=pipeline go run ./hack/mcpctl
MCP_NAME=oai-chat-apiserver MODE=show go run ./hack/mcpctl   # 读回
```

三点注意：

1. **模式写的是模型看到的名字**，不是 MCP 线上名。agent 对每个线上名做 `SanitizeName` 再暴露（`pkg/mcpclient`），策略匹配的是后者的结果——也就是用户会在卡片上看到的那个名字。`/pipeline` 走 cobrax 的 `toolName`（去掉根命令名的命令路径）得到 `pipeline`，无需改写，所以直接写 `pipeline`。
2. **同名的工具会被改名**：`Uniquify` 在跨服务端重名时把后来者改成 `pipeline_2`（并打一条 Warn 日志）。此时按 `pipeline` 写的门禁会落空，得按日志里的新名字改声明。这是并集策略只按名字匹配的代价，改名字比放宽成通配更安全。
3. **`MODE=apply` 不碰这列**：重新登记一个服务端（改 url、换隧道端口）不该顺手摘掉别人装的闸门，所以解除门禁必须显式 `MODE=clear-require-approval`。生效时间与其它行改动一致：`store.refresh-interval`（5m）+ `mcp-cache-ttl`（5m），想立刻生效就重启 oai-agent-apiserver。

## 3. 分层

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

- 策略判定归 biz：`resolveApproval`（`biz/v1/agent/agent.go`），与 `resolveMCPs` / `resolveInstruction` 同构同处，产物放进 `RunOptions.Approval`。
- 拦截归 engine：`einoengine.go` 的 handler 链 `[toolTrace, approval, contextGuard, summarization]`（`handlers[0]` 最外层；`toolTrace` 必须在外层，这样中断经它上报后该步保持 `pending`）。

## 4. 中断与恢复

### 4.1 中断即挂起

engine 收到 `Action.Interrupted` 后发 `EventInterrupted`（带本次全部待批请求），再发**终端事件 `EventSuspended`**，然后结束本次 SSE。`EventSuspended` 与 `Done/Failed/Stopped` 并列但语义不同：**run 没有结束**，渲染方不得把它当作结果收尾。

checkpoint 由 eino 的 runner 在发出中断**之前**写入，所以"挂起"天然跨进程、跨副本。

### 4.2 checkpoint 与 RunMeta

eino 的 checkpoint 只存执行状态，不足以重建一个 run：instruction、工具集、审批策略都由 run 的 agent 选择派生，而选择不在 checkpoint 里。因此同一个 `Store` 里存一份**兄弟 key**：

| key | 内容 |
|---|---|
| `<prefix>:agent:cp:<runID>` | eino 的 checkpoint blob |
| `<prefix>:agent:cpmeta:<runID>` | `RunMeta{Agents, SessionKey, UserMessage, UserID, Reasoning, AllowAll, ApprovalFallback, RequestID, Pending}` |
| `<prefix>:agent:cpclaim:<runID>:<requestID>` | 一次性答复标记（`Claim` = SetNX，多副本先到先得） |

resume 时用 `RunMeta` 逐字重建 `RunOptions`（含 `ApprovalFallback`）：重建不一致不是小瑕疵——checkpoint 里的中断点按地址记录，工具集变了就找不到可续的那一处。

`Store` 接口：`Get/Set/Delete/PutMeta/GetMeta/Claim`。Redis 与内存两种实现（`internal/agent/pkg/checkpoint`）。

### 4.3 一次决定覆盖整组

一条 assistant 消息里的多个工具调用会产生 composite interrupt。eino 的 `ResumeWithParams.Targets` 是 map，且文档明确要求**未被 targeted 的 leaf 必须自行重新中断**。由于 resume 时 ToolsNode 整体重跑，逐个批准会重放已批准的工具，语义歧义。

所以：**一次决定覆盖本次中断的全部 root cause**，卡片必须逐个列出所有待批工具，让这个决定是知情的。

### 4.4 中间件的四种状态

| 状态 | 条件 | 行为 |
|---|---|---|
| 首次进入 | `!wasInterrupted` | `tool.StatefulInterrupt(ctx, PermissionInfo{...}, argsInJSON)` |
| 被批准 | `isTarget && hasData && Approved` | 用**中断时存的参数**调原 endpoint |
| 被拒绝 | `isTarget && hasData && !Approved` | 返回普通工具结果字符串（"用户拒绝执行…不要重试"），交由模型解释并收尾 |
| 未被 targeted | `!isTarget` | **重新中断**（否则并行场景下该信号被吞，永久挂起） |

被 target 但没带数据（bare resume）视为拒绝——本 engine 永远用 `ApprovalResult` 回复 permission 地址，走到这里说明答复丢了或格式不对，跑工具等于未经许可执行。

**拒绝是回答，不是失败**：工具结果里带着拒绝理由回到模型，模型用自己的话解释；本轮不中断。

### 4.5 失败关闭与幂等

- **失败关闭**：策略要求审批而没有 checkpoint store / 无法征询时，一律拒绝并说明，不再静默放行（这也是 `prepare` 里"有工具就必须有 store"守卫的由来）。
- **幂等**：靠 checkpoint + claim。第一次 resume 后 checkpoint 与 claim 都已消费，重复点击 / 点旧卡片得到 `ErrNotPending`（"已处理或已失效"），替代旧实现里会静默成功的按需建 gate。
- **超时**：不再有阻塞 `Wait`，超时语义落在 `agent.checkpoint-ttl`（默认 24h）——它是这类 run 唯一的垃圾回收，必须长于最慢的答复。

## 5. 协议

`pkg/api/agent/v1/agent.proto`：

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

- `POST /v1/agent/approve` 由"返回 JSON"改为**返回 SSE**，与 `POST /v1/agent/ask` 对称：答复触发 resume，续跑的事件从这条流回来。`internal/pkg/clientset/agent` 的 `AgentApproveStream` 与 `AgentAskStream` 对称。
- `AgentEventType` 新增 `agentEventTypeSuspended = 10` 作为挂起终端。
- 决策常量（`allow` / `deny` / `allow_all`）是**跨服务的线上契约**：`agent/pkg/agent` 与 `chat/pkg/agent` 各持一份字面量，两侧由 `TestDecisionValuesAreWireValues` / `TestApprovalVocabularyIsWireValues` 钉住。这是有意的镜像——两个二进制各自独立发版，共享一个 Go 常量意味着某一侧升版会静默改变对另一侧的语义。

## 6. chat：一个 turn 一张卡，决策就地

`biz/v1/bot/ai_turn.go` 的 `aiTurn` 区分**挂起**与**结束**：

- `EventInterrupted` → 记 `PendingPermission`、**把决策画在自己这张卡上**、`setSuspendedTurn`、停掉 pending 行的计时；**不**收尾、不写 Done 页脚。
- `EventSuspended` → 只是"这条流结束了"：卡片保持原样，等答复。
- 答复之后，续跑事件通过**同一条 sink**回来（端口把 sink 保留在 backend session 上，见 `Session.AnswerPermission` 的注释），于是第二段流渲染进同一张卡片，并在真正 `EventDone` 时收尾。被答醒的 turn 会 `rearm()` 重新拿一个 deadline：一个超时之后才被答复的 run 仍然要有个地方放它的答案。

卡片回调必须 3 秒内返回（Lark 回调预算），所以点击只做"投递答复"并立即返回，续跑由后台泵流。

### 6.1 为什么是"一张卡"（本次改造的起因）

改前的形态是**两张互不知道对方的卡片**：流式卡在挂起时被故意留着不收尾（「正在思考中，请稍候 ……」还在写，停止按钮仍然可点），决策则通过 `SendCardModel` **另发一条消息**（审批卡或征询卡）。于是同一句话在群里得到两张卡，读者不知道哪张是活的、能不能继续说话——这就是"卡片紊乱"。

现在一次 turn 的**全部状态都在这张卡上**，决策区是它的一部分，插在停止按钮行之前，构成唯一的活决策面（沿用既有不变量：审批优先、问题随后再中断，绝不出现两个可答复的面）：

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

### 6.2 决策区的两个元素

排在停止按钮行之前（`CardElement.Create` + `insert_before` + `target_element_id`）：

| 元素 | 形态 | 说明 |
|---|---|---|
| `decision_log` | markdown，**只追加** | 待决时是请求描述，已决时把结论追加成一行；一个 turn 暂停两次就有两行，读起来是一条时间线 |
| `decision_row_<n>` / `decision_grant_<n>` | column_set（按钮） | 审批 = 允许 / 拒绝；第二行 = 总是允许；征询 = 每个选项一个按钮（沿用 `askq:<i>`）；**只在待决时存在**，答复即删——"没有待决就没有按钮"就是"不留死按钮"的实现方式。id 必须短：飞书把 `element_id` 限死在 20 字符以内，且是在**往已存在的卡上插组件**时校验（见 §6.8） |

```
? **请选择**：要发布到哪个环境？
✓ 已选择：edu-onex · 14:32
? **需要你的批准**：pipeline（env=edu-onex）
✗ 已拒绝：pipeline · 14:33
```

- 每次暂停用一对新 id（`<n>` = 该 turn 第几次暂停），避免"删了再用同一个 id"的语义含混。
- 日志行里的**参数**来自协议新增的 `arguments`（§5）：`einoengine` 从 `PermissionInfo.Arguments` 填，chat 侧 `larkcard.ArgumentsDigest` 压成一行摘要（顶层标量 `k=v`，嵌套对象写成 `{}`，列表取其成员逗号相连；**先脱敏再截断**到 80 字）。`ArgumentsDigest` 对 `{}` / `null` / 成员全空返回空串——"这次调用没有参数可说"，而不是把 `null` 印在行上。
- 决定没有作出时（取消 / 失效 / 被取代）共用 `○`，靠文字区分；两者的共同点正是"这次决定没有作出、工具没有执行"。

### 6.3 四态

| 态 | 触发 | 卡片变化 |
|---|---|---|
| **暂停** | `EventInterrupted` | 写决策区（请求 + 按钮）；正文占位清空（**仅当正文还空着**）；工具行改 `○ 等待人工确认` 并停表；summary 改 `? 等待你的选择`；**不发新消息** |
| **答复** | 点按钮 / 文本答复 | 投递成功**之后**才删按钮、追加结论行；正文继续写同一张卡；工具行重新计时；真正 `EventDone` 才收尾 |
| **接管** | 待决期间来了新消息 | 取消**仍然停在决策上**的 run（被挂起的工具**不执行**）→ 写 `○ 已取消：收到新消息` 并删按钮 → 这张卡按`已被新消息接管`收尾（页脚写明原因，不再与 `/stop` 共用一句`已停止`）→ 新消息开**新** turn 与新卡 |
| **失效** | 等待超过 `approvalWaitGrace`（30 分钟） | 写 `○ 已失效：等待超时，本次执行未继续` 并删按钮，卡片按已结束收尾；此后该 run 的迟到点击被拒（`decisionExpired`） |

- **被新的暂停取代**：上一段暂停的按钮还在卡上、新的中断又到了——后端（CLI 那种"流一直开着、进程自己持有请求"的形态）可以不等人回答就再中断一次。这时旧按钮**就地撤掉**、日志追加 `○ 已被新的确认替代，本次执行未继续`，免得卡上留着一个点不动也送不出去的决定。这是"失效"之外的第四条退出路径，判断依据是"卡上还有活按钮"（`takePendingActions`），不是时间。
- **接管的选择**：挂起会释放会话槽，所以一条新消息本来就会开新 turn——与其假装它不会发生，不如把它**做成被允许且被解释**的行为。接管动作在 `runAI` 里、`sess.Submit` / `SetCurrentRun` **之前**完成，否则会与旧 run 的 `ClearCurrentRun` 抢时序。
- **只有还停在决策上的 run 会被接管**：暂停的 turn 在恢复运行后**仍留在**按会话索引的挂起表里（`clearSuspendedTurn` 只在该 turn `finish()` 时删），所以"表里有它"不等于"它在等人"。删除这一判定就会停掉一个**正在执行**的 run——用户刚批准的工具被取消、卡片以`已停止`收尾、旁边再开一张新卡，正是"一条消息两张卡"那张卡 1 的来历。恢复后的那半段不持有会话锁（`pkg/agent` 的续跑是 detach 出去的，为的是不把 3 秒预算的卡片点击拖成几分钟），因此这半段期间到达的新消息会**并行**开自己的 turn，而不是排队——这是已知残余。
- **失效这一态改前完全没有**：到期只让 turn 的 context 死掉，没有任何人看 `ctx.Done()`，卡片就那么立着、按钮一直可点。现在 `aiTurn` 按每次暂停装一个 `time.AfterFunc`，到点用一个 detach 出来的 2s context 下发（此时 turn 的 ctx 已死），并置 `expired` 标志，让迟到点击不再把 turn 拖起来。turn 已经被 `finish()` 注销，所以"这个 run 的等待已过期"另外记在会话级的 `expiredPauses` 里（由下一次暂停清除）。

### 6.4 待决期间的消息规则

- `/` 开头的命令照旧（`/stop` 始终可用）。
- 纯文本 + 审批类：命中决策词表 → 答复；**明确取消词**（`取消` / `算了` / `cancel`，**不含 `停止`**，那是 `/stop` 的事）→ 取消；其余 → `false, "", nil` 落回提示词那一档，由 `runAI` 当新请求接管。
- 词表按**整条消息 trim 后等价匹配**，不做子串/前缀匹配——否则「允许，但别删 namespace」会被吞成答复，而「先别允许，改成构建 X」又可能被误判成接管。
- 纯文本 + 征询类：带 @机器人 的视为新请求；否则作答复（自由文本本就是合法答案）。
- 图片 / 其他消息：视为新请求。

### 6.5 字符

只用五个单色字符，**没有 emoji、沙漏、暂停/停止方块、彩色勾叉**：`?` 待决、`✓` 已允许/已选择/已完成、`✗` 已拒绝/失败、`○` 没有作出（取消/失效/被取代）、`⟳` 进行中。定义在 `larkcard`（`Glyph*`），卡片与文本消息共用。
按钮上的字符只能是**裸字符**（card 2.0 的 `text` 是 `plain_text`，放不了 `<font color=…>`），颜色由 `type` 承担（允许 = `primary`，拒绝 = `danger`，总是允许 = `default`）；`icon` 不在按钮白名单里（`card2schema.go`），本次不用。

### 6.6 能力、降级与未验证的部分

往在跑的卡里加组件、加完再删，是 cardkit 的官方能力（`CardElement.Create/Delete/Settings/Content`，共用严格递增的 `sequence`）。落地时：

- 建卡 `config` 加 `update_multi: true`——元素级接口要求它不为 false，而它只影响多人协作更新。
- **用元素级接口而不是整卡 `Card.Update`**：整卡重写会打回客户端的折叠/展开状态，而决策区在暂停期间要保持可见。也因此没有用 `BatchUpdate`（信封语义含糊）：写日志与删按钮是两次调用。
- 收尾时决策区必须保住：`finish()` 走 `CloseStreamingCard` → `BuildRichCard` 重建整张卡，会**把决策区整块抹掉**。为此 `rich.go` 新增 `BuildRichCardWithDecisions(...)`，`BuildRichCard` 签名不变、转发时传 `nil`，于是既有调用点与 golden 一个都不用动。
- **每一步卡片写操作都是非致命的**：失败只记日志，并回退到"另发一条决策卡"的老路（`sendApprovalCard` / `sendAskQuestionCard` 保留为降级路径，按钮 value 少一个 `inline` 标记即走替换消息那条路）。原因见下。
- **卡片里的写操作没有在真机上验过**：本环境没有真实飞书租户，所以「流式卡上插组件 / 插入后继续写正文 / 回调带 `session_key`」都只有单测与 golden 覆盖，没有真机截图。若真机上"流式卡不能加组件"成立，降级路径自动接管，四态规则不变。
  > 2026-09-28 首次真机跑批注：元素级接口**是可用的**——暂停时 `decision_log` 的插入成功落到卡上，`update_multi` 也确实生效；两次审批（`decision_actions_1_last` 与 `decision_actions_2_last` 各一次）都没画成同一张卡，原因不是"流式卡不能加组件"，而是组件自己的 `element_id` 越界（§6.8）。另一条真机独有的规则：`CardElement.Content` 拒绝**空 content**（`code=99992402`），所以"清空正文占位"实际写的是零宽空格（`placeholderCleared`），不是空串。
- 按钮 value **不带 `card_id`**：决策日志的重写依赖进程内的 turn 状态（`suspended` / `expiredPauses`），带上 `card_id` 也换不来跨进程改写，反而让 value 多一个会过期的字段。

### 6.7 其它既有约定（未变）

- **多请求**：一组待批工具全部列出，文案说明一次点击决定全部（允许则全部执行，拒绝则全部不执行）；`perm_*` 值里带 `run_id` / `req_id` / `perm_subject`。
- **身份校验**：`answererAllowed` 以发起者（`PendingPermission.AskedBy`）为准，SRE/Admin 为兜底；两个无法比较的未知身份（缺 clicker、旧记录）放行而不是拒绝——拒绝会把一个 run 永久锁死。
  > 注意 `runtime.IsSREUser` / `IsAdminUser` 目前被**钉死为 true**（`pkg/runtime/static.go` 的 `TODO(role-gate)`），所以角色那一支当前是放行的，真正起作用的是发起者比较。恢复真实判定不在本次范围内。
- **投递失败不动卡片**：卡片是唯一还带活按钮的地方，一个投递失败的答复如果把按钮删了就再也没法回答。
- **停车卡片上的停止按钮**：`Stop` 除了取消运行中的 run，还要**删掉挂起的 checkpoint**——否则停止前发出的答复会在停止后抵达，执行用户刚刚叫停的工具。停止与接管共用一个实现（`stopRun`）。

### 6.8 真机上的两条硬规则（2026-09-28）

§6.6 的"真机没验过"在 2026-09-28 有了第一批实测，两条规则只有在真租户上才会说话：

1. **`element_id` 有 20 字符上限**，而且是在 `CardElement.Create`（往已存在的卡上插组件）时校验：

   ```
   300315: elementID format error. Only alphabets, numbers, and underscores are
   allowed. It must start with an alphabet and not exceed 20 characters.
   ElementID:decision_actions_1_last
   ```

   落地时的 id 是 `<pattern>_<n>` 再粘一个 `_last` 后缀（24 字符），于是**每个带"总是允许"那行的审批都插不进去**，`presentDecision` 返回 error，`pause()` 走降级 → `sendApprovalCard` 另发一条消息。征询类没有第二行，19 字符合法，所以是好的——"同一张卡"只在没有第二行按钮时成立。现在的 id 是 `decision_row_<n>` / `decision_grant_<n>`（`decision.go`），短到 slot 上千也不越界。

   这类 id 由运行时拼出来，**不在任何 golden 里**，所以离线校验必须覆盖两条路：`Card2Violations` 现在会检查每个 `element_id`（`TestCard2SchemaGuardCatchesAnElementIDThePlatformWouldRefuse`），测试替身 `fakeLarkServices.InsertCardElements` 在每次插入时跑同一个校验——HITL 的全部单测因此都压在这条规则上。

2. **`CardElement.Content` 拒绝空 content**（`code=99992402, msg=field validation failed`）。暂停时清正文占位原本写空串，在真机上从未生效（卡片会一直挂着「正在思考中，请稍候 ……」）。现在写 `placeholderCleared`（零宽空格）。

同一批实测还确认了 §6.6 担心的前提**不成立**：往在跑的卡上插组件、插完继续写正文，在真租户上是可行的。

### 6.9 "一条消息一个 turn"在投递层依赖的两件事（2026-09-28）

§6.1 的不变量（一条消息 → 一个 turn → 一张卡）在投递层还依赖两件事。2026-09-28 群里"发一次 @ 出两张卡"的起因就是这两件都没做到，而**每层各自猜一次**（handler 按 event_id 查概率缓存、session 忙则排队、biz 见"挂起的 turn"就停掉它）都只是入口过滤器，永远可以绕过。现在把职责收到一处。

1. **WS 收消息回调必须立刻返回**。SDK 先把事件交给处理器，处理器**返回之后**才写回响应帧（`oapi-sdk-go/v3/ws/client.go`），而 WS 回调原本是同步跑完整个 turn 的，于是每个跑得比飞书 ACK 超时久的 turn 都会被平台**重投**。日志里对得上：`aa619a7c…` 20:11:26 → 20:11:45、`ec7bdd72…` 20:12:51 → 20:13:11（两次首次投递的同一秒都有 `AI turn started`），而同一时段一个 1 秒内派发完的卡片回调**没有**被重投。现在 WS 回调与 webhook 路径一样用 `runtime.GoSafe` 异步派发（`handler/bot.go`），`c.eventHandler.Do` 立即返回——重投不再发生。**可证伪的判据**：`grep -c 'lark sdk \[receive message'` 每条消息只 +1（修复前凡是 >20s 的 turn 都 +2）。其余回调保持同步：卡片回调的返回值就是给飞书的结果（toast/卡片），不能异步。
2. **认领的是消息，不是事件 id**。重投是否发生不该由我们决定（平台行为、进程重启、另一条投递通道），所以守卫放在**唯一能产生 turn 的地方**：`runAI` 开头的 `claimTrigger`（`biz/v1/bot/admission.go`），键为 `会话 + om_…`，TTL 1 小时、到点前不淘汰（群流量挤不掉别的会话），空 `MessageId` fail-open。命中即 WARN + `opsassist_duplicate_triggers_total` 后直接返回，**不排队**——排队的重复消息在前一个 turn 结束后照样会再跑一个。认领表就位之后 handler 那把 ristretto 闸门（`handler/handler.go`）不再承重，丢 key 无害，本次未改它的实现。

   边界：认领表是内存态 ⇒ 前提是**单副本**，与会话锁（`pkg/session` 的 `currentRun`/`pending` 同样是内存态、不落 store）共用同一个前提。将来横向扩副本时，认领要与会话锁一起搬进 store。

## 7. 非 Lark 场景

| 面 | 实现 |
|---|---|
| 纯文本回复 | 待审批漏斗 + 决策词表；答复走 `AnswerPermission`，续跑渲染进同一张卡片 |
| HTTP/API 直调 | `POST /v1/agent/approve`（SSE）。契约：`runID` + `requestID` 必须与当前挂起一致，否则 `ErrNotPending`；答复一次性，重复调用同样 `ErrNotPending` |
| OpenAI 兼容路径 | `/v1/chat/completions` 的调用方没有卡片也没有 approve 调用，所以按 `agent.compat-approval-mode` 处理：`deny`（默认）不执行该工具并把拒绝作为工具结果交给模型解释；`error` 让请求失败；`allow` 在该端点不做门禁。配置值在启动时校验，拼错不会被读成 `allow` |
| CLI 后端 | CLI 进程自己持有 IPC 权限请求，run 不会 `EventSuspended`：它的流一直活着，答复后后续事件在原流上继续到达。端口对调用方的承诺因此写成"`EventInterrupted` 是挂起、`EventSuspended` 只是这条流结束" |
| TUI/Web 审批帧 | **本期未做**（见 §9） |

## 8. 实施中发现的三件事（都是真 bug）

1. **中断载荷必须注册 gob 类型**：checkpoint 是 gob 编码，`PermissionInfo` / `AskQuestionInfo` 作为 interface 值随中断状态一起落盘；未注册时 `saveCheckPoint` 在中断发出**之前**就失败，表现为"第一个需要审批的调用直接报运行失败"。已用 `schema.RegisterName` 注册（类型名会写进 checkpoint 流：可增，不可改名或复用），并由 `TestInterruptPayloadsSurviveGob` 同时钉住"可编解码"与"名字未变"。
2. **有工具的 run 必须有 checkpoint store**：`adk.Runner` 在 `store == nil` 时把迭代器原样返回——不报错、不 checkpoint。于是自中断的工具会"问出一个没人能回答的问题"（答复返回 `ErrNotPending`）。已在 `prepare` 里把守卫放宽为"工具存在即必须有 store"。
3. **OpenAI 兼容路径的降级要在中间件里做**（而非在 handler 里事后渲染）：只有在门禁处就决定"不中断、直接拒绝"，才能复用已经测过的拒绝路径，让模型给出连贯的回答；否则流会以一个问题结束，而调用方拿到空答案。

## 9. 与计划稿的偏差与明确延后

| 项 | 说明 |
|---|---|
| `core.CheckPointDeleter` | eino v0.8.13 没有这个接口；删除由本仓库的 `checkpoint.Store.Delete` 提供 |
| 决策常量 | 计划说"合并为 `pkg/agent` 里的一份"，实现改为**两个服务各持一份字面量**（理由见 §5） |
| `updatedInput`（带修订参数的批准） | 未实现：协议里没有该字段，批准只能"用中断时的参数执行"。理由是没有调用方需要它，而它的语义（改参数后地址是否仍有效）需要单独设计 |
| `agent.approval-timeout` 配置项 | 仍未做成可配项。等待的上界现在是 `approvalWaitGrace`（30 分钟，硬编码，从暂停时刻起算），到点后卡片就地写 `○ 已失效` 并收尾；`checkpoint-ttl` 是另一件事（后端自己的过期）。**没有**主动通知（不另发消息、不 @人），只是把卡片改成不再可答复 |
| 逐个工具分别批准 | 明确不做（理由见 §4.3） |
| agent 重启后的自动续跑 | 未做：checkpoint 已具备前提，但需要一个重驱/清扫器 |
| `wshub` TUI/Web 审批帧 | 未做；端口已具备，复用同一 sink 即可 |
| 角色门禁 | 保持现状（`TODO(role-gate)`，角色判定仍被钉死为 true），见 §6 |

## 10. 文件清单

**新增**

- `internal/agent/pkg/approval/{policy.go,policy_test.go}` —— 声明式策略（唯一判定）
- `internal/agent/pkg/checkpoint/{checkpoint.go,checkpoint_test.go}` —— `Store` 的 Redis/内存实现 + `RunMeta` 兄弟 key
- `internal/agent/biz/v1/agent/{approval.go,approval_test.go}` —— 中间件与四态覆盖
- `internal/agent/pkg/agent/approval_test.go`、`internal/chat/pkg/agent/approval_test.go` —— 跨服务契约定值（决策词、gob 名）
- `internal/agent/handler/chat_completions_test.go` —— 兼容路径的三种结局
- `internal/chat/biz/v1/bot/decision.go` —— 决策区：`decisionLog`、决策行的纯函数、`presentDecision` / `resolveDecision` / `cancelDecision` / `expireDecision`、按暂停装的失效定时器
- `internal/chat/biz/v1/bot/decision_test.go` —— 四态、接管、失效、两次暂停累积、参数上卡、降级路径
- `docs/design/human-in-the-loop.md` —— 本文

**修改（要点）**

- `internal/agent/biz/v1/agent/{agent.go,einoengine.go,engine.go,emit.go}` —— `resolveApproval`、`approvalPlanFor`（compat 模式）、挂 handler、挂起终端、`Approve` 改事件流、注入 checkpoint、删除 `awaitApproval`
- `internal/agent/pkg/agent/{agent.go,approval.go}` —— 中断/恢复类型、gob 注册、决策常量、`RunOptions.Approval`/`ApprovalFallback`、`Resumable`
- `internal/agent/providers.go` —— `ProvideCheckpointStore`；删除 `ProvideRunStateRegistry`
- `internal/agent/handler/{agent_stream.go,chat_completions.go}` —— `approve` 改 SSE；兼容路径按 `compat-approval-mode` 明确拒绝
- `pkg/api/agent/v1/agent.proto`（+ 重新生成 pb）、`internal/pkg/clientset/agent/agent.go`
- `internal/chat/biz/v1/bot/{ai_turn.go,ai.go,approval.go,card_action.go,funnel.go,bot.go,progress.go,streaming_card.go}` —— 挂起态、同卡接续、决策就地（§6）、多请求卡片、身份校验、停车卡片停止、纯文本漏斗与接管、暂停时停表、`update_multi` 与元素级写接口
- `internal/chat/pkg/larkcard/{rich.go,render.go,tools_display.go,card2schema_test.go}` —— `BuildRichCardWithDecisions`、`ElementMarkdown` / `ElementButtonRow`、`ArgumentsDigest`、`Glyph*`、决策区元素的离线 schema 校验
- `internal/chat/pkg/agent/{agent.go,event.go}` + `llm/{session.go,translate.go}` + `cli/session.go` —— 端口与事件（保留 sink 的 `AnswerPermission`）
- `internal/chat/pkg/session/permission.go` —— `PendingPermission.AskedBy`
- `pkg/options/agent_options.go`、`configs/examples/oai-agent-apiserver.example.yaml` —— `require-approval` / `checkpoint-ttl` / `compat-approval-mode`
- `hack/mcpctl/main.go` —— `set-require-approval=` / `clear-require-approval` 两个模式，`show` 打印各行门禁（§2.4）
- `internal/agent/biz/v1/agent/{approval_test.go,instruction_test.go}` —— `TestResolveApproval` / `TestResolvedPolicyHoldsTheNamedTool`：行级声明 → 策略 → `DecisionAsk` 的并集链路（`fakeAIStore` 因此补上 `MCPs()`）

**删除**

- `internal/agent/pkg/runstate/`（整包）—— 闸门与 checkpoint 两套状态并存的根源

## 11. 验证

单测（禁止 `go test ./...`）：

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

其中 `TestRunAITurnKeepsTheCardOpenForTheContinuation` 与 `TestApprovalHandlerPausesThenRunsOnApproval` 是这条链路的端到端测试：前者覆盖"挂起不收尾 → 答复 → 第二段流渲染进同一张卡片"，后者覆盖"Run → 中断 → Resume → 同一个工具带着原参数执行"。

卡片交互的四态在单测里各有一支：`TestPauseDrawsTheDecisionOnTheTurnsOwnCard`（暂停画在同一张卡上、按钮 value 齐全、正文占位清空、summary 改口、**没有**另发消息）、`TestAnsweringADecisionLeavesOneLineAndNoButtons`、`TestTakeoverCancelsTheParkedRun`、`TestAnExpiredWaitClosesTheCardAndRefusesLateClicks`、`TestStoppingAParkedTurnRecordsWhyTheDecisionWentUnmade`、`TestSecondPauseExtendsTheSameLog`、`TestAPauseThatIsSupersededWithdrawsItsButtons`、`TestTheDecisionLineNamesTheCallItIsAskingAbout`、`TestTheDecisionIDsStayInsideThePlatformsLimit`。卡片 JSON 由 `TestCard2SchemaGuard` 的 `decision block elements` / `question option row` 两个 fixture 离线按 2.0 白名单校验，`element_id` 的合法性也在这一步查（§6.8）。**注意：真机（探针 §3.1 与 §11 的端到端）只跑过一轮，用的还是修 `element_id` 之前的构建**——见 §6.6、§6.8。

端到端（`agent.engine=eino` + 一个命中 `RequireApproval` 的 MCP 工具，现成的就是 `/pipeline`）：

0. `MCP_NAME=oai-chat-apiserver MODE=set-require-approval=pipeline go run ./hack/mcpctl`（§2.4），等刷新或重启后确认日志里该 run 的策略含 `pipeline`。
1. `@机器人 请构建 onex pipeline 中的 content 服务` → **只有一张卡**；模型问环境时按钮出现在**同一张卡**里，正文占位（「正在思考中，请稍候……」）已清空，summary 变「? 等待你的选择」。
2. 点 `edu-onex` → 决策区变一行「✓ 已选择」，正文在同一张卡里继续；随后 `pipeline` 审批以同样方式出现在同一张卡里。
3. 点「✓ 允许」→ 工具执行、**同一张**卡片继续输出；点「✗ 拒绝」→ 工具不执行，模型解释后同一张卡正常收尾。
4. 一条 assistant 消息触发两个需审批工具 → 决策区列出两个工具及其参数，一次批准两者都执行。
5. 挂起时再发一条消息 → 旧卡变「○ 已取消：收到新消息」并收尾（页脚写`已被新消息接管`，不再是裸`已停止`），新消息得到一张新卡（被挂起的工具**没有**执行）。
6. 挂起时发「允许」/「不允许」/「随便看看」→ 前两个是答复，最后一个按新请求接管。
7. 挂起时 `/stop` → 卡片按`你停止了本次运行`收尾，决策区写明已取消。
8. 不点、等宽限期 → 决策区变「○ 已失效」，按钮消失；再点（若有残留）被拒。
9. 重复点击 / 点已失效的旧卡片 → 明确报"已处理或已失效"。
10. `curl` 直调 `POST /v1/agent/approve`（带 `requestID`）→ 收到 SSE。
11. `/v1/chat/completions` 命中需审批工具 → 按 `compat-approval-mode` 明确拒绝并给出可读文案。
12. **重投（§6.9）**：群里发**一次** `@机器人 请构建 onex pipeline 中的 content 服务`（该 turn 必然 >20s，正落在修复前的重投窗里）→ 只有一张卡，四次决策都画在同一张卡上；期间 `grep -c 'lark sdk \[receive message'` **每条消息只 +1**（修复前 +2），控制台**没有** `Duplicate Lark event ignored`、`duplicate trigger dropped`、`taking over a parked run`。跑得慢的那次也要这样。

第 1～8 步需要真机飞书租户。2026-09-28 跑过一轮：流程本身能走（元素级插入、正文继续写、卡上按钮可点），但第 1～3 步的审批**都没画在同一张卡上**，原因见 §6.8——那是 `element_id` 越界，不是"流式卡不能加组件"。修掉之后这几步仍需重跑确认。

格式：批量改动请用 **scoped gofmt/goimports**；`make format` 会格式化整个仓库。
