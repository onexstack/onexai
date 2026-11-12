# Docker Compose

`manifests/installation/compose/` 用一条 `docker compose up` 在一台机器上启动完整的 onexai
部署。详细运维说明见 [manifests/installation/compose/README.md](../../manifests/installation/compose/README.md)，
本文是简版。

## 启动内容

- `oai-declserver` 与 `oai-controller-manager`（声明式控制面，底层为 etcd）；
- `oai-agent-apiserver`（OpenAI 兼容网关）与 `oai-chat-apiserver`
  （飞书/Lark ChatOps HTTP 面）；
- `oai-job-controller`、`oai-jobserver`、`oai-agent-runtime`；
- PostgreSQL 与 Redis。

Kafka 与 `oai-mqserver` 不在该栈中：`oai-mqserver` 没有注册任何 topic 订阅，
仓库里也没有 Kafka 生产者。

## 前置条件

- Docker Engine 或 Docker Desktop，Compose v2.20+。
- 约 8 GB 可用磁盘（镜像与构建缓存）。
- 首次构建需要网络。

## 快速开始

```bash
cd manifests/installation/compose
cp env.example env
$EDITOR env                 # 填写 LLM_BASE_URL、LLM_API_KEY、JWT_KEY

docker compose --env-file env up -d --build
./smoke.sh
```

首次构建会从源码编译全部服务。启动后 API 只监听回环地址：

```bash
curl -sS http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"agent":"default","messages":[{"role":"user","content":"2+3=?"}]}'
```

`agent` 必填；`"default"` 表示使用内建助手。当前代码没有 `GET /v1/models`
路由（模型 CRUD 是 `/v1/agent/models`）。

## 端口

| 服务 | 宿主机地址 |
|---|---|
| `oai-agent-apiserver` | `127.0.0.1:8080` |
| `oai-chat-apiserver` | `127.0.0.1:8082` |
| `oai-declserver` | `127.0.0.1:6444` |
| `oai-agent-runtime` | `127.0.0.1:8090` |

PostgreSQL、Redis、etcd 以及健康/指标端口都留在 compose 网络内。所有发布端口
默认只绑回环地址。

## 配置

`manifests/installation/compose/configs/` 存放非敏感的拓扑配置。密钥与 LLM 端点来自 `env`
（或进程环境），通过 viper 覆盖 YAML：

| 变量 | 配置键 |
|---|---|
| `LLM_BASE_URL` → `OAI_AGENT_APISERVER_LLM_BASE_URL` | `llm.base-url` |
| `LLM_API_KEY` → `OAI_AGENT_APISERVER_LLM_API_KEY` | `llm.api-key` |
| `LLM_MODEL` → `OAI_AGENT_APISERVER_LLM_MODEL` | `llm.model` |
| `JWT_KEY` → 两边的 `*_JWT_KEY` | `jwt.key` |
| `AUTH_BYPASS_AKSK` | `auth.bypass-aksk` |
| `AUTHZ_ENABLED` | `authz.enabled` |

空环境变量会被忽略，因此未设置的变量不会覆盖挂载文件。要整份替换配置，把
`env.example` 里的 `AGENT_CONFIG_FILE`、`CHAT_CONFIG_FILE` 等指向绝对路径即可。

不配置 LLM 时服务能启动，但运行会报 “AI engine is not configured”。本地
Ollama 用 `http://host.docker.internal:11434/v1`，并设
`LLM_ALLOW_PRIVATE_ENDPOINTS=true`。

## 开发模式与安全模式

默认 profile 面向本地开发：`auth.bypass-aksk=true`、`authz.enabled=false`，
控制面匿名 + `AlwaysAllow`，无需 seed，端口只绑回环。

`secure` profile 保留调用方鉴权：

```bash
# env: AUTH_BYPASS_AKSK=false, AUTHZ_ENABLED=true
docker compose --env-file env --profile secure up -d --build
```

它会增加两个一次性服务：`seed-aksk`（AutoMigrate 完成后写入
`agent.secret`）和 `seed-authz`（apply 示例 `RuntimeProfile`、`Agent` 与
`AuthorizationRule`）。生产加固清单见 compose README；控制面匿名模式与开发
JWT key 不适合暴露到公网的部署。

## 重置与日志

```bash
docker compose --env-file env logs -f oai-agent-apiserver
docker compose --env-file env down        # 保留数据
docker compose --env-file env down -v     # 删除 Postgres/etcd/Redis 数据
```

`initdb/00-schemas.sql` 只在 PostgreSQL 数据卷为空时执行。修改后用
`down -v` 重新创建。
