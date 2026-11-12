# Custom authentication / authorization webhooks

This document defines the contract between the platform and an external
authentication or authorization service. A service that implements the two HTTP
endpoints below can be created in the console (Extension Center → Webhook) and
referenced from an Agent's Runtime Governance step.

The platform itself implements both protocols
(`/apis/authentication.k8s.io/v1/tokenreviews` and
`/apis/authorization.k8s.io/v1/subjectaccessreviews`), so one onexai deployment
can be configured as another's backend.

## Resource model

The backends reuse the existing `Webhook` resource, discriminated by
`spec.type`:

| `spec.type` | Purpose | Protocol |
|---|---|---|
| empty / `run` | run-lifecycle hook | platform event contract |
| `authentication` | authentication backend | Kubernetes `TokenReview` |
| `authorization` | authorization backend | Kubernetes `SubjectAccessReview` |

```yaml
apiVersion: agent.onexai.io/v1alpha1
kind: Webhook
metadata:
  name: corporate-sso
  namespace: default
spec:
  type: authentication
  enabled: true
  # Full URL. The platform POSTs here; it does not append a path.
  clientConfig:
    url: https://sso.internal/apis/authentication.k8s.io/v1/tokenreviews
    caBundle: <PEM>
    headers:
      X-Tenant: acme
  secretRef:
    name: sso-token          # sent as Authorization: Bearer
  timeoutSeconds: 5
  failureMode: fail
  audiences: [onexai]        # authentication only
```

Constraints:

- `events` / `template` belong to run hooks only; they must be empty here.
- `audiences` belongs to authentication hooks only.
- `failureMode`: `ignore` (no opinion) or `fail` (deny). Authentication is always
  fail-closed; `failureMode` does not change it.
- `secretRef` is a reference; the platform resolves it before every call.

## Authentication webhook — TokenReview

`POST <spec.clientConfig.url>` with `Content-Type: application/json`.

Request:

```json
{
  "apiVersion": "authentication.k8s.io/v1",
  "kind": "TokenReview",
  "spec": {
    "token": "<the caller's bearer token>",
    "audiences": ["onexai"]
  }
}
```

Response (any 2xx):

```json
{
  "apiVersion": "authentication.k8s.io/v1",
  "kind": "TokenReview",
  "status": {
    "authenticated": true,
    "user": { "username": "alice", "uid": "10001", "groups": ["oncall"] },
    "audiences": ["onexai"],
    "error": ""
  }
}
```

- `authenticated=true` with a non-empty `user.username` → authenticated. The
  identity becomes the caller for this request and is what an authorization
  webhook receives in `spec.user` / `spec.groups`.
- `authenticated=false`, a non-2xx answer, a timeout or an unparseable body →
  this hook fails. The platform tries the next authentication hook; if none
  succeeds the request is `401`. It never falls back to the platform JWT/OIDC
  chain.

## Authorization webhook — SubjectAccessReview

`POST <spec.clientConfig.url>` with an `authorization.k8s.io/v1
SubjectAccessReview` body:

```json
{
  "apiVersion": "authorization.k8s.io/v1",
  "kind": "SubjectAccessReview",
  "spec": {
    "user": "alice",
    "groups": ["oncall"],
    "extra": {
      "onexai.io/subject-kind": ["user"],
      "onexai.io/agent-name": ["my-agent"],
      "onexai.io/tool-name": ["knowledge_search"],
      "onexai.io/mcp-name": ["kb-mcp"]
    },
    "resourceAttributes": {
      "namespace": "default", "verb": "get",
      "group": "onexai.io", "version": "v1",
      "resource": "agents", "name": "my-agent"
    }
  }
}
```

Response:

```json
{
  "apiVersion": "authorization.k8s.io/v1",
  "kind": "SubjectAccessReview",
  "status": { "allowed": false, "denied": true, "reason": "not in the oncall group" }
}
```

- `allowed=true` → allow.
- `denied=true` → deny (deny wins over allow).
- neither → no opinion.
- `evaluationError` non-empty → a failed call, handled by `failureMode`.

Agent-level authorization is fail-closed: if every referenced webhook returns no
opinion, the request is denied.

## Agent configuration

The Runtime Governance step exposes:

| Field | Meaning |
|---|---|
| `spec.webhooks` | run-lifecycle hooks |
| `spec.authnWebhooks` | authentication webhook names; non-empty **replaces** platform authentication |
| `spec.authzWebhooks` | authorization webhook names; non-empty **replaces** platform authorization (including `authzBackends`) |
| `spec.authzBackends` | platform authorization backend subset (OPA / CMDB / Casdoor / …) |

- Authentication: a Bearer credential is decided only by the Agent's TokenReview
  hooks; the platform JWT/OIDC chain is not consulted. AK/SK stays on the
  platform chain (dual channel). All hooks refusing → `401`.
- Authorization: the Agent's SubjectAccessReview hooks are the only authority for
  agent-level and tool-level decisions.
- An empty list means the platform default; existing agents are unchanged.
- Only Agent invocation entries (`/v1/agent/ask`, `/v1/chat/a2a`,
  `/v1/agent/agents/:name/invoke`) are affected. Console CRUD still uses the
  platform's authentication and authorization.

## Online test

The console's Webhook list and detail pages expose a "Test" action for the two
access-review types. It calls that one webhook (not the platform chain) with a
TokenReview or SubjectAccessReview and shows the HTTP status, latency, parsed
`status` and raw body. Platform-side call errors are shown separately.

## Failure semantics

| Scenario | Authentication | Authorization |
|---|---|---|
| Webhook refuses | try next; all refuse → 401 | deny |
| unreachable / timeout / non-2xx / unparseable | try next; all fail → 401 | `fail` → deny; `ignore` → no opinion |
| every webhook has no opinion | — | deny (fail-closed) |
| referenced webhook missing / disabled / wrong type | 401 | deny |
| Agent names none | platform chain | platform authorization |
