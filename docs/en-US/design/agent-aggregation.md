# Agent capability aggregation (spec.agents / spec.agentSelector)

## Goal

An `Agent` can declare capabilities individually via `spec.skills` / `spec.mcps` / `spec.knowledgeBases` / `spec.prompts`. Aggregation lets one Agent directly reuse another Agent's entire capability set:

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

The semantics are modeled on Kubernetes' aggregated ClusterRole: **the caller only needs to be authorized on the aggregating Agent**, not bound to each base Agent separately; base Agents are "building blocks", not "selected Agents".

## Fields

- `spec.agents []Reference`: aggregate by name, within the same namespace (`namespace` must be empty, otherwise admission rejects it).
- `spec.agentSelector *metav1.LabelSelector`: aggregate by label, matching only within the namespace of this Agent; an explicitly empty selector `{}` matches all Agents in the namespace except itself.

The two are unioned. Declaration order determines prompt merge order; selector matches are sorted by name.

## Resolution (controller)

`internal/controller/agent/agent/agents.go` expands `spec.agents ∪ selector` into a transitive closure:

- **Transitive**: A→B→C, A gets all capabilities of B and C;
- **visited + onPath + cycle detection**: diamonds are counted once, cycles are judged structural errors;
- **Limits**: at most 16 sources (consistent with validation's explicit reference limit), at most 8 levels deep;
- **Missing/disabled sources**: reported as that source being unavailable, contributing no capabilities; never silently skipped;
- **Structural failures fail closed**: on cycles, excessive depth, too many sources, or too many merged prompt sources, **the inherited result is discarded entirely**, keeping only the Agent's own declared capabilities, and `Ready=False`.

The reason for failing closed: what is inherited is not just skill/mcp/kb, but also **webhooks (admission hooks) and requireApproval (human approval mode)**. Publishing a "partially inherited" capability set would silently weaken these controls, so it is better not to inherit at all.

### Status

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

When `AgentAggregated` is `False`, `Ready` is also `False` — unlike `PromptAggregated`: prompt derivation has a "last good copy", capability inheritance does not.

## How prompts are aggregated

Reuse the new Prompt mechanism rather than inventing another:

1. The controller first merges the **source Agents' prompt sources** (the resolution result of `promptRef ∪ prompts ∪ promptSelector`) into this Agent's `promptSet`;
2. Then hands it to `reconcileAggregatedPrompt`: a single source is copied as-is, multiple sources go through LLM merging, writing out the `aggregated-<agent>` Prompt;
3. The input hash covers all sources (including source Agents' prompts), so a change in a source prompt triggers recomputation;
4. A source Agent's own `aggregated-<source>` is not treated as a source (avoiding nested aggregation); its own `inline-<source>` participates in merging as a source.

A source Agent's `promptAggregation.regenerateToken` is not involved; only this Agent's token takes effect.

### Runtime resolution

The runtime reads `status.aggregatedPrompt` first: when the controller has produced the derived Prompt, its content is the persona. While that object is being (re)created, or when the controller is unavailable, the runtime walks the binding list in order — `promptRef` first, then `spec.prompts` as listed — and uses the first Prompt that resolves (exists, is enabled, and has content). A missing entry is skipped rather than ending the walk.

`promptSelector` has no runtime fallback: only the controller can list Prompts, so selector matches reach the runtime through the aggregated Prompt alone. If no binding resolves, the engine's default instruction applies.

## Fields not inherited

| Field | Reason |
|---|---|
| `modelRef` | Aggregation contributes capabilities, not a change of runtime endpoint |
| `authzBackends` | Authorization backends are looked up by the requested Agent's name; the source's choice does not take effect |
| `maxSkills` | Budget belongs to this Agent itself; aggregation results count toward this Agent's budget |

## Runtime

`agentFromCR` reads `status.resolved` into the `Resolved*` fields of `AgentM` (`gorm:"-"`, not persisted), and runtime obtains the "effective set" through accessors:

```go
a.EffectiveSkills()          // 优先 ResolvedSkills，否则 spec 声明
a.EffectiveMCPs()
a.EffectiveKnowledgeBases()
a.EffectiveWebhooks()
a.EffectiveRequireApproval()
a.EffectiveCustomCommands()
```

The accessors trust `Resolved*` only when the controller has already observed the object (`AgentM.Observed`, from `status.observedGeneration > 0`); otherwise they fall back to the spec declaration. This way "the controller's resolution really is empty" (e.g. a binding was revoked) is not mistaken for "not yet reconciled" and does not resurrect the declared set.

Switched consumers: the run spec union (`internal/agent/biz/v1/agent/spec.go`), approval mode (`resolve.go`), resource authorization (`gate.go`), `/v1/agent/agents/:name/context-skills` (`internal/agent/di/providers_skill.go`), and custom commands (`customcommand/biz.go`).

The management API (`/v1/agent/agents`) still shows **the spec declaration view**: `AgentToCR` does not write `Spec.Agents` / `Spec.AgentSelector`, so an admin update never expands the aggregation configuration into static references. `Webhooks` is now expressible in the admin request body and follows the same nil/empty contract as the other reference lists: nil means "no opinion" (a hook declared with kubectl survives an unrelated console update), while an explicit empty list clears them. Hooks inherited from aggregation live only in `status.resolved.webhooks`, never in `spec.webhooks`, so this write path cannot touch them.

## Authorization attribution

`authorizeRun` uses the effective set to check whether the caller can reach these skill/mcp/knowledgebase through the **requested Agent**, and matching still follows that Agent's name. This is consistent with aggregated ClusterRole: binding the aggregate role grants the rules aggregated into it, without binding the base roles.

## Eventual consistency

- Editing a source Agent's spec or labels triggers recalculation of all Agents in the same namespace via `Watches(&Agent{}, ...)` (the predicate only lets through generation/label/deletion events; status writes do not self-trigger);
- There is a brief window between a source change and the consumer's status refresh; during that window runtime reads the previous `status.resolved`. This is inherent latency of derived data, not a caching bug.

## Acceptance record (2026-10-27, local declserver + controller-manager)

Temporary fixtures were created with `kubectl --kubeconfig ~/.onexai/declserver.kubeconfig` (already cleaned up), confirming:

- Name references and `agentSelector` union and deduplicate; `status.resolved` contains skills/mcps/knowledgeBases/webhooks/requireApproval/customCommands all from the source Agent's declarations;
- The source Agent's prompt enters `aggregated-<root>`; both multi-source merge paths were actually tested:
  when credentials are invalid it goes through deterministic `fallbackConcat` (`PromptAggregated=False/AggregationFailed`, `aggregatedPromptStale=true`, content still retains all sources); after rotating the `deepseek-key` credential and switching `deepseek-secured.spec.model` to `deepseek-v4-flash`,
  `PromptAggregated=True/Aggregated` (message `the bound prompts were merged`), the output is a single LLM-merged prompt (both parties' requirements retained), and `aggregatedPromptStale`/`Overridden` are both false;
- Changes to the source Agent's spec (adding a skill) and labels (removing from the selector) both trigger consumer refresh (observable with `kubectl wait --for=jsonpath=...`);
- Cycle → `AgentAggregated=False/CycleDetected`, `Ready=False`, keeping only its own capabilities;
- Missing source → `SourceUnavailable` + `RefsResolved=False`, but existing aggregate content is retained;
- The runtime read path (informer → `agentFromCR` → `Effective*()`) was verified against a real declserver: `EffectiveSkills` is declaration ∪ inheritance, while `row.Skills` is still the declaration view;
- After deleting an Agent, `aggregated-<agent>` is automatically deleted by ownerReference GC.

## Known limitations

- Cross-namespace skill/mcp/knowledgebase declared by a source Agent still appears as `ns/name` in status, but at runtime the store addresses by name and cannot distinguish objects with the same name (existing behavior, identical to declaring them directly).
- Exceeding `maxSkills` currently only reports `SkillsWithinBudget=False` and does not truncate the capability set (existing behavior).
