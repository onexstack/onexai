# Custom commands `/xxxx`

Custom commands let a business team declare its own slash command (e.g. `/deploy`), bind it to an Agent as needed, have it discovered via `/help` in Lark, and have it executed by `oai-agent-apiserver` through a standard REST API.

Its relationship with built-in commands: **the parsing method is exactly the same** (chatserver builds commands with cobra/pflag, generates usage, parses positional arguments and flags); only the command's "execution body" is declared by a resource rather than compiled into code.

## 1. Resource model

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

`metadata.name` is the single source of truth; aliases are written in `spec.aliases`. Parameters are a **per-command strongly-typed list**, not fixed fields; `object`/`array`/`json` parameters can use `schema` to declare a controlled subset of JSON Schema.

Validation is completed at write time: type and execution-body one-of, parameter uniqueness, `required`/`default` mutual exclusion, template compilability and valid field references, URL scheme/host literals, `sideEffects` whitelist, and so on.

`spec.usage` is display-only (the `/help` example and the console session-command list). The `用法:` line in errors is generated from `parameters`, so a hand-written usage cannot disagree with the declaration. Template fields are `.Params` (plural): `.Param`, undeclared `.Params.x`, and unknown `.Conversation.x` / `.User.x` are rejected at write time.

`configs/examples/customcommands.yaml` contains slightly more complex prompt, http and tool examples that can be used as form references.

## 2. Binding to an Agent

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

**Binding is authorization**: there is no second-layer authorization object (`CommandGrant` has been removed; commands are granted uniformly through `AgentBinding.spec.commands`). The Agent's `status.resolved.customCommands` is written by the controller, and chatserver constructs the command tree from it; after unbinding, the next resolution (default 15s TTL) no longer includes it.

## 3. REST API

| Method | Path | Description |
|---|---|---|
| POST/GET | `/v1/agent/customcommands` | Create / list |
| GET/PUT/DELETE | `/v1/agent/customcommands/{name}` | Read / update / delete |
| POST | `/v1/agent/customcommands/{name}/invoke` | Execute |
| POST | `/v1/agent/customcommands/{name}/approve` | Answer a tool call needing approval (`allow`/`deny`) |
| GET | `/v1/custom-command-audits` | Call history (`command/agent/userId/phase/offset/limit`) |
| GET | `/v1/agent/agents/{name}/context-commands` | Summary of commands bound and enabled for that Agent |
| — | `/apis/agent.onexai.io/v1alpha1/customcommands` | Standard control-plane verbs (kubectl) |

`/v1` uses AK/SK signatures; `invoke` **must include `agent`**, and the server validates that the Agent is indeed bound to that command.

### invoke contract

The request is a "proposal", and the response fills in `status`:

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

`phase` has three states:

- `Denied`: deterministically not allowed (command/Agent missing, Agent not bound, Agent disabled, approval required).
- `Failed` + non-empty `evaluationError`: the call did not complete (DNS/connection/timeout/parsing).
- `Failed` + empty `evaluationError`: executed but failed (upstream 4xx/5xx, tool error).

`warnings` are shown as a card footnote; `auditAnnotations` go only to logs.

## 4. Parameter parsing

- chatserver dynamically registers pflags, positional arguments, enum/JSON validation, and `PreRunE` required checks according to the declaration; `/help <cmd>` and `--help` are generated natively by cobra.
- chatserver sends **structured `params`**, not the raw command line; apiserver re-validates types, enums, defaults, and schema against the same declaration.
- Omitted optional parameters participate in template rendering as the type's zero value, so `{{ if .Params.formal }}` works; referencing a key not declared in the template still errors.
- Template fields are checked statically at write time; the runtime also reports the available fields for dynamic references. `spec.usage` no longer appears in errors; the usage line always comes from the parameter declarations.

## 5. Three execution bodies

### prompt

Renders `spec.prompt.template` and returns `kind=prompt`, which chatserver executes as an ordinary AI turn (streaming card, approval, history are all reused). This type is available only on the Lark surface.

Template data: `.Params`, `.Command`, `.Agent`, `.Conversation`, `.User`, `.DryRun`, `.Now`.

### http

Proxies an external REST call:

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

- `url` and `service` are mutually exclusive; the `url`'s scheme/host must be literals.
- `url` targets are private-network/loopback-blocked by default, configurable via the `allowed-hosts` whitelist; `service` targets are in-cluster Services, using a separate fetcher that allows private networks.
- `sideEffects` allows `dryRun` only for `None|NoneOnDryRun`; dryRun carries `X-OnexAI-Dry-Run: true`.

### tool

Calls a tool on an MCP server of an already-bound Agent:

```yaml
  type: tool
  tool:
    mcpServer: {name: onexai}
    tool: convention_coding
    argumentsTemplate: '{}'
```

Before execution it validates that the MCP is bound and that `allowedTools` permits it. When `requireApproval` is hit (at the deployment, Agent, or MCP layer), it does not execute but returns `phase=PendingApproval` with `status.approval` (approval id, tool, rendered arguments, expiry):

- Lark renders an "allow / deny" card; clicking calls `approve` **as the clicker**.
- `approve` accepts only the initiator themselves, within 15 minutes, single-use; after allowing, it executes **the arguments stored in the approval record** and does not accept re-sent arguments from the caller, preventing argument swaps after approval.
- MCP synchronous tool calls have no approval UI; when approval is hit they are denied directly.

## 6. Configuration

`oai-agent-apiserver`:

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

`oai-chat-apiserver`:

```yaml
chat:
  agent-command:
    customs-ttl: 15s             # 绑定后多久在 /help 生效
```

### Observability

- Metrics: `<prefix>_custom_command_total{type,phase}` and `..._custom_command_duration_seconds{type}`; `prefix` is `onexai_agent` when the alignment switch is on, otherwise the old `agent` is retained. Command names are not labels (user-defined, unbounded cardinality).
- Audit log: one `custom command invoked` line per invoke, containing `uid / command / agent / kind / phase / reason / conversation / surface / user / duration_ms`; does not record `params`, prompt text, response bodies, or any credentials.
- Audit table: the same fields are written to `agent.custom_command_audit` (created by AutoMigrate), queryable via `GET /v1/custom-command-audits` filtered by `command/agent/userId/phase` with `offset/limit` paging. Write failures only log a warning and do not affect the completed command.

## 7. Local verification

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

Afterwards, `/help` in Lark shows a "自定义命令" group, and `/hello 世界 --formal` executes directly.

## 8. MCP

Custom commands are exposed to agents as MCP tools:
- **The tool list is re-read periodically by chatserver** (`GET /v1/agent/customcommands`, `mcp.refresh-interval`, default `10s`). Added/changed/deleted commands appear in `tools/list` **without restarting chatserver**, at most one refresh interval later. Every `tools/list` / `tools/call` is still filtered by the caller's bound Agent (`AllowsCustom`; its customs cache is 15s), and unbound commands are neither visible nor executable.
- **Prerequisite**: the Agent must bind the MCP server that publishes the command catalog (for example the `/mcp` endpoint of `oai-chat-apiserver`). Without it a command is only a chat slash command and never becomes that Agent's MCP tool.
- The `prompt` type returns **the rendered instruction text** over MCP (it does not start an AI turn); `http` / `tool` types execute normally.
- Tool commands hitting `requireApproval` are denied directly over MCP (synchronous tool calls have no approval UI); use the AI conversation path instead.
- The agent-side tool-discovery cache is invalidated when an Agent, binding, command or MCP server changes, and probes repeatedly with a short TTL during the settling window. `oai-agent-apiserver` does not need a restart.

## 9. Current boundaries

- No resource-level OPA; invoke authorization is "AK/SK service identity + Agent binding + Agent enabled state".
- selector-only binding depends on the controller having written `status.resolved` first; before reconciliation it falls back to explicit references. After a command/binding change the Agent controller reconciles again on the binding status update, so no manual trigger is needed.
- Same-named custom commands across namespaces are still deduplicated by the global command tree (first wins); agents in different namespaces should use the command in their own namespace.
- Command invocation history is an append-only audit table with no delete API.
