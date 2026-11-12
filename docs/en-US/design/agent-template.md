# Agent Templates

## Goal

An agent template is a named, reusable behavior unit. It carries the prompt,
model, capability references and run contract an agent would otherwise repeat,
so a team can define its behavior once and have many agents opt into it.

A template is not an agent: it has no identity, no runtime and no authorization.
Those stay on the Agent, which keeps a template portable across runtimes and
security boundaries.

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
    name: gpt-4o-mini   # this agent's own value wins
```

## Semantics

```text
effective(agent) = agent.spec
                 ∪ template[0].spec
                 ∪ template[1].spec
                 ∪ selector matches
                 ∪ aggregated agents
                 ∪ AgentBindings
```

- **Sets are unions.** Prompts, skills, MCPs, knowledge bases, custom commands,
  webhooks, approval patterns and delegates from every template join the
  agent's own set.
- **Scalars follow declaration order.** Model, output schema, memory and
  session tools fall back to the first template that declares them; an explicit
  value on the Agent always wins.
- **Templates are same-namespace.** A cross-namespace template is rejected at
  admission.
- **Templates do not nest.** A template cannot reference another template.
  Composition happens at the consumer: an agent may name several templates in
  order.
- **Runtime and authorization never travel.** `runtime.profileRef`,
  `authzBackends` and identity are Agent-only fields.

## How it takes effect

1. Write the `AgentTemplate` and an `Agent` that names it in
   `spec.templateRefs`.
2. The agent controller resolves the template, merges its behavior and writes
   the result into `status.resolved` (including `status.resolved.templates`).
3. The controller watches `AgentTemplate`; editing one re-evaluates every agent
   in the namespace, so a change reaches all referencing agents without an
   agent edit.
4. The runtime reads `status.resolved`, exactly as it does for selectors,
   aggregated agents and bindings.

A missing or disabled template contributes nothing and reports
`TemplatesApplied=False` plus `RefsResolved=False`, so a half-resolved agent is
visible rather than silently degraded.

Deleting a template that an agent still references is blocked by the
`agentreference` admission plugin; the error names the blocking agents.

## Boundaries

| Boundary | Value |
|---|---|
| Template references per agent | 16 |
| Nesting | Not allowed |
| Namespace | Same as the referencing agent |
| Runtime selection | Agent only |
| Authorization | Agent only |
| Propagation | Controller watch, no agent edit required |

## Relationship to other kinds

- `Prompt` holds prompt text; a template references Prompts, it does not
  inline them.
- `RuntimeProfile` selects where an agent runs; a template never does.
- `AgentBinding` adds capabilities to a set of agents; a template is a behavior
  unit an agent opts into. Both are additive and can be used together.
- Agent aggregation (`spec.agents`) unions capabilities from other agents; a
  template is not an agent and cannot aggregate.

## Management surface

The Agent REST message exposes the same `spec.templateRefs` as `templateRefs`,
an ordered name list:

- omitting the field means "no opinion" and leaves a kubectl-written list alone;
- an explicit `[]` clears the list.

The console renders a template's JSON `spec` as a structured form (prompts,
model, capability references, run contract and advanced selectors). The
top-level status switch is written back as `spec.enabled`, which is what the
control plane reads when the spec is non-empty. Spec fields the form does not
expose (such as the legacy `promptRef`) survive an edit unchanged.

## Permissions

Managing templates uses the same `agenttemplates` resource permissions as the
other declarative kinds. The REST face is mounted at `/v1/agent/templates`.
