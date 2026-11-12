# 自定义命令 `/xxxx`

自定义命令让业务方自己声明一个斜杠命令（如 `/deploy`），按需绑定到 Agent，
在飞书里通过 `/help` 被发现，并由 `oai-agent-apiserver` 通过标准 REST API 执行。

它和内置命令的关系是：**解析方式完全相同**（chatserver 用 cobra/pflag 构造命令、
生成 usage、解析位置参数与 flag），只是命令的「执行体」由资源声明，而不是编译进代码。

## 1. 资源模型

```yaml
apiVersion: agent.onexai.io/v1alpha1
kind: CustomCommand
metadata:
  name: hello            # 即 /hello；小写字母/数字/连字符，≤32
  namespace: default
spec:
  enabled: true
  displayName: 打招呼
  description: 打印一段问候
  type: prompt           # prompt | http | tool
  usage: "<name> [--formal]"
  parameters:
    - name: name
      in: positional     # flag（默认）| positional
      type: string       # string|integer|number|boolean|duration|array|object|json
      required: true
    - name: formal
      type: boolean
      short: f
      description: 正式语气
  prompt:
    template: "{{ if .Params.formal }}您好{{ else }}你好{{ end }}，{{ .Params.name }}！"
```

`metadata.name` 是唯一的事实来源，别名写在 `spec.aliases`。参数是**每命令自定义的强类型列表**，
不是固定字段；`object`/`array`/`json` 参数可用 `schema` 声明受控 JSON Schema 子集。

校验在写入期完成：类型与执行体 one-of、参数唯一性、`required`/`default` 互斥、
模板可编译且字段引用在允许范围内、URL scheme/host 字面量、`sideEffects` 白名单等。

`spec.usage` 只用于展示（`/help` 示例、控制台会话命令列表）；错误提示里的
`用法:` 由 `parameters` 自动生成，避免手写 usage 与参数声明不一致。模板字段是
`.Params`（复数）：`.Param`、未声明的 `.Params.x`、未知的 `.Conversation.x` /
`.User.x` 会在写入期被拒绝。

`configs/examples/customcommands.yaml` 提供了 prompt、http、tool 三个稍复杂的
示例命令，可作为控制台表单的参考。

## 2. 绑定到 Agent

```yaml
apiVersion: agent.onexai.io/v1alpha1
kind: Agent
metadata: { name: helper, namespace: default }
spec:
  enabled: true
  customCommands:
    - name: hello
  # 也可以用 customCommandSelector 按 label 选择
```

**绑定即授权**：没有第二层授权对象（`CommandGrant` 已删除，命令统一由 `AgentBinding.spec.commands` 授予）。Agent 的 `status.resolved.customCommands`
由 controller 写入，chatserver 据此构造命令树；取消绑定后下一次解析（默认 15s TTL）就不再出现。

## 3. REST API

| 方法 | 路径 | 说明 |
|---|---|---|
| POST/GET | `/v1/agent/customcommands` | 创建 / 列表 |
| GET/PUT/DELETE | `/v1/agent/customcommands/{name}` | 读取 / 更新 / 删除 |
| POST | `/v1/agent/customcommands/{name}/invoke` | 执行 |
| POST | `/v1/agent/customcommands/{name}/approve` | 回答需审批的 tool 调用（`allow`/`deny`） |
| GET | `/v1/custom-command-audits` | 调用历史（`command/agent/userId/phase/offset/limit`） |
| GET | `/v1/agent/agents/{name}/context-commands` | 该 Agent 绑定且启用的命令摘要 |
| — | `/apis/agent.onexai.io/v1alpha1/customcommands` | 控制面标准动词（kubectl） |

`/v1` 走 AK/SK 签名；`invoke` **必须带 `agent`**，服务端会校验该 Agent 确实绑定了该命令。

### invoke 契约

请求是「提案」，响应回填 `status`：

```json
{
  "uid": "可选，用于关联日志",
  "agent": "helper",
  "user": { "username": "go.kong", "groups": ["sre"] },
  "context": { "conversationId": "oc_xxx", "chatType": "group", "surface": "lark" },
  "params": { "name": "世界", "formal": true },
  "dryRun": false
}
```

```json
{
  "uid": "...", "kind": "prompt", "name": "hello",
  "status": {
    "phase": "Succeeded",
    "params": { "name": "世界", "formal": true },
    "usage": "/hello <name> [--formal]",
    "prompt": { "prompt": "您好，世界！" }
  }
}
```

`phase` 三态：

- `Denied`：确定性的不允许（命令/Agent 不存在、Agent 未绑定、Agent 禁用、需要审批）。
- `Failed` + `evaluationError` 非空：调用没完成（DNS/连接/超时/解析）。
- `Failed` + `evaluationError` 为空：执行了但失败（上游 4xx/5xx、tool 报错）。

`warnings` 会作为卡片脚注展示，`auditAnnotations` 只进日志。

## 4. 参数解析

- chatserver 按声明动态注册 pflag、位置参数、枚举/JSON 校验和 `PreRunE` 必填检查；
  `/help <cmd>` 与 `--help` 由 cobra 原生生成。
- chatserver 发送的是**结构化 `params`**，不是命令行原文；apiserver 用同一份声明复核类型、
  枚举、默认值和 schema。
- 省略的可选参数会以类型零值参与模板渲染，因此 `{{ if .Params.formal }}` 可用；
  模板中引用未声明的键仍然报错。
- 写入期会静态检查模板字段；运行期仍会对动态引用给出可用字段提示。`spec.usage`
  不再进入错误行，错误里的用法始终来自参数声明。

## 5. 三种执行体

### prompt

渲染 `spec.prompt.template` 后返回 `kind=prompt`，由 chatserver 作为一轮普通 AI turn 执行
（流式卡片、审批、历史都复用）。该类型仅 Lark surface 可用。

模板数据：`.Params`、`.Command`、`.Agent`、`.Conversation`、`.User`、`.DryRun`、`.Now`。

### http

代理外部 REST 调用：

```yaml
  http:
    method: POST
    url: "https://api.example.com/deploy/{{ .Params.service }}"   # 或 service{namespace,name,port,path}
    headers: [{name: X-Env, value: "{{ .Params.env }}"}]
    body: '{"service":"{{ .Params.service }}"}'
    sideEffects: None        # None | NoneOnDryRun | Some | Unknown
    secretRef: {name: my-token}   # 以 Bearer 发送，不落日志
  output:
    format: json             # text | json | template
```

- `url` 与 `service` 二选一；`url` 的 scheme/host 必须是字面量。
- `url` 目标默认禁私网/回环，可配 `allowed-hosts` 白名单；
  `service` 目标是集群内 Service，用单独允许私网的 fetcher。
- `sideEffects` 只有 `None|NoneOnDryRun` 允许 `dryRun`；dryRun 会带 `X-OnexAI-Dry-Run: true`。

### tool

调用已绑定 Agent 的 MCP server 上的一个 tool：

```yaml
  type: tool
  tool:
    mcpServer: {name: onexai}
    tool: convention_coding
    argumentsTemplate: '{}'
```

执行前校验 MCP 已绑定、`allowedTools` 允许。命中 `requireApproval`（部署、Agent 或 MCP 任一层）
时不执行，而是返回 `phase=PendingApproval` 与 `status.approval`（审批 id、工具、渲染后的参数、过期时间）：

- 飞书会渲染「允许 / 拒绝」卡片；点击后以**点击人**的身份调用 `approve`。
- `approve` 只接受发起人本人、15 分钟内、单次有效；允许后执行**审批记录里存储的参数**，
  不接受调用方重新传参，防止审批后换参数。
- MCP 同步工具调用没有审批 UI，命中审批时直接拒绝。

## 6. 配置

`oai-agent-apiserver`：

```yaml
agent:
  custom-command:
    allowed-hosts: []            # 空=任意公网 host；支持 "*.example.com"
    allow-plain-http: false
    allow-private-hosts: false
    max-timeout: 60s
    max-response-bytes: 1048576
    max-redirects: 2
```

`oai-chat-apiserver`：

```yaml
chat:
  agent-command:
    customs-ttl: 15s             # 绑定后多久在 /help 生效
```

### 可观测性

- 指标：`<prefix>_custom_command_total{type,phase}` 与
  `..._custom_command_duration_seconds{type}`；`prefix` 在开启对齐开关时为 `onexai_agent`，
  否则沿用旧的 `agent`。命令名不作为 label（用户自定义、基数不可控）。
- 审计日志：每次 invoke 一行 `custom command invoked`，包含
  `uid / command / agent / kind / phase / reason / conversation / surface / user / duration_ms`；
  不记录 `params`、prompt 正文、响应体或任何凭据。
- 审计表：同字段写入 `agent.custom_command_audit`（AutoMigrate 创建），
  通过 `GET /v1/custom-command-audits` 按 `command/agent/userId/phase` 过滤、`offset/limit` 分页查询。
  写失败只记 warning，不影响已完成的命令。

## 7. 本地验证

```bash
kubectl --kubeconfig ~/.onexai/declserver.kubeconfig apply -f customcommand.yaml
kubectl --kubeconfig ~/.onexai/declserver.kubeconfig get customcommands
# NAME    TYPE     ENABLED   PARAMS   AGE
# hello   prompt   true      2        5s

kubectl --kubeconfig ~/.onexai/declserver.kubeconfig patch agent helper --type merge \
  -p '{"spec":{"customCommands":[{"name":"hello"}]}}'
kubectl --kubeconfig ~/.onexai/declserver.kubeconfig get agent helper \
  -o jsonpath='{.status.resolved.customCommands}'
```

之后在飞书里 `/help` 会看到「自定义命令」分组，`/hello 世界 --formal` 直接执行。

## 8. MCP

自定义命令会作为 MCP tool 暴露给 agent：
- **工具清单由 chatserver 周期性重读**（`GET /v1/agent/customcommands`，默认
  `mcp.refresh-interval: 10s`），新增/修改/删除命令后**无需重启 chatserver**，最迟一个刷新周期
  出现在 `tools/list`；每次 `tools/list` / `tools/call` 仍会按调用者绑定的 Agent 过滤
  （`AllowsCustom`，其 customs 缓存默认 15s），未绑定的命令不可见也不可执行。
- **前置条件**：Agent 必须绑定提供命令目录的 MCP Server（例如 `oai-chat-apiserver`
  暴露的 `/mcp`）。否则命令只是聊天斜杠命令，不会成为该 Agent 的 MCP tool。
- `prompt` 类型在 MCP 上返回**渲染后的指令文本**（不启动 AI turn），`http` / `tool` 类型正常执行。
- 命中 `requireApproval` 的 tool 命令在 MCP 上直接拒绝（同步工具调用没有审批 UI），请走 AI 对话路径。
- agent 侧的工具发现缓存会在 Agent/绑定/命令/MCP Server 变更时失效，并在失效后的收敛窗口内用短 TTL
  反复探测，不需要重启 `oai-agent-apiserver`。

## 9. 当前边界

- 不做资源级 OPA；invoke 的授权是「AK/SK 服务身份 + Agent 绑定 + Agent 启用状态」。
- selector-only 绑定依赖 controller 先写入 `status.resolved`；未 reconcile 前回退到显式引用。
  命令/绑定变化后，Agent controller 会因 binding status 变化重新 reconcile，不再需要手工触发。
- 跨命名空间同名自定义命令仍按全局命令树去重（先到先得）；不同命名空间的 Agent 应使用各自空间内的命令。
- 命令调用历史是追加的审计表，不提供删除接口。
