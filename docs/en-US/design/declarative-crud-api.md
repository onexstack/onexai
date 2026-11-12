# Declarative Resource REST API

`oai-agent-apiserver` exposes the declarative resources served by `oai-declserver` under `/v1/<group>/<plural>`. The group segment matches the declserver API group (`agent`, `chat`, `core`), and the resource segment is declserver's plural name.

## Routes

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
| agent | CustomCommand | `/v1/agent/customcommands` |
| agent | Webhook | `/v1/agent/webhooks` |
| agent | AuthorizationRule | `/v1/agent/authorizationrules` |
| agent | RuntimeProfile | `/v1/agent/runtimeprofiles` |
| agent | RuntimeHosting | `/v1/agent/runtimehostings` |
| agent | SandboxTemplate | `/v1/agent/sandboxtemplates` |
| agent | ScheduledRun | `/v1/agent/scheduledruns` |
| core | Secret | `/v1/core/secrets` |
| chat | Command (read-only) | `/v1/chat/commands` |

## Where business lines and credentials live

A business line (Namespace) is not a declserver CRD: it is a `corev1.Namespace` whose display name,
description, owner and update time live in the `onexai.io/display-name`, `onexai.io/description`,
`onexai.io/owner` and `onexai.io/updated-at` annotations; labels are native Namespace labels.
The `agent.namespace` row table and the reverse syncer are gone. `GET /v1/agent/namespaces` reads the
control plane directly (Get goes to the API server, List reads the informer).

Secrets live in core Secrets labelled `onexai.io/secret-kind=aksk`; `/v1/core/secrets` keeps its
request/response shape. The SecretID is the API identity and is mapped deterministically to a Secret
name. List returns only objects carrying that label, so Model/MCP/Agent credentials are never exposed.

## Deprecated declarative objects

These objects are still served by declserver so historical data can be read or migrated, but no
runtime reads or writes them; do not build new dependencies on them:

| Object | Notes |
|---|---|
| `chat.onexai.io/v1alpha1 DutyGroup` | Duty groups are a chat-owned SQL resource (`chat.oncall_group` and its schedule tables) |
| `chat.onexai.io/v1alpha1 BotApp` | chat reads its Lark application from service config, not this object |
| `agent.onexai.io/v1alpha1 ScheduledRun` | Scheduling moved to `batch.onex.ai CronJob`; the `/v1/agent/scheduledruns` facade remains supported |

Requests under `/apis/...` for these resources carry a standard `Warning: 299 - "..."` header. The
`/v1/agent/scheduledruns` facade is supported and does not carry that warning.

## Scheduling and batch jobs

Scheduling now runs on the standard `batch.onex.io/v1beta1` Job and CronJob
resources, served by `oai-declserver` and reconciled by `oai-job-controller`.
A scheduled agent run is a CronJob whose `jobTemplate.spec.type` is `agentrun`;
each occurrence is a child Job.

`/v1/agent/scheduledruns` remains as a compatibility facade: it creates and
reads the same CronJob, its `/executions` route lists the child Jobs, and
`/trigger` creates a manual child Job. New clients should use the CronJob
resource directly.

The `ScheduledRun` object is no longer reconciled by `oai-agent-apiserver`. Run
`oai-scheduledrun-migrator` once to copy existing objects to CronJobs; after the
migration the facade only reads and writes the batch resources.

Every writable resource has five routes:

```text
POST   /v1/<group>/<plural>
GET    /v1/<group>/<plural>            ?offset=&limit=
GET    /v1/<group>/<plural>/:name
PUT    /v1/<group>/<plural>/:name
DELETE /v1/<group>/<plural>/:name
```

Request and response bodies match the existing `agents`/`skills` endpoints: create and update wrap the object as `{"<kind>": {...}}`, and list returns `{"<plural>": [...], "count": n}`.

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

`status` is controller-owned. A request may carry it, but create/update does not change it; the declserver strategy preserves or clears it.

## Agent prompt bindings

An Agent carries its persona as an **ordered list of Prompt names** (`spec.prompts` in the CRD, `prompts` on the management message). The controller merges `promptRef ∪ prompts ∪ promptSelector` into `aggregated-<agent>`, and the runtime prefers that object; while it is being created, the runtime falls back to the list in order and uses the first Prompt that exists, is enabled and has content. The resolution rules are in [agent aggregation](agent-aggregation.md).

`prompts` is the only prompt spelling on the management message: the single-value `sysPrompt` field was removed, as was the pre-rename `workspace` field on every resource message. Their proto field numbers are reserved and must not be reused. Free-form text is not accepted — put it in a Prompt object and bind it.

## Unified authentication

The resource surface is mounted once behind a middleware that accepts either credential:

1. **AK/SK signature** — service-to-service calls, the same contract as the signed `/v1` face.
2. **Bearer JWT** — browser console calls, which must carry the administrator claim.

Selection is by credential presence, never by fallback: any AK/SK header pins the signed path, and a failed signature is not rescued by a valid JWT. `X-Onex-Control-Token` applies only to the AK/SK path for `/v1/agent/authorizationrules` and `/v1/core/secrets`; a JWT administrator does not need the second credential, matching the existing `/v1/admin` contract.

## Namespace

- Create: `metadata.namespace` > `?namespace=` > `controlplane.namespace` > `helper-public`.
- Get/Update/Delete: `?namespace=` > configured scope > cross-namespace lookup > the default namespace.
- List: `?namespace=` > configured scope > all namespaces.

## Errors

Errors use the `core.WriteResponse` envelope:

```json
{"reason":"NotFound","message":"...","metadata":{}}
```

Kubernetes errors map as follows: `NotFound` → 404, `AlreadyExists`/`Conflict` → 409, `Invalid`/`BadRequest` → 400, `Unauthorized` → 401, `Forbidden` → 403, everything else → 500.

## Command migration

`Command` moved from `agent.onexai.io` to `chat.onexai.io`:

- Command bodies are compiled into the chat binary, and `internal/chat/commandreg` is the only writer; it reconciles at start-up and every ten minutes.
- The endpoint therefore lives in `oai-chat-apiserver` and is **read-only** (`GET /v1/chat/commands`, `GET /v1/chat/commands/:name`).
- A write to `Command` would be undone by the next reconcile; use `CustomCommand` for user-defined commands.
- Objects do not move automatically. The reconciler recreates them in the new group; the old objects need a one-time cleanup.

## Breaking changes

The former `/v1/agents`, `/v1/skills`, `/v1/admin/<resource>` paths are now `/v1/agent/<plural>`. Clients and the console must be updated with the service. Transition aliases can be added in a later release if needed.
