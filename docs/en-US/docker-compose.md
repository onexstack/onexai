# Docker Compose

`manifests/installation/compose/` starts a complete onexai deployment on one machine with a
single `docker compose up`. The long-form operational notes live in
[manifests/installation/compose/README.md](../../manifests/installation/compose/README.md); this page is the
short version.

## What it starts

- `oai-declserver` and `oai-controller-manager` (the declarative control plane,
  backed by etcd);
- `oai-agent-apiserver` (OpenAI-compatible gateway) and `oai-chat-apiserver`
  (Lark/Feishu ChatOps HTTP surface);
- `oai-job-controller`, `oai-jobserver`, and `oai-agent-runtime`;
- PostgreSQL and Redis.

Kafka and `oai-mqserver` are not part of the stack. `oai-mqserver` has no topic
subscribers and nothing in the repository produces Kafka messages.

## Prerequisites

- Docker Engine or Docker Desktop with Compose v2.20+.
- About 8 GB of free disk for images and the build cache.
- Network access on the first build.

## Quick start

```bash
cd manifests/installation/compose
cp env.example env
$EDITOR env                 # LLM_BASE_URL, LLM_API_KEY, JWT_KEY

docker compose --env-file env up -d --build
./smoke.sh
```

The first build compiles every service from source. The API is then available
on loopback:

```bash
curl -sS http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"agent":"default","messages":[{"role":"user","content":"2+3=?"}]}'
```

`agent` is required. `"default"` selects the built-in assistant; there is no
`GET /v1/models` route in this tree (model CRUD is `/v1/agent/models`).

## Ports

| Service | Host address |
|---|---|
| `oai-agent-apiserver` | `127.0.0.1:8080` |
| `oai-chat-apiserver` | `127.0.0.1:8082` |
| `oai-declserver` | `127.0.0.1:6444` |
| `oai-agent-runtime` | `127.0.0.1:8090` |

PostgreSQL, Redis, etcd, and the health/metrics ports stay on the compose
network. Every published port is bound to loopback by default.

## Configuration

`manifests/installation/compose/configs/` holds the non-secret topology. Secrets and the LLM
endpoint come from `env` (or the environment) and override the YAML through
viper:

| Variable | Config key |
|---|---|
| `LLM_BASE_URL` → `OAI_AGENT_APISERVER_LLM_BASE_URL` | `llm.base-url` |
| `LLM_API_KEY` → `OAI_AGENT_APISERVER_LLM_API_KEY` | `llm.api-key` |
| `LLM_MODEL` → `OAI_AGENT_APISERVER_LLM_MODEL` | `llm.model` |
| `JWT_KEY` → both `*_JWT_KEY` | `jwt.key` |
| `AUTH_BYPASS_AKSK` | `auth.bypass-aksk` |
| `AUTHZ_ENABLED` | `authz.enabled` |

An empty environment value is ignored, so the mounted file stays authoritative
when a variable is left unset. To mount a complete replacement config, set
`AGENT_CONFIG_FILE`, `CHAT_CONFIG_FILE`, or one of the other variables listed
in `env.example` to an absolute path.

The services start without an LLM, but runs fail with "AI engine is not
configured". A local Ollama works through
`http://host.docker.internal:11434/v1` with `LLM_ALLOW_PRIVATE_ENDPOINTS=true`.

## Development and secure modes

The default profile is for local development: `auth.bypass-aksk=true`,
`authz.enabled=false`, and an anonymous `AlwaysAllow` control plane. Nothing
needs to be seeded and every port is loopback-only.

The `secure` profile keeps caller authentication:

```bash
# env: AUTH_BYPASS_AKSK=false, AUTHZ_ENABLED=true
docker compose --env-file env --profile secure up -d --build
```

It adds a one-shot `seed-aksk` (writes the caller rows into `agent.secret`
after AutoMigrate) and `seed-authz` (applies the sample `RuntimeProfile`,
`Agent`, and `AuthorizationRule`). See the compose README for the production
hardening checklist; the control plane's anonymous mode and the dev JWT key are
not suitable for an exposed deployment.

## Reset and logs

```bash
docker compose --env-file env logs -f oai-agent-apiserver
docker compose --env-file env down        # keep data
docker compose --env-file env down -v     # delete Postgres/etcd/Redis data
```

`initdb/00-schemas.sql` runs only on an empty PostgreSQL volume. After changing
it, recreate the stack with `down -v`.
