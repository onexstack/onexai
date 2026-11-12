# 跨 Namespace 共享能力与批量绑定

> **实现更新**：跨空间授权已从资源自身的 `spec.allowedNamespaces` 改为目标空间的
> `ReferenceGrant`。Agent spec 只允许本地引用；跨空间能力通过 AgentBinding /
> ClusterAgentBinding 注入。本文下方描述 `allowedNamespaces` 的段落保留为历史设计，
> 当前语义以 `ReferenceGrant` 为准。
>
> **本次补充（2026-11）**：
>
> - 引用即身份：运行时看到的引用一律是 `namespace/name`；裸名只在引用方空间已知
>   的边界上解析一次。裸名在集群内同名多份时**拒绝**并提示写限定名，不再按 store
>   遍历顺序取第一个。
> - 无宿主调试台（playground）的引用方是控制台当前业务空间，请求通过
>   `composition.namespace` 携带；为空时回退部署默认空间。页头选择会同时影响裸名
>   解析与 grant 判定。
> - 可共享 kind 表收敛为 MCP / Skill / KnowledgeBase / Prompt / Memory / Model；
>   CustomCommand、Webhook、RuntimeProfile、Agent/AgentTemplate/delegate/call
>   只能同空间。Model 的跨空间引用由 `modelRef`（Agent / Binding）走 grant；
>   每轮显式传入的 `model` 覆盖仍按 owner 归属判定，不做 namespace 门。
> - 多 Agent 运行要求**每个**引用方都被放行（不是任一），否则一个租户的 grant
>   会扩大另一个租户的运行能力。
> - grant 索引来自 informer；运行期判定将拒绝时做一次 live 读取，避免
>   「刚创建 grant 就运行」的竞态。

## 目标

平台管理员把一批平台级能力（MCP、Skill、知识库、Prompt、Memory）一次绑定给分散在多个
namespace 里的一批 Agent，而不是逐个 namespace 复制资源、逐个 Agent 改 spec。

## 三层模型

```text
调用方（Agent / Binding）
   └── 引用：Reference{namespace, name}，运行时一律按 (namespace, name) 寻址
          └── 授权：资源自身的 spec.allowedNamespaces
                 └── 批量：集群级 ClusterAgentBinding
```

### 1. 引用：永远全限定

`Reference{Namespace, Name}` 早已存在。本次把"省略 namespace"的含义固定为**引用方所在
namespace**，并在转换层把每个引用规范化成 `namespace/name`：

- `internal/agent/store/controlplanestore/convert.go` 的 `qualifiedNames` / `promptRefsFromCR`；
- 运行时统一用 `model.ResolveRef` 解析，再用 `model.EntryFilter` 生成
  `where.F("name", ...).F("namespace", ...)`。

这修掉了一个既有缺陷：运行时此前只按裸 name 集群级查找，同名资源跨 namespace 时结果
取决于遍历顺序，且限定名 `ns/name` 反而查不到、被静默丢弃。

REST 面仍是 `repeated string`：同 namespace 写裸名，跨 namespace 写 `ns/name`
（`model.BareEntries` 负责这个边界）。因此控制台与已有客户端不需要改协议。

### 2. 共享授权：资源侧白名单，默认拒绝

MCP / Skill / KnowledgeBase / Prompt / Memory 统一：

```yaml
spec:
  allowedNamespaces:   # 省略/空 = 仅本 namespace 可用
    - team-a
    - "*"              # 全平台
```

- 语义：`own namespace ∪ allowedNamespaces`；空列表**不是**全开。
- 唯一实现：`pkg/apis/agent/sharing`。准入、控制器、运行时必须调用同一个函数。
- 拒绝信息保持模糊（`namespacepolicy.go` 的做法），不暴露对方租户。

准入、控制器与运行时三个位置都校验，但职责不同：

| 位置 | 检查什么 | 失败后果 |
|---|---|---|
| 准入 | 引用形状、namespace 格式 | 拒绝写 |
| 控制器 | 目标存在、启用、允许引用方 | `status.missing` + condition |
| 运行时 | 最终闸门（`checkTargetPolicies`） | 运行失败 |

选择器（`skillSelector` 等）**只在引用方 namespace 内匹配**，跨 namespace 只能显式点名。

### 3. 批量：ClusterAgentBinding

`ClusterAgentBinding` 是集群级对象，对标 K8s `ClusterRoleBinding`：

```yaml
apiVersion: agent.onexai.io/v1alpha1
kind: ClusterAgentBinding
metadata:
  name: platform-base
spec:
  namespaces: [team-a, team-b]        # 显式列表，不用 selector
  agentSelector:
    matchLabels: {platform: "true"}
  agents:
    - {namespace: team-c, name: helper}
  prompts:        [{namespace: platform, name: base}]
  skills:         [{namespace: platform, name: k8s-debug}]
  knowledgeBases: [{namespace: platform, name: runbook}]
  mcpservers:     [{namespace: platform, name: gitlab}]
  memoryRef:      {namespace: platform, name: team-memory}
```

- **纯并集**，与 `AgentBinding` 相同：无 deny、无 priority、无覆盖。
- 受众先用 `namespaces` 划界，`agentSelector` 只在这个范围内生效。
- namespace 列表是显式的，不是 label selector：新建的 namespace 不应自动继承平台能力。
- `memoryRef` 是单值：Agent 自身声明优先；两个绑定给出不同 Memory 是结构性冲突，
  通过 `MemoryConflict` condition 报告，而不是按名字随便挑一个。
- 所有引用必须带 namespace——集群级对象没有"自己的 namespace"可以默认。

## 合并顺序

```text
effective(agent) = agent.spec
                 ∪ templates（同 ns）
                 ∪ selectors（同 ns）
                 ∪ aggregated agents（同 ns）
                 ∪ AgentBinding（同 ns）
                 ∪ ClusterAgentBinding（命中该 agent 的集群级绑定）
```

单值字段仍按"Agent 自身显式值优先，其次按声明顺序回退"。

## 生效方式

- Agent controller 是 `agent.status.resolved` 的唯一写者。
- ClusterAgentBinding 的贡献优先读其 `status.resolved`；status 过期时 agent controller
  直接调用纯函数 `Compute`（`internal/controller/agent/clusteragentbinding`），
  因此绑定 controller 尚未追上时不会把 agent 误判为"未绑定"。
- `status.bindingContributions` 增加 `kind` 与 `namespace`，区分两种来源。

## 与 Kubernetes 的对照

| K8s 机制 | 做法 | 本方案 |
|---|---|---|
| Gateway API ReferenceGrant | 目标方显式授权跨 ns 引用 | 资源自带 `allowedNamespaces` |
| ClusterRole / ClusterRoleBinding | 定义与绑定可集群级 | 共享资源 + `ClusterAgentBinding` |
| StorageClass / PriorityClass | 对象本身集群级 | 未采用：会丢租户所有权 |
| HNC 复制传播 | 复制对象到子 ns | 未采用：副本漂移、删除残留 |
| ownerReferences | 禁止跨 namespace | 不跨租户级联删除 |

## 边界

| 项 | 规则 |
|---|---|
| 自定义命令 | 不可共享，必须与 Agent 同 namespace（命令体运行在调用方权限下） |
| AgentTemplate / 聚合 Agent / Delegate | 仍必须同 namespace |
| 选择器 | 只在引用方或目标 namespace 内匹配 |
| 运行时部署 | 跨租户部署必须 `declclient` 的 namespace 为空；否则 informer 只缓存一个 ns |
| 删除保护 | 被引用的资源由 `agentreference` 准入插件阻止删除（待补两种 binding） |

## 管理面

- REST：`/v1/agent/clusteragentbindings`（读写），写操作仅管理员；读对所有登录用户开放。
- 控制台：「智能体 → 平台能力绑定」页，与「能力绑定」同一套描述符驱动表单。
- 引用选择器：同空间资源显示裸名；其他空间只有在其 `allowedNamespaces` 放行当前空间时
  才出现，且显示为 `空间/名称`。集群级绑定表单没有"当前空间"，因此一律用限定名。
- 资源管理列表仍按**归属**显示：`team-a` 的列表不会出现 `platform` 的资源。
  它们出现在引用选择器里，因为那是"可用"而非"归属"。

## Memory 归因

共享 Memory 写入在原空间进行，条目上的 `agent_namespace` 记录**写入方 Agent 所在的
空间**（不是 Memory 的空间）。没有它，两个空间里的同名 Agent 在条目维度无法区分。

- 自动保存（`memoryPlanFor`）从运行选中的 Agent 记录取空间；
- `save_memory` 工具从解析到的 Agent 记录取空间；
- 手工通过管理 API 写入的条目没有运行上下文，该字段为空（未知），这是诚实的默认值。

检索仍按 `(namespace, memory, subject)` 隔离；`agent_namespace` 是归因字段，不改变
召回范围。

## 尚未完成

- 列表页「显示共享给我」开关。引用选择器已经覆盖了实际使用场景，列表侧暂保持
  归属视图。
