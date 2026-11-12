# 外部 A2A 智能体

这份文档记录 onexai 如何**注册、发现和调用第三方 A2A 智能体**，以及它为什么这样落地。
入站协议面（onexai 作为 A2A 服务端）见 [a2a.md](a2a.md)；本文只讲出站。

## 一、定位

一个 `Agent` 有两个正交维度：

| 维度 | 取值 | 含义 |
|---|---|---|
| `spec.kind` | `native` / `external` | 行为由平台定义，还是在第三方注册 |
| `spec.execution.mode` | `inproc` / `a2a` | 在本进程执行，还是通过 A2A 远端执行 |

合法组合三种：平台智能体（`native + inproc`）、运行在 A2A Runtime 的平台智能体
（`native + a2a`）、注册的第三方 A2A 智能体（`external + a2a`）。`external + inproc`
非法：外部智能体没有平台提示词、模型和工具。

外部智能体**不新建 CRD**，只是同一个 `Agent` 资源的一种形态。目录、授权、Chat、
`calls` 和引用关系因此都只有一套，而不是两套各自演化的模型。

外部智能体禁止平台行为字段（提示词、模型、Skill/MCP/知识库、审批等），由校验层拒绝；
`kind` 创建后不可变。

## 二、注册与卡片发现

注册来源三种：

- `card-url`：给出 AgentCard 地址；
- `json`：直接给出卡片 JSON；
- `manual`：手工填写端点、协议版本与传输方式。

`POST /v1/agent/agents/preview` 抓取并归一化一张卡片，**不落库**，失败返回 `ok=false`。
注册向导用它做实时预览。

控制器（`internal/controller/agent/agent`）负责抓卡并写 `status.a2a`：

- 发现顺序：显式 `cardUrl` → 源站根 `/.well-known/agent-card.json` → 旧版
  `agent.json` → endpoint 下的 well-known；
- 同时理解 A2A **0.3**（`message/send|stream`、`tasks/get|cancel`）与 **1.0**
  （`SendMessage`、`SendStreamingMessage`、`GetTask`、`CancelTask`），
  业务层只消费归一化后的 DTO；
- 写入协议版本、传输方式、解析后的绝对端点、能力、技能、卡片摘要与版本历史；
- 抓卡失败时保留最后一张好卡片，把错误写进 `lastSyncError`，状态降级而不是清空；
- 卡片中的相对接口地址（如 `/v1/chat/a2a`）按卡片/端点解析为绝对地址。

所有出站请求都经过 `internal/pkg/netguard`：默认拒绝私网地址，只有部署显式放开时才允许。

## 三、出站调用链路

| 位置 | 职责 |
|---|---|
| `internal/agent/pkg/a2aclient` | 不依赖 SDK 的 JSON-RPC 客户端；0.3/1.0 双栈；SSE 解析 |
| `internal/agent/pkg/a2ainvoke` | 一次调用的编排：建传输、同步/流式、轮询终态、取消、落调用记录 |
| `internal/agent/pkg/a2atool` | `a2a_invoke` 工具，供平台智能体按 `calls` 调用外部智能体 |
| `internal/agent/biz/v1/agent/resource.go` | 管理面 invoke API |
| `internal/agent/biz/v1/agent/external.go` | Chat 直连外部智能体 |

`a2ainvoke.Service` 是这三条入口（管理面 invoke、`a2a_invoke` 工具、Chat）共用的唯一
实现：同样的凭据解析、同样的超时与轮询、同样的调用记录。三条路径各写一份，是目前最
容易开始互相不一致的地方。

同步调用遇到非终态（`submitted`/`working`）会轮询到终态，但**不轮询**
`input-required` / `auth-required`：那两个状态等的是调用方，不是时间。

超时优先级：调用方显式传入 → 智能体的 `execution.a2a.timeoutSeconds` → 服务默认（120s）。
这个顺序只在 `a2ainvoke.callTimeout` 里实现一次，调试面板、`a2a_invoke` 工具和 Chat
因此不会对同一个智能体给出不同的截止时间。Chat 还会把本次 run 的 deadline 作为显式值。

## 四、鉴权与凭据

支持 `none` / `apiKey` / `bearer` / 自定义 Header / OAuth2 Client Credentials /
`aksk`（onexai 自身的签名格式）。

- 凭据写入 `<name>-a2a-credentials` Secret，从不回传到读接口；
- `clearCredentials` 清除、切换鉴权类型整体替换、空凭据保留不变；
- `aksk` 逐请求签名：`X-API-KEY / X-TIMESTAMP / X-NONCE / X-SIGN`，
  签名串包含 method、path、body MD5、timestamp、nonce；
- 控制器的抓卡与运行时的调用共用同一份凭据解析，不存在“抓卡能过、调用不能过”。

## 五、调用记录

每次出站调用写一行 `agent.a2a_invocation`：

- `source`：`console` / `chat` / `agent-tool`；
- 调用方身份、父 run、taskId、contextId、状态、耗时、工件；
- 输入输出按 `datamask` 脱敏后入库，单个字段有长度上限。

记录是“谁让哪个对端做了什么”的唯一答案，也是 Chat 与调试面板展示历史的数据源。

## 六、Chat 直连

Chat 选中一个外部智能体时，这一轮**不走引擎**：`AgentBiz.Ask` 在准入之后把请求交给
`a2ainvoke.Stream`，由对端执行。

- 只有 `len(agents) == 1` 且该 agent 是 `external` 才走这条路；平台智能体的路径完全不变；
- 外部智能体带 `composition`（测试模式）或多选时直接拒绝：对端没有平台能力可以收窄，
  也没有第二个对端可以合并；
- `contextId` 取会话 ID，对端若支持上下文可以延续；
- 只有文本会被发送；Chat 对外部智能体隐藏图片/附件入口与调试台；
- 事件的 `delta` / `artifact` 文本转换成 `answer` 增量，终态转换成 `done`；
  对端用一个完整 task 快照回答时，快照文本同样会成为答案；
- `input-required` / `auth-required` 在 Chat 中作为本轮回答展示并结束：Chat 没有可续跑
  的引擎 checkpoint，续聊应该去详情页的「调试」或「调用记录」；
- 准入、配额、并发槽、run 记录、会话落库与平台智能体一致，run 的 runtime 记为 `a2a`，
  调用记录 `source=chat`；
- 停止只取消这次出站调用，不取消 run 的上下文：回合以 `stopped` 收尾，run 记录正常结算为
  `canceled`，调用记录记为 `canceled`（与引擎的安全点停止同构）；
- 服务端未接线 `a2ainvoke` 时明确报错，绝不回落到引擎 —— 那会用部署默认助手冒名回答。

## 七、`calls` 联动

`Agent.spec.calls` 可以引用任意 Agent。控制器解析到 `status.resolved.calls`：

- native 目标并入 `Delegates`，走既有 `agent_invoke_agent`；
- external 目标进入 `a2a_invoke` 调度工具，参数为 `agent`（enum 只列可用外部智能体）/
  `message` / `context_id` / `task_id`；
- 校验目标存在、启用、external 必须有已同步卡片、跨空间需要 `sharing` 允许。

## 八、前端面

- `/agents`：统一目录，页签 `全部 / 平台智能体 / A2A 智能体`，类型列区分平台与 A2A；
- `/agents/new?type=external`：注册向导，四种来源、鉴权、实时卡片预览；
- Agent 详情：`AgentCard`、`技能` 页签，`调试`（流式 invoke、`input-required` 续聊）、
  `调用记录`；
- Chat：外部智能体可直接选择，带 `（A2A）` 标识；测试模式、调试台、图片与附件对它隐藏；
- `/a2a-agents` 兼容重定向到 `/agents?tab=external`。

## 九、API

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/v1/agent/agents` | 创建（含 external） |
| POST | `/v1/agent/agents/preview` | 抓卡预览，不落库 |
| POST | `/v1/agent/agents/:name/invoke` | 同步调用 |
| POST | `/v1/agent/agents/:name/invoke/stream` | 流式调用（SSE） |
| GET | `/v1/agent/agents/:name/tasks/:taskId` | 远端任务快照 |
| POST | `/v1/agent/agents/:name/tasks/:taskId/cancel` | 取消远端任务 |
| GET | `/v1/agent/agents/:name/invocations` | 调用记录列表 |
| GET | `/v1/agent/agents/:name/invocations/:invocationId` | 单条调用记录 |

## 十、边界与已知限制

- 外部智能体的能力、模型与工具完全由对端决定，平台不代理、不注入；
- Chat 只发送文本，不发送图片、附件、`calls` 或平台能力；
- Chat 遇到 `input-required` 不会自动续跑，续聊在详情页调试面板完成；
- 出站网络受 `netguard` 约束；需要访问私网对端时由部署显式放开；
- 一个外部智能体只对应一个卡片、一个端点；多接口的 1.0 卡片取声明的主接口。
