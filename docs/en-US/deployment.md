# Deployment

## Helm

`manifests/installation/charts/onexai` is a single chart for the control plane and the runtime
services.

```bash
helm upgrade --install onexai manifests/installation/charts/onexai \
  -n onexai-system --create-namespace \
  -f values-secrets.yaml
```

What the chart creates:

- `oai-declserver` (StatefulSet/Deployment) with its serving certificate and tokens;
- `oai-controller-manager` with the control-plane kubeconfig;
- `oai-agent-apiserver`, `oai-chat-apiserver`, and optional `oai-jobserver`,
  `oai-mqserver`, `oai-mcpserver`;
- one Secret per service, rendered from `files/<service>.yaml`;
- RBAC and Services.

## Docker Compose

For a single-machine deployment, `manifests/installation/compose/` starts the control plane, both
API servers, the job workers, an A2A runtime, PostgreSQL, Redis, and etcd with one
command. Kafka and `oai-mqserver` are not included. See
[Docker Compose](docker-compose.md).

The default profile is a development topology (bypass-aksk, authorization off,
anonymous control plane, loopback-only ports). The `secure` profile seeds the
caller secrets and authorization rules. The README in `manifests/installation/compose/` lists the
production hardening steps.

## External dependencies

- **etcd**: either a managed service or a dedicated StatefulSet. Use a distinct
  `--etcd-prefix` per deployment; two deployments must never share a prefix.
- **PostgreSQL**: one database, three schemas (`agent`, `chat`, `job`). AutoMigrate
  creates the tables but not the schemas, so a fresh database needs them
  pre-created (the compose stack does this in
  `manifests/installation/compose/initdb/00-schemas.sql`). `coredb.dialect: mysql` is supported but
  the PostgreSQL path is the primary one.
- **Kafka** only for `oai-mqserver`, which currently registers no topic subscribers;
  it is not part of the compose stack. **Redis** is optional (the compose stack
  includes it for agent sessions and nonces).
- **LLM endpoint**: any OpenAI-compatible endpoint, including a local vLLM. A
  non-empty `llm.base-url` is required even when Agents name Model objects.
- **Lark/Feishu app**: subscribe to message and card events, and point the callback at
  `oai-chat-apiserver`.

## Scaling notes

- `oai-chat-apiserver` keeps WebSocket hubs, Lark connections, and de-duplication state
  in-process; run **one replica** unless session state is externalized.
- `oai-agent-apiserver` is stateless apart from Redis/informer caches and scales
  horizontally.
- `oai-controller-manager` uses leader election when more than one replica runs.

## Images

The Dockerfiles under `build/docker/` build every binary and a distroless runtime image.
The compose stack uses `manifests/installation/compose/Dockerfile`, which builds every service from
one parameterized recipe (`--build-arg SERVICE=<cmd name>`) and selects a Debian
runtime because its healthchecks use `curl`. Override the registry with
`REGISTRY`/`REGISTRY_PREFIX`; no private registry is required.

```bash
make image REGISTRY_PREFIX=ghcr.io/onexstack
```
