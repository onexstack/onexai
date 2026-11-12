# AgentBinding：按 label selector 批量绑定能力

## 目标

给一批 Agent 施加同一套平台能力（prompt / skill / knowledge base / MCP /
custom command / 平台命令），不再逐个修改 Agent 的 spec。

```yaml
apiVersion: agent.onexai.io/v1alpha1
kind: AgentBinding
metadata:
  name: platform-base
  namespace: default
spec:
  selector:                 # 可选；与 agents 至少有一个
    matchLabels:
      platform: "true"
  agents: [helper]          # 可选点名，与 selector 取并集
  prompts: [{name: base-prompt}]
  skills: [{name: k8s-debug}]
  knowledgeBases: [{name: kb-runbook}]
  mcpservers: [{name: gitlab}]
  customCommands: [{name: triage}]
  commands: [bind, oncall]   # 精确点名
  commandSelector:           # 可选；按命令目录标签整组授予，与 commands 并集
    matchExpressions:
      - {key: agent.onexstack.io/group, operator: In, values: [duty]}
```

## 语义：纯并集，零冲突

```
effective(agent) = agent.spec 自有 refs
                 ∪ agent.spec 各 XxxSelector 命中
                 ∪ 所有 enabled 且 (selector 命中 agent.Labels | spec.agents 含 agent)
                   的 AgentBinding 贡献
```

对标 Kubernetes 的 RBAC / NetworkPolicy / ClusterRole `aggregationRule`：

- 绑定只做加法，**没有 deny、没有 priority、没有覆盖**；
- 重复引用按 `(kind, namespace/name)` 或命令名去重，结果排序；
- 所以"冲突"在模型上不存在——两个绑定引用同一个对象，结果就是同一个对象。

"收回"靠不再选中（改 label、删除绑定），而不是写一条负向规则。

## 生效方式

- **只在 controller 合并**：Agent controller 把贡献写进
  `agent.status.resolved`，并记录 `status.bindingContributions`（来源、代数、计数，
  最多 64 条，超出置 truncated）与 `resolvedHash`；
- 运行时读 status；绑定变化 → hash 变化 → `runnercache` 的 fingerprint 失效并重建图；
- 暂停中的 run 依赖 graph 地址稳定，因此 `runnercache.Key` 仍然只是 agent 名集合，
  fingerprint 只做新鲜度判断，pinned entry 跨 fingerprint 变化继续保留。

不把结果写回 `agent.spec`：那是 PodPreset 被移除的原因。

## Command 目录

命令体编译在 chat 二进制里（`internal/chat/pkg/command`），`Command`
（`agent.onexai.io/v1alpha1`）是它的目录条目：

- chat 启动时 reconcile 一次，之后每 10 分钟对账；
- 只清理带 `onexai.io/managed-by=oai-chat-apiserver` 且已不在 catalog 里的对象；
- AgentBinding 解析时对照 `Command` 校验：不存在或被禁用 → `status.missing`，
  `RefsResolved=False`；`skill:<name>` 是运行时技能命令，原样透传；
- 因此"绑定了一条已下线的命令"会显式可见，而不是静默不授权。

`spec.commandSelector` 是批量轴：每个 `Command` 都带
`agent.onexstack.io/group=<组名>`，所以 `group in (duty)` 会授出整个 duty 组，
**包括以后新增到该组的命令**。语义要点：

- 与 `spec.commands` 取并集；显式名字是"钉子"，标签是"跟随目录"；
- chat 运行时按编译期目录的标签展开（规则重建时），控制器按目标命名空间里的
  `Command` 对象展开到 `status.resolved.commands`；两者结果一致；
- **空选择器 `{}` 不是"全部命令"**：`metav1.LabelSelectorAsSelector({})` 会得到
  `Everything()`，所以写路径直接拒绝空选择器；selector 命中 0 条只是授予 0 条，
  不写 `status.missing`。

## 边界与容量

| 项 | 规则 |
|---|---|
| disabled agent | 不参与匹配；绑定侧计数与 agent 侧合并使用同一规则 |
| disabled 资源 | 视为不存在，进 `status.missing` |
| 跨 namespace 引用 | 允许；目标必须在其 `allowedNamespaces` 中放行引用方。见 [跨 namespace 共享](cross-namespace-sharing.md)。批量跨 ns 下发用 `ClusterAgentBinding` |
| contributions | 只存计数与来源，最多 64 条；能力并集不截断 |
| 空 selector `{}` | 选中本 namespace 全部 enabled agent，并有 condition 标记 |

## 权限

- 创建/更新 binding 时，调用者必须对被引对象有 `get`、对 agents 有 `list`
  （selector 是批量授权的闸门），失败返回 403 并产生 Event；
  selector 的 TOCTOU 是已知取舍：后续新匹配的 agent 不再逐个校验。
- RBAC 模式下，chat 的身份需要 `commands` 的
  `get/list/watch/create/update/delete`（目录 reconcile 会写这些对象）。

## 与 CommandGrant 的关系

`CommandGrant` 已删除。命令授予统一走两种绑定：精确名单在
`spec.commands`，整组批量在 `spec.commandSelector`（两者并集）。
历史对象用 `hack/commandgrant2binding` 转换；该工具在转换时把 selector 按当时的
catalog 标签展开成名字——那是迁移工具的选择，不是 API 的限制，新对象可以直接
写 `commandSelector` 让授权跟随目录。展开为空则保持为空，忠实保留原效果。
