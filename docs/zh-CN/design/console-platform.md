# 智能体平台控制台（管理面设计与边界）

> 前端在 `onex-console` 仓库的 `apps/agent`（应用名「智能体平台」，挂在 OneX 课程平台的
> `实战项目 → Go 项目平台`）。本文只写**服务端**这一侧：新增的路由、鉴权模型、以及几条
> 刻意的边界。

## 1 为什么需要这一层

`oai-agent-apiserver` 的 `/v1` 全组由 AK/SK 签名守卫，浏览器不可能持有密钥；而 8 类资源
（agent / skill / mcp / 知识库 / prompt / model / secret / 授权规则）此前只能被服务间调用。
运营要看一眼「平台上有哪些 Skill、哪个 agent 绑了哪些知识库」，今天只能去翻数据库。

所以新增第二个面：**同一批资源，另一套鉴权**。

```
浏览器（Bearer 令牌）
   ├── /v1/admin/*      → oai-agent-apiserver :9181   ← 本次新增（管理员）
   ├── /v1/auth/login   → oai-chat-apiserver  :9180   ← 唯一签发点
   ├── /v1/tickets      → oai-chat-apiserver  :9180   ← 本次新增（管理员，只读）
   └── /v1/oncall-groups、/v1/conversations …（JWT；值班组读登录可读、写仅管理员）
```

## 2 两个面，两套鉴权

| | 服务间面 | 管理面 |
| --- | --- | --- |
| 前缀 | `/v1/*` | `/v1/admin/*` |
| 鉴权 | AK/SK HMAC（`X-API-KEY`/`X-TIMESTAMP`/`X-NONCE`/`X-SIGN`） | `Authorization: Bearer`（HS256） |
| 主体 | 一个服务（`secretId`） | 一个人（`user_id`） |
| 授权 | 无（签名即全部权利） | 管理员角色 |

路由在 `internal/agent/httpserver.go` 的两个分组上注册，中间件在**注册时固化**在各自的
路由链上——这正是两块面能共存的前提：管理面的请求不会经过 AK/SK 校验（它无法满足），
服务间面的请求也不会被要求持有令牌。反向也成立，所以一条管理面路由**不可能**因为漏写
而落到签名面之外。

`internal/agent/handler/admin.go` 与 `admin_debug.go` 用的是 `RegisterAdmin`（第二个注册
表），不是 `Register`：`ApplyTo` 会把**全部** registrar 挂到给定分组上，复用它会把服务间
面整套重复注册到 `/v1/admin` 下。

## 3 鉴权模型：名单只有一份，角色在签发端盖章

- **名单只有一份，且不在配置里**：SRE 与管理员名单是 `chat.setting` 的两行
  （`onexai-sre-user-id` / `onexai-admin-user-id`，值是 Lark user_id 的 JSON 数组），
  由 `internal/chat/pkg/roleset` 缓存并每分钟重读；`admin = 管理员行 ∪ SRE 行`。
  两个面（飞书/MCP 的 `StaticRuntime`、签发令牌的 `AuthBiz`）读的是**同一个缓存实例**，
  所以不会出现「飞书面上是管理员、管理面上不是」。此前它们来自
  `lark.sre-user-ids` / `chat.roles.admin-user-ids`，改一次要改配置加重启。
- **签发端盖章**：`POST /v1/auth/login` 按登录身份查这份缓存，把结果写进令牌的 `is_sre` /
  `is_admin` claim。下游服务只读 claim，不读名单——**令牌里的角色是签发那一刻的**，
  与名单后来的改动无关（`jwt.expired` 默认 24h）。
- **两个 claim 不许合并**：两个集合在缓存里分开存，因为 `x-sre-only` 与 `x-admin-only`
  门住不同的命令，合并会让前者变成后者。`internal/pkg/authn/bearer` 的 `RequireAdmin`
  因此只看 `is_admin`。
- **空表 = 谁都不是**（fail-closed）：没有这一行就没有这个角色，`/doctor` 是唯一能看清
  当前名单人数与读取时间的入口。
- **令牌跨服务可用**：两个服务的 `jwt.key` 必须是同一个值（配置注释里有这一条）。此前四个
  key 值互不相同，chat 签的令牌在 agent 上验不过。

### 3.1 这套身份**不是**验证式的，别把它当认证

登录的三种 provider 都无法校验凭据：`local` 接受任意非空用户名口令，`lark` 把一个字符串
当身份，`apikey` 同理。所以登录证明的是「你知道一个被部署方列在名单里的名字」。

这不是本次引入的，但**把它接到浏览器面之后，它的权重变了**：以前 REST 调用者的 `is_sre`
恒为 false（名单从不被 REST 读取），所以这个缺口没有出口；现在它会签发带 `is_admin` 的令牌。
生产必须接真实身份源（飞书 SSO，或信任 OneX 的 iam），否则管理面等于开放。本机用它是因为
`chat.login.allow-local` 只在那份本机配置里打开，且启动会打 INSECURE 告警。

## 4 调试面（`/v1/admin/agent/*`）

`POST /v1/admin/agent/ask|approve|stop`，形状与 `/v1/agent/*` 同源，但有三处不同：

1. **共用 `AgentAskRequest`，但不接受服务端拥有的字段**。三条 ask 入口（`/v1/agent/ask`、
   `/v1/admin/agent/ask`、`/v1/user/agent/ask`）使用同一份请求消息：内容在 `input`，目标在
   `selection`，生成参数在 `generation`。浏览器面显式提交 `subject`/`actor`/`sessionId`/
   `metering`/`options`/`metadata`/`model`/`selection.invokedSkills`，或 `generation` 里除
   `reasoning` 之外的参数，会以 400 拒绝；执行主体由服务端从登录态或 agent 的 `owner` 推导。
   边界只有一处实现：`internal/agent/pkg/validation/console_ask.go`。原面之所以能把 `senderID`
   放在请求体里，是因为调用方是 AK/SK 背书的可信服务；浏览器没有这个前提。
2. **两个身份分开记**。运行**执行**在 agent 的 owner 名下（否则连它的授权门都过不去——
   每个用户一条规则，管理员没有规则），而**审计**记的是按下按钮的管理员。
   `AskRequest.ActorID/ActorName/ActorIsSRE` 就是为此存在的；不区分的话，审计会把管理员
   的动作记到一个没做过这件事的用户头上。
3. **runID 由服务端铸造并带 `dbg-` 前缀**。`/approve` 与 `/stop` 只认这个前缀，因为那两个
   端点接受任意的 runID，而部署里还有真实用户的会话——一次误批就是批准了别人会话里的工具调用。

**不落库、不计费**：不写 `chat_session`/`chat_message`（调试不是任何人的会话），不写用量。
`interaction` 审计会记一行（`Source = "console"`）——管理员以平台身份运行，是必须留痕的事。

事件形状：上游用**数字** 0..10 表示类型（`AgentStreamEvent.Type`，数字就是枚举的声明顺序）。
管理面把它映射成字符串联合后再下发，映射在
`internal/agent/pkg/agentstream/console.go`（第二个 `StreamSink`）；两条流因此有各自的
sink，而 `handler` 里只共用连接/超时那一段（`streamWithSink`）。

| 字符串事件 | 含义 |
| --- | --- |
| `delta` | 增量文本；`reasoning` 区分推理流与回答 |
| `step` | 工具步骤（名称/状态/耗时/tokens/id/**result**） |
| `usage` | 累计用量（`Done` 之前单独下发一次） |
| `interrupt` | 审批/提问暂停：本段流结束，**run 未结束**，据此调 `/approve` |
| `suspended` | 因暂停而终流。与 `interrupt` 分开是因为「已结束」这个字样会盖掉界面上唯一还能做的动作 |
| `final` | 终态；被停止时 `text` 为空，含义是「没有更多内容」而不是「回答是空的」 |
| `error` | 终态失败 |

**`suspended` 是上游没有的一档。** 上游用 `EventSuspended` 表示「流结束但 run 在等回答」，
数字形状下它和 `EventDone` 只能靠枚举值区分；字符串联合里给它一个名字，前端才能不把
「暂停」画成「结束」。

**工具 `result` 在管理面**会下发**，在 chat 面不会**：chat 面的 sink 把步骤转成生成的 proto
消息，那条消息没有 result 字段；管理面自己映射，所以能带上。`agent.ToolStep.Result` 两侧
都被填充（见其注释），所以这是形状差异，不是数据缺失。截图式证据：本机实测让调试运行查时间，
回答里出现「刚才查到的是 2026-10-06 16:41，时区 +08:00」——那就是工具结果进了模型上下文，
而它同时也在 `step` 事件里到了浏览器。

## 5 主动不做的事

- **不做工单的写面**。工单的生命周期属于值班命令与飞书卡片（`/open` `/hand` `/close` `/mark`），
  那些路径还要把它镜像到事件平台（Regen）。第二个写者会让两边不一致，而**没有任何一方能发现**
  ——镜像本来就是单向的。所以 `GET /v1/tickets` 只有读。
- **不改 Agent 与知识库的读写能力**。它们的 CRUD 一直在服务间面上，管理面**对称地**也暴露
  （否则只是安全表演）；但控制台把它们渲染成只读，因为 OneX 侧的对账（会员档变化、内容同步、
  下一轮对话）会重写 `prompts`/`namespace`/`status`/`knowledgeBases`。要真正禁止，得改 biz
  的权限判定——那是一个产品决定，不是本层能替它做的。
- **不给会话面加隔离**。`/v1/conversations` 返回的是机器人所在的所有群（服务端丢弃调用者
  身份），`/v1/search` 是全库匹配。这两个事实**写在了前端页面上**，而不是靠不显示来回避。
- **不接 Casbin**。README 说「基于 Casbin 的授权」，但 Casbin 只出现在套话、生成器模板与 RAG
  语料里；`internal/pkg/known/role.go` 的 `RoleUser`/`RoleAdmin` 全仓无人引用。真正的角色模型
  是 §3 那两份名单。

## 6 验证（已在本机跑过）

临时实例（chat :9280 / agent :9281，独立 HOME 避开单实例锁），用 `-c` 指向改过端口的配置副本：

| 请求 | 结果 |
| --- | --- |
| `GET :9281/v1/admin/agents`（无令牌） | 401 |
| 同上（chat 签发的非管理员令牌） | 403 |
| 同上（chat 签发、名单内的令牌） | 200，`{"agents":[…],"count":N}`，毫秒时间戳 |
| `GET :9281/v1/agent/agents`（无 AK/SK 签名） | 401（服务间面未受影响） |
| `GET :9280/v1/conversations`（修 `AlwaysDeny` 之前恒 403） | 200 |
| `GET :9280/v1/tickets`（管理员 / 非管理员） | 200 / 403 |
| `POST :9281/v1/admin/agent/{approve,stop}`（runID 不带 `dbg-`） | 400 |

单元测试：`internal/pkg/authn/bearer`（含「无 claim 必须 fail-closed」）、
`internal/agent/handler`（管理面路由集合是签名面的子集 + 每条管理面路由对非管理员 403）、
`internal/chat/biz/v1/auth`（名单内/外的角色盖章）。

### 6.1 浏览器走查（Playwright 驱动本机 Chrome）

子应用独立起（:3010，`PUBLIC_ONEXAI_{CHAT,AGENT}_BASE_URL` 指向临时实例的 :9280/:9281，
**不动正在运行的 9180/9181**），18 项全通过、控制台无 error、无失败请求：

- 未登录渲染登录页并给出声明式身份提示；登录后侧边栏两组 11 个入口齐全；
- 智能体 10 行、工单 9 行、值班组 5 行、会话 200 行，均为**真实数据**；
- 智能体只读（无新建入口），Skill 有新建入口且弹窗字段来自描述符；
- 智能体详情三个页签（基本信息/关联配置/**调试**），只读原因写在页面上；
- 会话页明确写出「没有归属隔离」。

调试面真实跑通：输入问题 → `POST /v1/admin/agent/ask` 200 → 面板渲染、模型回答与用量
（输入 486/输出 121/推理 81/调用 1）→ 「已结束」。停止路径：`POST /v1/admin/agent/stop` 200，
**部分回答保留**（这一点是本次修掉的：`final` 事件在停止时 text 为空，直接覆盖会把模型
已经说出的部分擦掉）。

走查还抓到一个 typecheck 抓不到的真缺陷：描述符把 `idKey` 字段同时声明成 extraColumn 时，
它与框架的「ID/名称」列同 `key`，antd 去重会**整列丢掉**——于是列表里没有任何进详情的链接。
已修（并把这条例外写进 `FieldSpec.column` 的注释）。

### 6.2 驱动这个界面时要知道的两件事（下次省时间）

- **antd 的两字按钮名会变成「登 录」**（`autoInsertSpace` 在两个中文字之间插空格），按名字
  找按钮要用容忍空格的正则。
- **带图标的 antd 按钮，可访问名包含图标的 `aria-label`**：「发送」按钮实际叫「send 发送」，
  锚定匹配一个都找不到。
- `Alert` 的 `message` 在 antd 6 已弃用（应用侧已全部改为 `title`）。

## 7 普通用户面

控制台不再只是管理面：登录用户要能浏览平台目录、创建自己的资源并与之对话。

| 面 | 前缀 | 鉴权 | 能力 |
| --- | --- | --- | --- |
| 声明式资源面 | `/v1/agent/*`、`/v1/core/*` | Bearer（任意登录用户） | 读全平台；写按 owner 隔离 |
| 用户运行面 | `/v1/user/agent/*` | Bearer（任意登录用户） | 以**本人**身份运行，受运行授权门约束 |
| 管理面 | `/v1/admin/*` | Bearer + `is_admin` | 同前，调试运行以 agent owner 身份执行 |

- **认证与授权分开**：`UnifiedAuth` 只回答「你是谁」，Bearer 分支不再要求管理员；
  策略集中在 `internal/agent/handler/user_guard.go` 的 `UserGuard`。这样同一组路由可以
  同时服务共享目录与治理资源。
- **治理资源仍是管理员**：`/v1/core/secrets`、`/v1/agent/authorizationrules` 在
  `governancePaths` 里整条前缀拦截；前端菜单同步隐藏「凭据管理」「访问控制」「用户管理」。
- **写操作按 owner 隔离**：带 `owner` 列的资源（agent/model/namespace/runtimeprofile/
  scheduledrun/webhook）只有 owner 或管理员能改；创建时服务端把 owner 改写为调用者，
  不依赖客户端自觉。没有 owner 列的资源（skill/MCP/知识库/prompt/…）是共享构件，
  登录用户都可写。
- **owner 可以运行自己的智能体**：`authorizeRun` 对 owner 跳过 agent 级策略检查，但
  技能/MCP/知识库的引用检查照旧（拥有智能体不等于拥有它引用的资源）。普通用户创建的
  智能体因此不需要额外写一条 `oai_authz_rule` 才能对话。
- **两套运行端点**：管理员继续用 `/v1/admin/agent/*`（以 owner 身份执行，便于调试他人
  的智能体）；普通用户用 `/v1/user/agent/*`（以本人身份执行）。两面的 runID 前缀不同
  （`dbg-` / `usr-`），审批与停止只接受自己铸造的 ID。

## 8 尚未做

- **生产网关没有 onexai 的路由**。`/onex/agent/*` 指向的是 OneX 平台的 `ox-agent-apiserver:8188`，
  与 onexai 无关，不能复用。本机开发走子应用自带的代理（`apps/agent/rsbuild.config.ts`）。
- **本机 `chat.setting` 里没有管理员行**。名单现在是表里的两行（见第 3 节），
  「没有这一行 = 谁也不是」，所以管理面与 `/agent` 这类管理员命令对所有人拒绝。
  要用它，先写这两行之一：

  ```sql
  INSERT INTO chat.setting (key, value, description, created_at, updated_at)
  VALUES ('onexai-admin-user-id', '["你的user_id"]', '管理员 user_id 列表(JSON 数组)',
          extract(epoch from now())::bigint, extract(epoch from now())::bigint)
  ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at;
  ```

  一分钟内生效，不必重启；`/doctor` 会打印当前人数与名单读取时间。
- **管理面还没有读写这份名单的接口**。store 层的 `SettingStore`（Get/Set）已就位，
  但 `/v1/admin/settings` 还没做，且它有一个自举问题要先解决：鉴权本身读的就是这份名单。
