# CLAUDE.md

onexai is an open-source Go module: an OpenAI-compatible agent gateway plus a
Lark/Feishu ChatOps runtime on a Kubernetes-style declarative control plane.

## Binaries

| Binary | Responsibility |
|---|---|
| `oai-agent-apiserver` | OpenAI-compatible gateway: chat completions, agents, MCP, skills, knowledge bases, prompts, models |
| `oai-agent-runtime` | A2A runtime and sandbox execution |
| `oai-chat-apiserver` | Lark/Feishu bot, ChatOps, sessions/messages, duty/tickets |
| `oai-declserver` | Declarative control-plane API server (etcd-backed) |
| `oai-controller-manager` | Controllers for agents, bindings, bot apps, duty groups, peer bindings |
| `oai-jobserver` | Async job and cronjob consumer |
| `oai-job-controller` | Reconciles `batch.onex.io` Job/CronJob; runs the agentrun provider |
| `oai-scheduledrun-migrator` | One-shot ScheduledRun → batch CronJob migration |
| `oai-mqserver` | Kafka consumer |
| `oai-mcpserver` | MCP tool server (stdio / SSE / streamable HTTP) |

## Layout

- `cmd/<binary>/` — thin entry points.
- `internal/<service>/{handler,biz,store,model}` — clean architecture; dependencies flow
  handler → biz → store; Wire builds the graph.
- `pkg/` — public contracts: APIs, declarative client, `duty`, shared clients.
- `pkg/apis/` — CRD types; `pkg/generated/` — checked-in codegen.
- `configs/examples/` — example service configuration.
- `manifests/installation/charts/onexai` — Helm chart.
- `docs/en-US` and `docs/zh-CN` — mirrored documentation.

## Commands

```bash
make build                 # all cmd/ binaries into _output/platforms/<os>/<arch>/
make test                  # unit tests (use CGO_ENABLED=0 on macOS/arm64)
make lint.defensive        # merge-gating lint
make generate              # go:generate
make protoc                # protobuf/gRPC/OpenAPI/defaults
make gen-k8s               # deepcopy/conversion/client/informer
make verify.public         # no private paths/hosts/module names
```

Do not run `go test ./...` on a laptop: some packages need SQLite/CGO or a cluster.
Use package-scoped tests. On macOS/arm64 set `CGO_ENABLED=0` (go-m1cpu crashes in
`init` under some CGO configurations).

## Conventions

- Comments, log messages, and docs are English; user-facing product strings may be
  Chinese and are mirrored in the bilingual docs.
- Reuse onexstack packages (`pkg/store`, `pkg/errorsx`, `pkg/options`, `pkg/server`,
  `pkg/ptr`) instead of re-implementing them.
- CRD fields and database columns are stable APIs. Do not rename a persisted field
  without a migration.
- Optional integrations (duty providers, Zadig, CMDB, Prometheus, RAGFlow, usage
  reporting) stay disabled by default and must not make a service fail to start when
  they are down.
- The private `harness` CLI is not in this repository. Do not add imports of
  `internal/onexai` or reference its files.
- When changing a declarative resource, update the control-plane types, the controller,
  and the runtime together; keep the example configs and both language docs in sync.

## Duty

`pkg/duty` is the provider contract. `pkg/duty/regen` is the reference implementation.
Selection is `duty.provider: none|regen`; an empty value infers `regen` from
`backend.regen-endpoint`. The in-memory store lives in `internal/chat/duty`; the ticket
mirror in `internal/chat/store` depends only on the contract.
