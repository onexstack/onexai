# Agent 能力聚合（spec.agents / spec.agentSelector）

## 目标

`Agent` 可以通过 `spec.skills` / `spec.mcps` / `spec.knowledgeBases` / `spec.prompts`
逐个声明能力。聚合让一个 Agent 直接复用另一个 Agent 的整套能力：

```yaml
apiVersion: agent.onexai.io/v1alpha1
kind: Agent
metadata:
  name: sre-aggregate
  namespace: helper-public
  labels:
    tier: aggregate
spec:
  agents:
    - name: base-sre
  agentSelector:
    matchLabels:
      tier: base
  skills:
    - name: local-only-skill
```

语义对标 Kubernetes 的 aggregated ClusterRole：**调用方只需要被授权到聚合 Agent 上**，
不需要对每个基础 Agent 单独绑定；基础 Agent 是"构件"，不是"被选中的 Agent"。

## 字段

- `spec.agents []Reference`：按名字聚合，同一个 namespace（`namespace` 必须为空，
  否则 admission 拒绝）。
- `spec.agentSelector *metav1.LabelSelector`：按标签聚合，只在本 Agent 所在 namespace
  匹配；显式空选择器 `{}` 匹配 namespace 内除自己以外的所有 Agent。

两者取并集。声明顺序决定 prompt 的合并顺序；selector 匹配结果按名字排序。

## 解析（controller）

`internal/controller/agent/agent/agents.go` 负责把 `spec.agents ∪ selector`
展开成传递闭包：

- **传递**：A→B→C，A 得到 B 与 C 的全部能力；
- **visited + onPath + 环检测**：菱形只计一次，环判为结构性错误；
- **上限**：最多 16 个来源（与 validation 的显式引用上限一致）、最深 8 层；
- **缺失/禁用来源**：报告为该来源不可用，不贡献任何能力；不会静默跳过；
- **结构性失败 fail-closed**：环、超深、来源数超限、合并后的 prompt 来源超限时，
  **整体丢弃继承结果**，只保留本 Agent 自己声明的能力，并让 `Ready=False`。

之所以 fail-closed：继承的不只是 skill/mcp/kb，还有 **webhooks（准入钩子）与
requireApproval（人工审批模式）**。发布"部分继承"的能力集会让这些管控被静默削弱，
所以宁可不继承。

### 状态

```yaml
status:
  resolved:
    agents: [base-sre, base-k8s]      # 传递闭包，解析顺序
    skills: [...]
    mcps: [...]
    knowledgeBases: [...]
    webhooks: [...]                   # 本 Agent ∪ 来源
    requireApproval: [...]            # 本 Agent ∪ 来源
    customCommands: [...]
  aggregatedPrompt: aggregated-sre-aggregate
  conditions:
    - type: AgentAggregated
      status: "True"                  # Aggregated / NoSources
      reason: Aggregated
```

`AgentAggregated` 为 `False` 时 `Ready` 也是 `False`——与 `PromptAggregated`
不同：prompt 的派生内容有"上一次好副本"，能力继承没有。

## Prompt 如何聚合

复用新版 Prompt 机制，不另造一套：

1. controller 先把**来源 Agent 的 prompt 来源**（`promptRef ∪ prompts ∪ promptSelector`
   解析结果）并进本 Agent 的 `promptSet`；
2. 再交给 `reconcileAggregatedPrompt`：单来源原样拷贝，多来源走 LLM 合并，写出
   `aggregated-<agent>` Prompt；
3. 输入哈希覆盖所有来源（含来源 Agent 的 prompt），所以来源 prompt 变化会触发重算；
4. 来源 Agent 自己的 `aggregated-<source>` 不会被当作来源（避免嵌套聚合），
   它自己的 `inline-<source>` 会作为来源参与合并。

来源 Agent 的 `promptAggregation.regenerateToken` 不参与；只有本 Agent 的 token 生效。

### 运行期解析

运行期先读 `status.aggregatedPrompt`：controller 已经产出派生 Prompt 时，它的内容就是
persona。派生对象正在（重新）生成、或 controller 不可用时，运行期按顺序遍历绑定列表
——先 `promptRef`，再按 `spec.prompts` 的书写顺序——使用第一个能解析的 Prompt
（存在、已启用、内容非空）。某个条目不可用会被跳过，而不是终止整轮查找。

`promptSelector` 没有运行期兜底：只有 controller 能列出 Prompt，所以选择器的匹配结果
只通过聚合后的 Prompt 到达运行期。一个都解析不到时，使用引擎的默认 instruction。

## 不继承的字段

| 字段 | 原因 |
|---|---|
| `modelRef` | 聚合贡献能力，不改变运行端点 |
| `authzBackends` | 授权后端按被请求的 Agent 名字查，来源的选择不生效 |
| `maxSkills` | 预算是本 Agent 自己的，聚合结果计入本 Agent 的预算 |

## 运行期

`agentFromCR` 把 `status.resolved` 读进 `AgentM` 的 `Resolved*` 字段（`gorm:"-"`，
不写库），运行期通过访问器取"有效集合"：

```go
a.EffectiveSkills()          // 优先 ResolvedSkills，否则 spec 声明
a.EffectiveMCPs()
a.EffectiveKnowledgeBases()
a.EffectiveWebhooks()
a.EffectiveRequireApproval()
a.EffectiveCustomCommands()
```

访问器只在 controller 已观察过该对象（`AgentM.Observed`，来自 `status.observedGeneration > 0`）时
信任 `Resolved*`，否则回落到 spec 声明。这样"controller 解析结果确实为空"（例如 binding 被撤销）
不会被误判成"尚未 reconcile"而把声明集又复活。

被切换的消费点：run spec 的并集（`internal/agent/biz/v1/agent/spec.go`）、
审批模式（`resolve.go`）、资源授权（`gate.go`）、`/v1/agent/agents/:name/context-skills`
（`internal/agent/di/providers_skill.go`）以及自定义命令（`customcommand/biz.go`）。

管理 API（`/v1/agent/agents`）仍展示 **spec 声明视图**：`AgentToCR` 不写
`Spec.Agents` / `Spec.AgentSelector`，所以一次 admin 更新不会把聚合配置展开成静态引用。
`Webhooks` 现在可以由 admin 请求体表达，沿用与其它引用列表相同的 nil/空语义：
nil 是"无意见"（用 kubectl 声明的钩子不会被无关的控制台更新抹掉），显式空数组才是清空。
聚合继承而来的钩子只出现在 `status.resolved.webhooks`，不在 `spec.webhooks`，因此这条写路径碰不到它们。

## 授权归因

`authorizeRun` 用有效集合检查调用方能否通过**被请求的 Agent** 到达这些
skill/mcp/knowledgebase，规则仍按该 Agent 名字匹配。这与 aggregated ClusterRole
一致：绑定聚合角色即可获得其聚合进来的规则，不需要绑定基础角色。

## 最终一致性

- 编辑来源 Agent 的 spec 或标签会通过 `Watches(&Agent{}, ...)` 触发同 namespace
  所有 Agent 重算（predicate 只放行 generation/label/删除事件，status 写入不会自激）；
- 来源变化到消费方 status 刷新之间有一个短暂窗口；在此期间运行期读的是上一次的
  `status.resolved`。这是派生数据的固有延迟，不是缓存 bug。

## 验收记录（2026-10-27，本地 declserver + controller-manager）

用 `kubectl --kubeconfig ~/.onexai/declserver.kubeconfig` 建了临时夹具（已清理），确认：

- 名字引用与 `agentSelector` 取并集、去重；`status.resolved` 里 skills/mcps/knowledgeBases/
  webhooks/requireApproval/customCommands 全部包含来源 Agent 的声明；
- 来源 Agent 的 prompt 进入 `aggregated-<root>`；多来源合并两种路径都实测过：
  凭据失效时走确定性 `fallbackConcat`（`PromptAggregated=False/AggregationFailed`、
  `aggregatedPromptStale=true`，内容仍保留全部来源）；轮换 `deepseek-key` 凭据并把
  `deepseek-secured.spec.model` 切到 `deepseek-v4-flash` 后，
  `PromptAggregated=True/Aggregated`（消息 `the bound prompts were merged`），产出是
  LLM 合并后的单一 prompt（双方的要求都保留），`aggregatedPromptStale`/`Overridden` 均为 false；
- 来源 Agent 的 spec（加 skill）与 label（移出 selector）变化都会触发消费方刷新
  （`kubectl wait --for=jsonpath=...` 可观察）；
- 环 → `AgentAggregated=False/CycleDetected`、`Ready=False`，只保留自身能力；
- 缺失来源 → `SourceUnavailable` + `RefsResolved=False`，但已有的 aggregate 内容保留；
- 运行期读取路径（informer → `agentFromCR` → `Effective*()`）用真实 declserver 校验通过：
  `EffectiveSkills` 是声明 ∪ 继承，而 `row.Skills` 仍是声明视图；
- 删除 Agent 后，`aggregated-<agent>` 由 ownerReference GC 自动删除。

## 已知限制

- 来源 Agent 自己声明的跨 namespace skill/mcp/knowledgebase 仍会以 `ns/name` 出现在
  status 里，但运行期 store 按名字寻址，无法区分同名对象（既有行为，与直接声明时一致）。
- `maxSkills` 超限目前只报告 `SkillsWithinBudget=False`，不会截断能力集（既有行为）。
