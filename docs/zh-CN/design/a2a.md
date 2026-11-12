# A2A 协议面

`oai-agent-apiserver` 通过 [A2A](https://a2a-protocol.org) 把 agent 能力暴露给其他服务。
这份文档记录这一层**做了什么、刻意不做、以及为什么**，改动前请先读。

## 一、分层

| 位置 | 职责 |
|---|---|
| `internal/agent/pkg/a2a` | 路径常量、AgentCard、三个 transport 的构造、`Executor`（请求映射与事件翻译）、身份拦截器 |
| `internal/agent/handler/a2a.go` | gin 桥接：路由注册、AK/SK 中间件、连接超时清理、输出上限注入、入站审计适配（`NewA2AGuard`） |
| `internal/agent/di/providers_a2a.go` | 只做接线：配置 → `Options` → `NewHandlers` |

依赖方向固定为 `handler → pkg/a2a → pkg/agent`。`Path`/`PathREST` 只在 `pkg/a2a` 写一次，
传输路由、前缀剥离与卡片广告的接口 URL 都从它们派生——改挂载点不会留下一个仍指向旧地址的卡片。
`PathCard` 则刻意独立：它别名 SDK 的 `a2asrv.WellKnownAgentCardPath`，因为发现路径按 RFC 8615
固定在源站根，而不是传输挂载点之下。

## 二、挂载与鉴权

| 路径 | 方法 | 鉴权 |
|---|---|---|
| `/v1/chat/a2a` | POST | AK/SK 签名 |
| `/v1/chat/a2a/rest/*` | 任意 | AK/SK 签名 |
| `/.well-known/agent-card.json` | GET | **公开** |

卡片必须公开：客户端取卡时还没有任何凭据。它也因此**不能列出内部 agent 清单**——
那是一份会被匿名读者拿走的资产目录。协议面的存在不依赖配置开关：三条路由在任何部署里
都存在，没有 agent 引擎时每个任务以 "the agent engine is not configured" 结束。

卡片发布在**源站根路径**，不在传输前缀之下：客户端在知道任何传输 URL 之前就要取到它。
原 `/v1/chat/a2a/.well-known/agent-card.json` 已移除——不提供别名，也不做重定向。
服务若部署在带路径前缀的反向代理之后，必须把源站根的 well-known 路径路由到本服务；
一个源站只能发布一张 agent card。

> 落地细节：gin 的中间件是沿组继承的，所以**卡片不能注册在 `/v1` 组里**——那样写出来的
> 路由同样要签名，客户端永远取不到卡。它由服务端包在 `InstallRESTAPI` 里直接挂到
> router 上（与 `/healthz`、`/media` 同一处），位置是源站根的 well-known 路径；
> handler 包只提供 `A2ACard`。

## 三、卡片契约

- `skills` 是**空数组而不是 null**。该字段没有 `omitempty`，nil 会序列化成 `null`，
  严格的客户端会直接解码失败。
- 鉴权用 `apiKey`（header `X-API-KEY`）+ description 说明签名要求。SDK 的
  `SecurityScheme` 是封闭联合（APIKey/HTTPAuth/mTLS/OAuth2/OIDC），表达不了 HMAC 签名，
  所以另有扩展 `…/extensions/aksk-signature/v1` 声明签名头与签名串：
  `METHOD \n PATH \n hex(md5(body)) \n X-TIMESTAMP \n X-NONCE`。
  只带 `X-API-KEY` 的通用 A2A 客户端会被 401。
- `agent` / `agents` 的选择约定由扩展 `…/extensions/agent-selection/v1` 声明。
- URL：`agent.a2a-public-url` 是**两个传输接口的绝对基地址**，不是卡片自身位置；配置校验会
  拒绝相对值。留空时卡片带路径相对值（同源取卡可用、严格客户端不可用）。**不默认从请求头推导**：
  卡片路由无鉴权，伪造 `Host`/`X-Forwarded-Host` 能把对端指向别处；只有显式打开
  `agent.a2a-trusted-proxy` 才信任转发头，且此时 `a2a-public-url` 必须为空（校验会拒绝两者同时设置）。
- `version` 取构建注入的 `version.Get().GitVersion`。

## 四、身份

1. AK/SK 中间件把签名者放进请求上下文（`authz.SubjectFromService(apiKey)`）。
2. 身份拦截器（`a2asrv.WithCallInterceptors`）把它写进 SDK 的 `CallContext.User`——
   这是 SDK 自己的身份通道，任务归属校验读的就是它。
3. `Executor` 取 `execCtx.User.Name` 作为运行的 `Subject`，缺失时回落
   `authz.SubjectFromContext`，再缺失才记作 `a2a-executor` 并告警（本地关掉鉴权时会出现）。

已知限制：`biz` 的 `subjectForRun` 目前一律构造 **用户** 主体，所以 A2A 调用在运行期
表现为"名为该 AK 的用户"，服务账号这一 Kind 不保留。要按服务账号授权，需要让
`AskRequest` 能直传主体。

## 五、会话与任务

- **会话键 = `contextId`**（回落 `taskId`）。A2A 的"同一对话"就是同一 context 下的多个任务；
  用 taskId 作键会让"新任务 + 相同 contextId"的续聊丢记忆。
- **run id = `taskId`**，因此 `Cancel` 能直接 `Stop(runID)`（跨副本生效，前提是配置了 Redis）。
  同一任务的多轮复用同一个 run id：停表按 run id 注册/注销，所以是安全的，但审计里的
  `run_id` 会重复。
### 集群模式（配置了 Redis 时自动开启）

四件东西都进 Redis，缺一件就退回 SDK 的进程内实现：

| 组件 | 键 | 作用 |
|---|---|---|
| `RedisTaskStore` | `<prefix>:agent:a2a:task:<id>` + 每用户 ZSET `:tasks:<user>` | 任务本身与列表；重启不丢、多副本可读；归属校验读 `CallContext.User.Name` |
| `RedisEventQueue` | `<prefix>:agent:a2a:events:<id>`（Stream） | 一条任务的**事件流**；resubscribe 换副本也能从头发回，24h 过期 |
| `RedisWorkQueue` | `<prefix>:agent:a2a:work`（Stream + 消费组）+ `:lease:exec|cancel:<id>` | 执行分发与租约；租约 2 分钟、心跳续期；10 分钟未确认的条目会被别的副本接管 |
| `CallContextCodec` | 随工作项序列化 | 把调用方（A2A user）、authz subject、输出上限带到**执行副本**，否则那边的运行会退化成"以面自身为调用者" |

事件队列用 Stream 而不是 Pub/Sub：Pub/Sub 不会回放订阅之前的事件，而"断线之后补上漏掉的"
正是 resubscribe 的全部意义。回放是安全的——SDK 的订阅先给出库里任务的快照，再丢弃版本号不高于
快照的事件，所以从头读不会重复答案。

**仍然做不到的**：跨副本的*实时*接管。执行中的副本如果崩溃，任务会停在运行态直到租约过期；
被接管的是**排队未执行**的条目与重新订阅，不是正在跑的 goroutine。真正的崩溃接管需要工作项
按租约分片迁移，属于下一层设计。

SDK 的降级路径也要知道：`SubscribeToTask` 在拿不到订阅时会返回**一帧任务快照**然后结束
（`handler.go:371-383`）。客户端看到非终态快照应当继续用 `tasks/get` 轮询，而不是把连接关闭
当作任务结束。

这个降级见过一次：双副本联调时有一次重订阅只回了一帧 WORKING 快照（0.02s 返回）。之后
4 次真机 + 10 次自动化都没能复现。为便于下次定位，现在有两处 debug 行：
`a2a: subscribing to a task's events`（reader 是否建起来）与
`a2a: masking a task owned by someone else as not found`（是否被归属掩蔽挡掉）。

### 自动化测试

`cluster_e2e_test.go` 用 miniredis + 两个 `NewHandlers` 实例模拟两个副本：一个请求在 A 上
断流、从 B 重订阅并跑到终态；以及从 B 取消 A 上正在跑的任务。两者都在进程内跑完，不依赖
外部 Redis/PG/模型。

## 六、事件映射

| agent 事件 | A2A 表现 |
|---|---|
| `EventAnswer` | 首个分片开一个 artifact，后续分片追加到**同一个 artifactID** |
| `EventFailed` | `failed` + 脱敏原因（上游错误只进日志） |
| `EventStopped` | `canceled` |
| `EventSuspended` / `EventInterrupted` | `failed` + "needs a human approval, which this surface cannot ask for" |
| `EventToolStep`/`EventProgress`/`EventReasoning`/`EventImage`/`EventSources`/`EventUsage`/`EventDone` | 丢弃 |

丢弃是决定而不是遗漏：工具步骤与推理在 A2A 里没有表示；图片是本站的引用（asset id 或需
凭据的 URL），发给对端等于给一张取不到的图。

输入接受文本与**图片 URL**（`image/png`、`image/jpeg`、`image/webp`），图片会转成
`media.Ref{URL, MimeType}` 交给运行——这是全仓统一的"传引用不传字节"形态。卡片
`defaultInputModes` 如实声明这四种。

内联字节（raw part）与结构化 data part **显式失败**，而不是悄悄只取文本：前者等于让
图片穿过每一层持有消息的地方，后者是本面没有的能力。

## 七、边缘策略

A2A 与 chat 面共用两道边缘策略，接线在 `handler/a2a.go`：

- **入站审计**：`NewA2AGuard` 把 adapter 的 `Guard` 端口接到 `screenInput`，与 chat 面同一个
  screener、同一个指标计数、同一条 interaction 记录。被拒的输入不会花一次模型调用。
- **输出上限**：桥接层用 `h.askContext(c)` 把按调用方 AK 解析出的上限挂到请求上下文。
- **连接超时**：桥接层先 `detachConnDeadlines`，否则服务端的绝对写超时会掐断流式回答。
- 可观测性的 skip 规则必须覆盖 `/v1/chat/a2a/` 前缀（含 `/rest` 子树），否则 REST 流会被
  body-capturing writer 包住。源站根路径的卡片则刻意**不**跳过：它是一次没有请求体、
  也不带凭据的小型公开 GET。

## 八、配置

```yaml
agent:
  a2a-public-url: ""      # 两个传输接口发布的绝对基地址（不是卡片位置）；空 = 相对路径
  a2a-trusted-proxy: false
  a2a-default-agent: ""   # 请求未指定 agent 时的默认值
  a2a-description: ""     # 卡片描述；空则回落到 agent.description
```

仓库配置（`configs/examples/oai-agent-apiserver.example.yaml`）与本机配置（`~/.onexai/oai-agent-apiserver*.yaml`）
必须同步更新。
