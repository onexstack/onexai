# onexai on docker compose

A self-contained, single-machine deployment of the onexai gateway and control
plane. One `docker compose up` starts the control plane, both API servers, the
job workers, an A2A runtime, PostgreSQL, Redis, and etcd.

Kafka and `oai-mqserver` are intentionally **not** part of this stack:
`oai-mqserver` registers no topic subscribers and nothing in the repository
produces Kafka messages.

## What runs

| Service | Role | Host port |
|---|---|---|
| `oai-declserver` | declarative control plane (etcd-backed) | `127.0.0.1:6444` |
| `oai-controller-manager` | reconciles agents, bindings, prompts, … | — |
| `oai-agent-apiserver` | OpenAI-compatible gateway | `127.0.0.1:8080` |
| `oai-chat-apiserver` | Lark/Feishu ChatOps HTTP surface | `127.0.0.1:8082` |
| `oai-job-controller` | `batch.onex.io` Job/CronJob reconciler | — |
| `oai-jobserver` | async job watcher | — |
| `oai-agent-runtime` | A2A runtime (echo by default) | `127.0.0.1:8090` |
| `etcd` | control-plane storage | — |
| `postgres` | sessions, tickets, runs, jobs | — |
| `redis` | agent sessions and nonces | — |

Override the host ports with `DECLSERVER_HOST_PORT`, `AGENT_HOST_PORT`,
`CHAT_HOST_PORT`, and `RUNTIME_HOST_PORT` in `env` if something already listens
on the defaults. (The control plane's host kubeconfig is a static file; update
`configs/declserver.kubeconfig.host` if you change its port.)

## Prerequisites

- Docker Engine or Docker Desktop with Compose **v2.20+** (the secure profile
  uses `service_completed_successfully`).
- About 8 GB of free disk for images and build cache.
- Network access on the first build (Go modules and base images).

## Quick start

```bash
cd manifests/installation/compose
cp env.example env
$EDITOR env                 # set LLM_BASE_URL, LLM_API_KEY, and a JWT_KEY

docker compose --env-file env up -d --build
./smoke.sh
```

The first build compiles every service from source and can take several
minutes. Later starts reuse the images.

Send a completion (the `agent` field is required; `default` selects the
built-in assistant):

```bash
set -a; . ./env; set +a
curl -sS http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"agent":"default","model":"'"${LLM_MODEL:-gpt-4o-mini}"'",
       "messages":[{"role":"user","content":"只回答数字：2+3=?"}]}'
```

> The quickstart's `GET /v1/models` does not exist in this tree. Model CRUD is
> `/v1/agent/models`; completions are `/v1/chat/completions`.

## Configuration

Non-secret topology lives in `configs/`. Secrets and the LLM endpoint come from
`env`, which overrides the YAML through viper:

```
OAI_AGENT_APISERVER_LLM_BASE_URL   -> llm.base-url
OAI_AGENT_APISERVER_LLM_API_KEY    -> llm.api-key
OAI_AGENT_APISERVER_LLM_MODEL      -> llm.model
OAI_AGENT_APISERVER_JWT_KEY        -> jwt.key
OAI_CHAT_APISERVER_JWT_KEY         -> jwt.key
OAI_AGENT_APISERVER_AUTH_BYPASS_AKSK -> auth.bypass-aksk
OAI_AGENT_APISERVER_AUTHZ_ENABLED  -> authz.enabled
```

An empty environment value is ignored by viper, so leaving `LLM_API_KEY=` unset
keeps whatever the mounted file says.

To mount a completely different config, point the matching variable at an
absolute path (see `env.example`):

```bash
AGENT_CONFIG_FILE=/path/to/oai-agent-apiserver.yaml
```

The services start without an LLM, but every run fails with "AI engine is not
configured". Add `LLM_BASE_URL` and `LLM_API_KEY` to get answers. For a local
Ollama, use `http://host.docker.internal:11434/v1` and set
`LLM_ALLOW_PRIVATE_ENDPOINTS=true`.

### Lark / Feishu

Set `LARK_APP_ID` and `LARK_APP_SECRET` to connect the bot. Without them the
chat HTTP surface still starts; the websocket client logs reconnect attempts
and the bot does not receive messages.

## Two auth modes

**Development (default).** `auth.bypass-aksk=true`, `authz.enabled=false`, and
the control plane runs with anonymous auth + `AlwaysAllow`. Host ports are
bound to loopback only. This is the mode that works without seeding anything.

**Secure profile.**

```bash
# in env:
AUTH_BYPASS_AKSK=false
AUTHZ_ENABLED=true

docker compose --env-file env --profile secure up -d --build
```

The `secure` profile adds two one-shot services:

- `seed-aksk` waits for the agent's `/readyz` (so AutoMigrate has run) and
  writes the `oai-chat-apiserver` / `oai-job-controller` rows into
  `agent.secret`.
- `seed-authz` applies `seed/` — an echo `RuntimeProfile`, an `Agent`, and the
  `AuthorizationRule`s that let it run. declserver serves these types directly;
  no CRDs are installed.

The control plane still trusts anonymous callers because it is only reachable
on the compose network and loopback. That is a local-development boundary, not
a production one; see **Production hardening**.

## Agent runtime

The default `configs/runtime.json` is an echo runtime (`sh -c 'printf …'`). It
needs no Node and no model credential, and proves the A2A card and
JSON-RPC/REST surfaces. The gateway still refuses every run when
`llm.base-url` is empty (the admission check is shared with the in-process
engine), so set `LLM_BASE_URL` even for the echo demo; the echo runtime itself
never calls the model. The gateway's default runtime is the in-process eino
engine; the Agent in `seed/` is what routes a run to the A2A container.

To run a real Claude Code CLI instead:

```bash
docker build -f Dockerfile.agent-runtime --build-arg INSTALL_CLAUDE_CLI=1 \
  -t onexai/oai-agent-runtime:dev ../../..
RUNTIME_CONFIG_FILE=$PWD/configs/runtime.claude.json \
  ANTHROPIC_API_KEY=... docker compose --env-file env up -d --no-build oai-agent-runtime
```

The CLI image is much larger, needs `ANTHROPIC_API_KEY`, and runs with
`--permission-mode bypassPermissions`; review that before pointing it at
anything but a disposable workspace.

## Operations

```bash
docker compose --env-file env ps
docker compose --env-file env logs -f oai-agent-apiserver
docker compose --env-file env down          # stop, keep data
docker compose --env-file env down -v       # stop and delete volumes
```

PostgreSQL initialisation runs only when `pgdata` is empty. After changing
`initdb/00-schemas.sql`, use `down -v` and start again.

## Troubleshooting

- **Agent fails to start with "the resource store needs the declarative
  control plane"** — `oai-declserver` is not ready or the kubeconfig is wrong.
  Check `docker compose logs oai-declserver`.
- **Agent fails on Redis** — a configured Redis that is unreachable is a
  startup error. Wait for the `redis` healthcheck or remove `redis.addr` from
  the config.
- **All named-agent requests are 403** — `authz.enabled` is on with no
  `AuthorizationRule`. Use the default development mode, or apply `seed/`.
- **Completions fail with "AI engine is not configured"** — `llm.base-url` is
  empty. Fill `LLM_BASE_URL`.
- **`schema "agent" does not exist`** — the Postgres volume predates
  `initdb/00-schemas.sql`; recreate with `down -v`.
- **job-controller exits with "unable to find leader election namespace"** —
  config files use mapstructure keys, not flag names: set
  `leader-election: false` (the flag spelling is `--leader-elect`). The default
  is true and requires a namespace.
- **A service exits with "--fake.endpoint is required"** — the option
  validation requires `fake.endpoint` and `oss.endpoint` even though this stack
  does not contact them; the provided configs set both.
- **A service exits with "--.key must larger than 5 and little than 33"** —
  `jwt.key` must be 6–32 characters. The compose default is 32; generate a real
  one with `openssl rand -hex 32`.
- **A service exits with "proto: file auth.proto is already registered"** — the
  images set `GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn`; this is a dependency
  conflict between `go.etcd.io/etcd/api/v3/authpb` and the Polarismesh
  specification, not a configuration error, and neither `auth.proto` is used by
  these processes.
- **Build killed with "cannot allocate memory"** — `docker compose build` runs
  every service in parallel and each Go compiler is memory-hungry. Build one at
  a time on a small Docker Desktop VM:
  `COMPOSE_PARALLEL_LIMIT=1 docker compose --env-file env build`.
- **declserver health** — compose probes `/healthz`. `/readyz` additionally
  gates on the aggregator's Service informer, which a standalone declserver
  does not serve, so it can stay 500 even though the control plane is usable.
- **Chat logs websocket reconnect errors** — Lark is not configured; either
  ignore them or set the credentials.

## Production hardening

This stack is a development and evaluation topology. Before exposing it:

- Disable `auth.bypass-aksk`, enable `authz`, and seed real `AuthorizationRule`
  and `agent.secret` data.
- Replace the anonymous `AlwaysAllow` control plane with token-file or RBAC
  auth (see `manifests/installation/controlplane-certs.sh` for the certificate
  flow used by the Helm chart).
- Generate a real `JWT_KEY` with `openssl rand -hex 32`; never reuse the
  compose default.
- Terminate TLS in front of the API servers and stop binding ports to loopback.
  Do not publish `6444` (the self-signed control plane).
- Give `oai-chat-apiserver` one replica: it holds an exclusive instance lock in
  its data directory.
- Do not enable `agent.sandbox` without deciding whether mounting
  `/var/run/docker.sock` into the gateway is acceptable. It is off by default.

## Image build notes

`Dockerfile` builds every Go service with `--build-arg SERVICE=<cmd name>`, so
the stack needs one Dockerfile rather than seven. The runtime stage is Debian
slim (not distroless) because the healthchecks use `curl`.

If Docker Hub is not reachable, point the builder and runtime bases at a mirror
in `env` (the defaults are the upstream images):

```bash
GO_IMAGE=mirror.example.com/library/golang:1.27.0
RUNTIME_IMAGE=mirror.example.com/library/debian:bookworm-slim
# The module proxy the builder uses; empty keeps Go's default.
GOPROXY=https://goproxy.cn,direct
```

The working tree replaces `github.com/onexstack/onexstack` with `../onexstack`,
which is outside the build context. The image build drops that replace and uses
the released v1.0.8 that `go.mod` already requires; the only local difference is
a log-noise suppression. `Dockerfile.dockerignore` excludes `_output` and the
root-level build artefacts so the context does not carry several gigabytes.
