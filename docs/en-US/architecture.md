# Architecture

onexai is a set of cooperating services around a declarative control plane.

## Control plane

`oai-declserver` is a Kubernetes-style aggregated API server backed by etcd. It serves
the CRDs in `pkg/apis/{agent,chat}` and is reached through a kubeconfig
(`controlplane.kubeconfig`). `oai-controller-manager` runs the controllers that validate
objects, resolve references, and publish status.

Runtime services do not keep a second copy of these resources. Agents, prompts, skills,
MCP servers, command grants, and peer bindings are read from the control plane through
informer caches. Duty groups are the exception: they are a chat-owned imperative resource,
with metadata, schedules, and escalation policy stored in PostgreSQL (`chat.oncall_*`).
BotApp is a deprecated declarative object: chat reads its Lark application from service
config, not from the control plane. DutyGroup, BotApp and ScheduledRun remain served only
so historical objects can be read or migrated.

## Data plane

| Store | Used for |
|---|---|
| PostgreSQL / MySQL (`coredb`) | sessions, messages, tickets, jobs, MCP rows, audit data |
| etcd | declarative objects served by `oai-declserver` |
| Kafka (`kafka`) | asynchronous event streams consumed by `oai-mqserver` |
| Redis (optional) | cross-pod session and rate-limit state |
| Object storage (`oss`) | assets, skills archives, job artifacts |

PostgreSQL deployments use three schemas (`agent`, `chat`, `job`); MySQL deployments use
prefixed table names. Set `coredb.dialect` accordingly.

## Request paths

- **OpenAI clients** call `oai-agent-apiserver` (`/v1/chat/completions`, `/v1/agent/agents`,
  `/v1/agent/skills`, `/v1/agent/mcpservers`, `/v1/agent/knowledgebases`).
- **Chat users** call `oai-chat-apiserver` through Lark/Feishu; the server turns messages
  into commands or agent turns, streams progress, and writes sessions/messages.
- **Long-running work** is submitted as a job or a Kafka event and consumed by
  `oai-jobserver` / `oai-mqserver`.
- **Tools** are exposed by `oai-mcpserver` and consumed by agents through MCP.

## Dependency direction

```text
cmd/<binary> → internal/<service>/{handler,biz,store} → pkg/*
```

`handler` adapts HTTP/MCP/IM transport, `biz` owns domain logic and interfaces, `store`
implements persistence, and `wire` builds the graph. Public contracts live under `pkg/`
so external clients (including the private harness CLI) can use them without importing
`internal/`.

## Optional integrations

All external platforms are adapters behind interfaces and are disabled until configured:
`pkg/duty` (Regen), Zadig, CMDB, Prometheus, RAGFlow, and usage reporting. A deployment
that configures none of them still runs the gateway, the chat bot, and the control plane.
