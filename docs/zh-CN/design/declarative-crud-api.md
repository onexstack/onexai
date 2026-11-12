# 声明式资源 REST API

`oai-agent-apiserver` 通过 `/v1/<group>/<plural>` 暴露 `oai-declserver` 中的声明式资源。路径中的 `<group>` 与 declserver 的 API group 对应（`agent`、`chat`、`core`），`<plural>` 使用 declserver 的资源复数名。

## 路由

| Group | Resource | Path |
|---|---|---|
| agent | Agent | `/v1/agent/agents` |
| agent | AgentTemplate | `/v1/agent/templates` |
| agent | AgentBinding | `/v1/agent/agentbindings` |
| agent | Skill | `/v1/agent/skills` |
| agent | Prompt | `/v1/agent/prompts` |
| agent | Model | `/v1/agent/models` |
| agent | MCPServer | `/v1/agent/mcpservers` |
| agent | KnowledgeBase | `/v1/agent/knowledgebases` |
| agent | Memory | `/v1/agent/memories` |
| agent | CustomCommand | `/v1/agent/customcommands` |
| agent | Webhook | `/v1/agent/webhooks` |
| agent | AuthorizationRule | `/v1/agent/authorizationrules` |
| agent | RuntimeProfile | `/v1/agent/runtimeprofiles` |
| agent | RuntimeHosting | `/v1/agent/runtimehostings` |
| agent | SandboxTemplate | `/v1/agent/sandboxtemplates` |
| agent | ScheduledRun | `/v1/agent/scheduledruns` |
| core | Secret | `/v1/core/secrets` |
| chat | Command（只读） | `/v1/chat/commands` |

## 业务线与凭据的落点

业务线（Namespace）不是 declserver CRD，而是一个 `corev1.Namespace`：展示名、描述、负责人和更新时间放在
`onexai.io/display-name`、`onexai.io/description`、`onexai.io/owner`、`onexai.io/updated-at` 四个
annotation 里，labels 使用 Namespace 原生 labels。`agent.namespace` 行表和反向 Syncer 已删除，
`GET /v1/agent/namespaces` 直接读控制面（Get 走 API server，List 走 informer）。

Secret 存储在 core Secret 中，带 `onexai.io/secret-kind=aksk` 标签；`/v1/core/secrets` 的请求/响应
形状保持不变。SecretID 是 API 身份，core Secret 名称由它确定性映射；`List` 只返回带该标签的对象，
不会把 Model/MCP/Agent 的凭据列出来。

## 已废弃的声明式对象

以下对象仍由 declserver 提供（便于读取历史数据或迁移），但运行时已不再读写，调用方不应再建立新依赖：

| 对象 | 说明 |
|---|---|
| `chat.onexai.io/v1alpha1 DutyGroup` | 值班组已是 chat 自有的 SQL 资源（`chat.oncall_group` 及排班表） |
| `chat.onexai.io/v1alpha1 BotApp` | chat 从服务配置读取 Lark 应用，不读该对象 |
| `agent.onexai.io/v1alpha1 ScheduledRun` | 调度已迁移到 `batch.onex.ai CronJob`；`/v1/agent/scheduledruns` facade 仍受支持 |

对 `/apis/...` 下这些资源的请求会得到标准 `Warning: 299 - "..."` 响应头。`/v1/agent/scheduledruns`
是受支持的 facade，不会带该警告。

## 调度与批处理作业

调度已迁移到标准的 `batch.onex.io/v1beta1` Job / CronJob，由 `oai-declserver`
提供 API、`oai-job-controller` 负责 reconcile。一次定时 agent 运行就是一个
`jobTemplate.spec.type: agentrun` 的 CronJob，每次触发产生一个子 Job。

`/v1/agent/scheduledruns` 保留为兼容 facade：读写同一个 CronJob，
`/executions` 列出子 Job，`/trigger` 创建一个手动子 Job。新客户端应直接使用
CronJob 资源。

`ScheduledRun` 对象不再由 `oai-agent-apiserver` reconcile。升级后先运行一次
`oai-scheduledrun-migrator` 把现有对象复制为 CronJob；迁移完成后 facade 只读写
batch 资源。

每个可写资源提供 5 条路由：

```text
POST   /v1/<group>/<plural>
GET    /v1/<group>/<plural>            ?offset=&limit=
GET    /v1/<group>/<plural>/:name
PUT    /v1/<group>/<plural>/:name
DELETE /v1/<group>/<plural>/:name
```

请求/响应体与 `agents`、`skills` 等既有接口保持一致：创建/更新使用 `{"<kind>": {...}}` 包装，列表返回 `{"<plural>": [...], "count": n}`。

```json
POST /v1/agent/agentbindings
{
  "agentBinding": {
    "name": "base",
    "namespace": "default",
    "spec": {
      "agents": ["helper"],
      "commands": ["bind"],
      "prompts": [{"name": "base-prompt"}]
    }
  }
}
```

```json
GET /v1/agent/agentbindings?offset=0&limit=20
{
  "agentBindings": [{ "...": "..." }],
  "count": 1
}
```

`status` 由 controller 维护：请求中可以携带，但创建/更新不会改变它（declserver 的 strategy 负责保留/清空）。

## Agent 的 prompt 绑定

Agent 的 persona 是**有序的 Prompt 名字列表**（CRD 里是 `spec.prompts`，管理面消息里是 `prompts`）。controller 把 `promptRef ∪ prompts ∪ promptSelector` 合并成 `aggregated-<agent>`，运行期优先使用它；派生对象尚未产出时，运行期按列表顺序回退，使用第一个存在、已启用且内容非空的 Prompt。解析规则见 [agent 聚合](agent-aggregation.md)。

管理面消息的 `prompts` 是唯一的 prompt 拼写：单值的 `sysPrompt` 字段已移除，各资源消息里改名前的 `workspace` 字段也一并移除。它们的 proto 字段号已 `reserved`，不得复用。自由文本不再接受——请写成 Prompt 对象再绑定。

## 统一鉴权

资源面只挂载一次，中间件同时接受两种凭据：

1. **AK/SK 签名**：服务间调用，行为与既有 `/v1` signed 面一致。
2. **Bearer JWT**：浏览器控制台调用，必须携带 admin claim。

选择按“凭据是否存在”决定，不做降级：只要出现任一 AK/SK 头，就只走签名校验；签名失败不会回退到 JWT。`X-Onex-Control-Token` 仅对 AK/SK 路径的 `/v1/agent/authorizationrules` 与 `/v1/core/secrets` 生效；JWT admin 不需要第二个凭据（与既有 `/v1/admin` 语义一致）。

## 命名空间

- Create：`metadata.namespace` > `?namespace=` > `controlplane.namespace` > `helper-public`。
- Get/Update/Delete：`?namespace=` > 配置的 scope > 跨命名空间查找 > 默认命名空间。
- List：`?namespace=` > 配置的 scope > 全部命名空间。

## 错误

统一使用 `core.WriteResponse` 的错误信封：

```json
{"reason":"NotFound","message":"...","metadata":{}}
```

Kubernetes 错误映射：`NotFound` → 404，`AlreadyExists`/`Conflict` → 409，`Invalid`/`BadRequest` → 400，`Unauthorized` → 401，`Forbidden` → 403，其余 → 500。

## Command 迁移

`Command` 已从 `agent.onexai.io` 迁到 `chat.onexai.io`：

- 命令体编译进 chat 二进制，`internal/chat/commandreg` 是唯一写入方，并在启动及每 10 分钟 reconcile；
- 因此接口放在 `oai-chat-apiserver`，且**只读**（`GET /v1/chat/commands`、`GET /v1/chat/commands/:name`）；
- 对 `Command` 的写入会被 reconcile 覆盖；需要可写的自定义命令请使用 `CustomCommand`；
- 换组后旧 etcd 对象不会自动迁移，新对象由 commandreg 自动重建，旧对象需要一次性清理。

## 破坏性变更

原有 `/v1/agents`、`/v1/skills`、`/v1/admin/<resource>` 等路径已统一迁移到 `/v1/agent/<plural>`。调用方与前端需要同步升级；如需过渡期，可在后续版本增加旧路径别名。
