# Agent 模板

## 目标

Agent 模板是一个具名、可复用的行为单元。它承载 prompt、model、能力引用和运行契约，让一个团队把行为定义一次，多个 Agent 直接引用。

模板不是 Agent：它没有身份、没有 runtime、没有授权。这三者保留在 Agent 上，模板因此可以跨运行时、跨安全边界复用。

```yaml
apiVersion: agent.onexai.io/v1alpha1
kind: AgentTemplate
metadata:
  name: incident-response
  namespace: default
spec:
  enabled: true
  prompts:
    - name: incident-persona
  skills:
    - name: triage
  modelRef:
    name: gpt-4o
  requireApproval:
    - "delete_*"
---
apiVersion: agent.onexai.io/v1alpha1
kind: Agent
metadata:
  name: oncall-helper
  namespace: default
spec:
  enabled: true
  templateRefs:
    - name: incident-response
  modelRef:
    name: gpt-4o-mini   # Agent 自己的值优先
```

## 语义

```text
effective(agent) = agent.spec
                 ∪ template[0].spec
                 ∪ template[1].spec
                 ∪ selector 匹配
                 ∪ 聚合的 Agent
                 ∪ AgentBinding
```

- **集合字段取并集。** 模板的 prompts、skills、MCPs、知识库、自定义命令、webhooks、审批模式和 delegates 都会并入 Agent 自身的集合。
- **单值字段按声明顺序回退。** model、outputSchema、memory、sessionTools 取第一个声明它们的模板值；Agent 自己显式声明的值永远优先。
- **模板必须同命名空间。** 跨命名空间的模板在准入阶段被拒绝。
- **模板不能嵌套。** 模板不能引用模板；组合发生在消费端，Agent 可以按顺序引用多个模板。
- **runtime 与授权不随模板走。** `runtime.profileRef`、`authzBackends` 和身份只属于 Agent。

## 生效方式

1. 创建 `AgentTemplate`，以及一个在 `spec.templateRefs` 中引用它的 `Agent`。
2. Agent 控制器解析模板、合并行为，并写入 `status.resolved`（含 `status.resolved.templates`）。
3. 控制器 watch `AgentTemplate`；编辑模板会重新评估该命名空间内的所有 Agent，因此改动无需编辑 Agent 即可传播。
4. 运行时读取 `status.resolved`，与 selector、聚合 Agent、AgentBinding 的读取路径一致。

模板缺失或被禁用时不贡献任何行为，并上报 `TemplatesApplied=False` 与 `RefsResolved=False`，让“只解析了一半”的 Agent 可见，而不是静默降级。

删除仍被 Agent 引用的模板会被 `agentreference` 准入插件阻止，错误信息会列出阻塞的 Agent。

## 边界

| 边界 | 取值 |
|---|---|
| 每个 Agent 引用的模板数 | 16 |
| 嵌套 | 不允许 |
| 命名空间 | 与引用它的 Agent 相同 |
| 运行时选择 | 仅 Agent |
| 授权 | 仅 Agent |
| 传播方式 | 控制器 watch，无需编辑 Agent |

## 与其他资源的关系

- `Prompt` 保存 prompt 文本；模板引用 Prompt，不内联。
- `RuntimeProfile` 决定 Agent 在哪里运行；模板不参与。
- `AgentBinding` 给一批 Agent 追加能力；模板是 Agent 主动引用的行为单元，两者都是增量语义，可以同时使用。
- Agent 聚合（`spec.agents`）合并其他 Agent 的能力；模板不是 Agent，不能聚合。

## 管理面

`Agent` 的 REST 消息通过 `templateRefs` 暴露同一份 `spec.templateRefs`（名称数组，保持声明顺序）：

- 省略该字段表示「无意见」，不会清掉用 kubectl 写下的引用；
- 显式传 `[]` 表示清空。

控制台的「智能体模板」页把模板的 JSON `spec` 拆成结构化表单：提示词、模型、能力引用、运行契约与高级选择器。顶层的「状态」开关在保存时同步为 `spec.enabled`——后端在 `spec` 非空时只认这个字段。表单未覆盖的 spec 字段（如旧版的 `promptRef`）在编辑保存时原样保留。

## 权限

管理模板使用与其他声明式资源相同的 `agenttemplates` 资源权限。REST 接口挂载在 `/v1/agent/templates`。
