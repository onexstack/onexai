# AgentBinding: batch-binding capabilities by label selector

## Goal

Apply the same set of platform capabilities (prompt / skill / knowledge base / MCP / custom command / platform command) to a group of Agents without editing each Agent's spec individually.

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
  commands: [bind, oncall]   # named outright
  commandSelector:           # optional; grants a whole catalog group, unioned with commands
    matchExpressions:
      - {key: agent.onexstack.io/group, operator: In, values: [duty]}
```

## Semantics: pure union, zero conflict

```
effective(agent) = agent.spec 自有 refs
                 ∪ agent.spec 各 XxxSelector 命中
                 ∪ 所有 enabled 且 (selector 命中 agent.Labels | spec.agents 含 agent)
                   的 AgentBinding 贡献
```

Modeled on Kubernetes' RBAC / NetworkPolicy / ClusterRole `aggregationRule`:

- Bindings are additive only: **no deny, no priority, no override**;
- Duplicate references are deduplicated by `(kind, namespace/name)` or command name, and the result is sorted;
- So "conflict" does not exist in this model — two bindings referencing the same object simply yield the same object.

"Revoking" is done by no longer selecting (changing labels, deleting the binding), not by writing a negative rule.

## How it takes effect

- **Merged only in the controller**: the Agent controller writes contributions into `agent.status.resolved` and records `status.bindingContributions` (source, generation, count; at most 64 entries, beyond which it is marked truncated) and `resolvedHash`;
- Runtime reads status; a binding change → hash change → the `runnercache` fingerprint becomes stale and the graph is rebuilt;
- Paused runs depend on a stable graph address, so `runnercache.Key` remains only the set of agent names; the fingerprint only determines freshness, and pinned entries survive fingerprint changes.

The result is not written back to `agent.spec`: that is the reason PodPreset was removed.

## Command catalog

Command bodies are compiled into the chat binary (`internal/chat/pkg/command`), and `Command` (`chat.onexai.io/v1alpha1`) is its catalog entry:

- chat reconciles once at startup, then every 10 minutes;
- It only cleans up objects carrying `onexai.io/managed-by=oai-chat-apiserver` that are no longer in the catalog;
- AgentBinding resolution validates against `Command`: missing or disabled → `status.missing`, `RefsResolved=False`; `skill:<name>` is a runtime skill command and is passed through as-is;
- Therefore "binding a command that has been taken offline" is explicitly visible rather than silently unauthorized.

`spec.commandSelector` is the batch axis: every `Command` carries
`agent.onexstack.io/group=<group>`, so `group in (duty)` grants the whole duty
group — **including commands added to that group later**. In detail:

- it is unioned with `spec.commands`: named commands are pins, the label follows the catalog;
- the chat runtime expands it against the compiled catalog's labels when rules are rebuilt; the controller expands it against the `Command` objects in the target namespaces and records the result in `status.resolved.commands`; both must agree;
- **an empty selector `{}` does not mean "all commands"**: `metav1.LabelSelectorAsSelector({})` yields `Everything()`, so the write path rejects an empty selector. A selector that matches nothing grants nothing and is not reported in `status.missing`.

## Boundaries and capacity

| Item | Rule |
|---|---|
| disabled agent | Does not participate in matching; the same rule applies to both binding-side and agent-side counting |
| disabled resource | Treated as nonexistent, goes into `status.missing` |
| cross-namespace reference | v1 rejects it: the runtime lister is namespace-scoped |
| contributions | Only counts and sources are stored, at most 64 entries; the capability union is not truncated |
| empty selector `{}` | Selects all enabled agents in this namespace, and a condition marks this |

## Permissions

- When creating/updating a binding, the caller must have `get` on the referenced objects and `list` on agents (the selector is the gate for batch authorization); failure returns 403 and produces an Event;
  the selector's TOCTOU is a known trade-off: newly matching agents later are not validated one by one.
- In RBAC mode, chat's identity needs `get/list/watch/create/update/delete` on `commands` (catalog reconciliation writes these objects).

## Relationship with CommandGrant

`CommandGrant` has been removed. Command granting now goes through the bindings: named commands in `spec.commands`, whole catalog groups in `spec.commandSelector` (the two are unioned). Historical objects are converted with `hack/commandgrant2binding`, which expands a legacy selector into names against the catalog labels at conversion time — that is the migration tool's choice, not an API limitation; a new object can write `commandSelector` so the grant follows the catalog. An empty expansion stays empty, faithfully preserving the original effect.
