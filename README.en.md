# onexai

[中文](README.zh-CN.md) | English

onexai is an open-source **LLM gateway and ChatOps platform** for teams that want to run
agents, tools, and on-call workflows on their own infrastructure.

It combines two products in one Go module:

- an **OpenAI-compatible agent gateway** — chat completions, agents, MCP tools, skills,
  knowledge bases, prompts, and model routing;
- a **ChatOps runtime** — a Lark/Feishu bot with sessions, tickets, duty/on-call,
  human-in-the-loop approvals, and asynchronous jobs.

The platform is built on a Kubernetes-style declarative control plane
(`oai-declserver` + CRDs + `oai-controller-manager`), so every agent, prompt, skill,
and MCP server is a versioned object rather than a row edited by hand. Duty groups are
the exception: they are a chat-owned imperative resource stored in PostgreSQL.

> The private `harness` CLI is **not** part of this repository. Everything needed to
> build, run, and extend the server platform lives here.

## Components

| Binary | Role |
|---|---|
| `oai-agent-apiserver` | OpenAI-compatible LLM gateway: `/v1/chat/completions`, agents, MCP, skills, knowledge bases, prompts, models |
| `oai-agent-runtime` | A2A agent runtime and sandbox execution |
| `oai-chat-apiserver` | Lark/Feishu bot and ChatOps: chat, auth, sessions/messages, duty/tickets, commands |
| `oai-declserver` | Kubernetes-style aggregated API server for the declarative control plane (etcd-backed) |
| `oai-controller-manager` | Controllers for agents, bindings, bot apps, peer bindings, and namespace resources |
| `oai-jobserver` | Asynchronous job and cronjob consumer |
| `oai-job-controller` | Reconciles `batch.onex.io` Jobs and CronJobs; runs the agentrun provider |
| `oai-scheduledrun-migrator` | One-shot migration of ScheduledRun objects to batch CronJobs |
| `oai-mqserver` | Kafka consumer |
| `oai-mcpserver` | MCP tool server (stdio, SSE, streamable HTTP) |

## Architecture

```text
                         ┌──────────────────────────────┐
   OpenAI clients ──────▶│ oai-agent-apiserver          │
   Web chat / IM ───────▶│ oai-chat-apiserver           │
                         └──────────────┬───────────────┘
                                        │ reads/writes
                         ┌──────────────▼───────────────┐
                         │ oai-declserver (kube-style)  │
                         │   CRDs + etcd                │
                         └──────────────▲───────────────┘
                                        │ reconciles
                         ┌──────────────┴───────────────┐
                         │ oai-controller-manager       │
                         └──────────────────────────────┘

   oai-jobserver (jobs)   oai-mqserver (Kafka)   oai-mcpserver (tools)
   oai-agent-runtime (A2A / sandbox)
```

- **Control plane first.** The runtime services read agents, prompts, skills, MCP
  servers, command grants, and peer bindings from the control plane. The control plane
  must be reachable before they start serving.
- **One resource, one source.** Reads go through the declarative client; SQL is used for
  sessions, messages, tickets, job state, and the chat-owned duty groups/schedules.
- **Everything optional is opt-in.** Duty providers, CMDB, Zadig, RAGFlow, and usage
  reporting are disabled unless configured.

## Features

- **OpenAI-compatible API** with streaming, tool calls, and multi-provider model routing.
- **Agents and skills** defined as declarative resources, with prompt aggregation and
  versioned references.
- **MCP** both as a client (tools discovered from MCP servers) and as a server
  (`oai-mcpserver`).
- **Knowledge bases** with pluggable retrieval backends (RAGFlow, vector stores).
- **ChatOps** for Lark/Feishu: slash commands, cards, tickets, duty/on-call, approvals,
  and one-message-one-turn session semantics.
- **Duty as an interface.** `pkg/duty` defines the roster and incident contract;
  `pkg/duty/regen` is the reference implementation. A deployment without a provider
  keeps tickets local and reports duty as unavailable.
- **Kubernetes-native operations**: CRDs, informer caches, server-side apply, and a
  controller-manager.

## Quick start

### Docker Compose (one command)

```bash
cd manifests/installation/compose
cp env.example env      # set LLM_BASE_URL and LLM_API_KEY
docker compose --env-file env up -d --build
./smoke.sh
```

This starts the control plane, both API servers, the job workers, an A2A
runtime, PostgreSQL, Redis, and etcd. Kafka and `oai-mqserver` are not part of
the stack. See [Docker Compose](docs/en-US/docker-compose.md) for ports,
configuration overrides, and the secure profile.

### From source

#### Prerequisites

- Go **1.27+**
- PostgreSQL 16+ (recommended) or MySQL 8+ (set `coredb.dialect`)
- etcd 3.5+ for `oai-declserver`
- Optional: Kafka, Redis, an LLM endpoint, Lark/Feishu credentials

#### Build

```bash
make build          # builds every cmd/ binary into _output/platforms/<os>/<arch>/
make test           # unit tests (uses -race; Linux)
CGO_ENABLED=0 go test -short ./...  # macOS/arm64
make lint.defensive # the merge-gating lint profile
```

#### Run locally

1. Start etcd and PostgreSQL.
2. Start the control plane:

   ```bash
   _output/platforms/$(go env GOOS)/$(go env GOARCH)/oai-declserver \
     --etcd-servers=http://127.0.0.1:2379 \
     --secure-port=6444 \
     --cert-dir=$HOME/.onexai/controlplane/certs
   ```

3. Apply the seed RBAC bindings (optional, for SRE/admin lists):

   ```bash
   kubectl --kubeconfig ~/.onexai/declserver.kubeconfig apply -f hack/rbac-bindings-seed.yaml
   ```

4. Start the runtime services with example configs:

   ```bash
   cp configs/examples/oai-agent-apiserver.example.yaml /tmp/oai-agent-apiserver.yaml
   cp configs/examples/oai-chat-apiserver.example.yaml  /tmp/oai-chat-apiserver.yaml
   _output/platforms/$(go env GOOS)/$(go env GOARCH)/oai-agent-apiserver --config /tmp/oai-agent-apiserver.yaml
   _output/platforms/$(go env GOOS)/$(go env GOARCH)/oai-chat-apiserver  --config /tmp/oai-chat-apiserver.yaml
   ```

5. Or render the Helm chart:

   ```bash
   helm template onexai manifests/installation/charts/onexai
   ```

See [docs/en-US/quickstart.md](docs/en-US/quickstart.md) for the full walkthrough.

## Configuration

- Example configs live in `configs/examples/`. Copy one, fill in the secrets, and pass
  it with `--config`.
- Secrets can be supplied through environment variables (for example
  `OAI_CHAT_APISERVER_BACKEND_REGEN_PASSWORD`) instead of the YAML file.
- The control plane connection is `controlplane.kubeconfig` plus
  `controlplane.namespace`.
- Duty selection:

  ```yaml
  duty:
    provider: regen   # none | regen; empty infers regen from backend.regen-endpoint
  ```

## Repository layout

```text
cmd/                 8 service entry points
internal/            service implementations (handler → biz → store)
pkg/                 public packages: APIs, control-plane client, duty contract,
                     shared clients
api/openapi/         generated OpenAPI documents
configs/             example service configuration
manifests/           Helm chart and Kubernetes manifests
docs/                bilingual documentation
hack/                development and operational helpers
scripts/             build, codegen, and release scripts
third_party/         patched forks required for a reproducible build
```

Internal packages follow a clean architecture: `handler` adapts transport, `biz` holds
domain logic, `store` owns persistence, and `wire` injects dependencies.

## Development

```bash
make deps           # install codegen and lint tools
make generate       # run go:generate directives
make protoc         # regenerate protobuf/gRPC/OpenAPI code
make format         # goimports (see the note below)
make test
```

> On macOS/arm64 set `CGO_ENABLED=0` when running tests: `go-m1cpu` crashes in `init`
> under some CGO configurations.

## Documentation

- [Architecture](docs/en-US/architecture.md)
- [Quick start](docs/en-US/quickstart.md)
- [Docker Compose](docs/en-US/docker-compose.md)
- [Configuration](docs/en-US/configuration.md)
- [Deployment](docs/en-US/deployment.md)
- [Development](docs/en-US/development.md)
- [Duty integration](docs/en-US/integrations/duty.md)
- Chinese docs: [docs/zh-CN](docs/zh-CN/)

## Contributing

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull
request. Security issues should follow [SECURITY.md](SECURITY.md).

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
