# Configuration

Every service accepts `--config <file>`. The file is a YAML document unmarshalled into
the service options; flags override it, and selected secrets can be supplied through
`OAI_<SERVICE>_<PATH>` environment variables.

## Common blocks

```yaml
insecure:
  addr: ":8080"          # HTTP listener

coredb:
  dialect: postgres      # postgres | mysql
  addr: 127.0.0.1:5432
  username: onexai
  password: CHANGE_ME
  database: onexai

otel:
  endpoint: 127.0.0.1:4317
  level: info
  output-mode: classic    # otel | console | file | classic | hybrid
```

## Agent gateway (`oai-agent-apiserver`)

```yaml
llm:
  base-url: https://api.openai.com/v1
  api-key: CHANGE_ME
  model: gpt-4.1-mini

agent:
  mcp-enabled: true
  ragflow:
    base-url: https://ragflow.example.com
    api-key: CHANGE_ME

controlplane:
  kubeconfig: /etc/onexai/declserver.kubeconfig
  namespace: default
```

The gateway exposes an OpenAI-compatible surface. API keys and per-agent policies are
managed as declarative resources; `auth.control-token` optionally protects the admin
routes.

### OIDC and Kubernetes structured authentication

`oai-chat-apiserver` can issue an OIDC (RS256) token alongside the login
response:

```yaml
chat:
  oidc:
    enabled: true
    issuer-url: https://auth.example.com
    audiences: [onexai]
    key-file: /etc/onexai/oidc-key.pem
```

When enabled it also publishes `/.well-known/openid-configuration` and `/keys`.
The issuer must be HTTPS; for a local self-signed certificate, put that CA in
`certificateAuthority` below.

Point `oai-agent-apiserver`'s `auth.authentication-config` at an
`apiserver.config.k8s.io/v1 AuthenticationConfiguration` to add OIDC tokens to
the existing `/v1` authentication chain; the HS256 browser token keeps working.
See `configs/examples/authentication-config.example.yaml`.

### External authorization (AuthorizationConfiguration)

`oai-agent-apiserver`'s `authz.authorization-config` points at an
`apiserver.config.k8s.io/v1 AuthorizationConfiguration`. Its Webhook authorizers
are evaluated in order (the first decision other than NoOpinion wins) and
replace `authz.webhook.endpoints`; the platform's OPA/CMDB/Casdoor backends stay
in the union. See `configs/examples/authorization-config.example.yaml`.

### Authentication / Authorization Webhooks (TokenReview / SubjectAccessReview)

`oai-agent-apiserver` serves the Kubernetes review endpoints:

- `POST /apis/authentication.k8s.io/v1/tokenreviews` (authentication)
- `POST /apis/authorization.k8s.io/v1/subjectaccessreviews` (authorization)

The caller presents `Authorization: Bearer <auth.webhook-token>`; when the option is
empty the endpoints answer 503. The console's `/auth-webhooks` page exercises the same
decision path online. Authorization reviews use the same `AuthorizationRule` / OPA
policy as the run path and fail closed when no rule matches.

An Agent may also reference authentication / authorization webhooks in its Runtime
Governance step: when non-empty, those hooks replace the platform's authentication
or authorization for that Agent's invocation entries. The standard interface
(route, HTTP method, request/response) and failure semantics are in
[Custom authentication / authorization webhooks](design/custom-auth-webhook.md).

## Control plane (`oai-declserver`)

The control plane takes command-line flags only (the compose file and chart already
pass them). Authentication and authorization are the kube-apiserver switches:

```text
--authentication-config=<AuthenticationConfiguration>
--authentication-token-webhook-config-file=<kubeconfig>
--token-auth-file=<csv>
--client-ca-file=<pem>  --requestheader-client-ca-file=<pem>
--authorization-config=<AuthorizationConfiguration>
--authorization-mode=AlwaysAllow|AlwaysDeny|RBAC|Webhook
--authorization-webhook-config-file=<kubeconfig>
```

`--authorization-config` is mutually exclusive with `--authorization-mode` and
`--authorization-webhook-*`, and `--authentication-config` with `--oidc-*`;
passing both is rejected at startup.

### Native token review

The control plane serves `authentication.k8s.io/v1` itself:

- `POST /apis/authentication.k8s.io/v1/tokenreviews`
- `POST /apis/authentication.k8s.io/v1/selfsubjectreviews`

Both use the same authenticator that guards this server's own requests, so "who is
this token" has the same answer as presenting the token directly.

### Delegating access reviews to a webhook

When reviews are forwarded to the Agent (or any service implementing the two
APIs), the kubeconfig's `cluster.server` must be the **full resource URL**, not
the host root:

```yaml
clusters:
- name: agent
  cluster:
    server: https://agent.onexai.svc:9444/apis/authorization.k8s.io/v1/subjectaccessreviews
    certificateAuthority: <PEM>
users:
- name: agent
  user:
    token: <auth.webhook-token>
```

The authentication webhook is the same shape with
`/apis/authentication.k8s.io/v1/tokenreviews`. Note that client-go never sends
kubeconfig credentials to a plaintext HTTP address, so the webhook target must be
HTTPS (the Agent enables its TLS listener with `secure.enabled=true`).

### CEL admission policies

`ValidatingAdmissionPolicy` / `MutatingAdmissionPolicy` and their bindings are
enabled by default and run before the two webhook admission plugins, so a policy
that only needs the object in hand costs no network round trip.

### Service references

The control plane serves core/v1 `Service`, so `service:` references in
`APIService`, CRD conversion webhooks and admission webhooks resolve to a real
ClusterIP.

### The console's extension page

The console's *Operations → Extension center → Extension objects* page
(`/extensions`) manages exactly these objects: admission webhook
configurations, CEL admission policies and their bindings, and `APIService`. It
reaches the control plane through the Agent's `/v1/admin/extensions` proxy
rather than directly, so there is still a single access path. The page edits the
JSON the API server stores, which means a field added upstream is not silently
dropped.

## ChatOps (`oai-chat-apiserver`)

```yaml
lark:
  app-id: CHANGE_ME
  app-secret: CHANGE_ME
  verification-token: CHANGE_ME
  event-mode: websocket

agent:
  endpoint: http://127.0.0.1:8080

websocket:
  allowed-origins: []    # browser origins allowed to open the socket; empty keeps allow-all
  max-concurrent-frames: 64

duty:
  provider: regen        # none | regen; empty infers regen from backend.regen-endpoint

backend:
  regen-endpoint: https://regen.example.com
  regen-username: bot@example.com
  regen-password: CHANGE_ME
  regen-user-map: "bot@example.com=ou_xxx"
```

`oai-chat-apiserver` refuses to start without `controlplane.kubeconfig`: command grants
and peer bindings come from the control plane, and a second SQL reader would be a second
source of truth. Duty groups and schedules are the exception — they are chat-owned
PostgreSQL tables and do not need the control plane.

A WebSocket handshake is not protected by the browser's same-origin policy. Set
`websocket.allowed-origins` to the console's origin when the two are served separately;
an empty list preserves the historical allow-all behaviour, and requests without an
`Origin` header (CLI clients) are always accepted.

Optional adapters (all default off): `backend.zadig-*`, `backend.cmdb-*`,
`backend.prometheus-endpoint`, `backend.dns-whitelist-*`.

## Jobs, MQ, and MCP server

```yaml
coredb: { ... }

kafka:
  brokers: ["127.0.0.1:9092"]
  group-id: onexai

oss:
  endpoint: http://127.0.0.1:9000
  access-key: CHANGE_ME
  secret-key: CHANGE_ME
  bucket: onexai
```

## Secrets

Do not commit real secrets. Prefer environment variables or your platform's secret
manager. The Helm chart renders the service configs into Kubernetes Secrets; values in
`values.yaml` are intentionally empty placeholders.
