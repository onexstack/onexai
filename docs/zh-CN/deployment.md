# 部署

## Helm

`manifests/installation/charts/onexai` 是一个同时管理控制面与运行时服务的 chart。

```bash
helm upgrade --install onexai manifests/installation/charts/onexai   -n onexai-system --create-namespace   -f values-secrets.yaml
```

chart 会创建：

- `oai-declserver`（及其服务端证书与 token）；
- 带控制面 kubeconfig 的 `oai-controller-manager`；
- `oai-agent-apiserver`、`oai-chat-apiserver`，以及可选的 `oai-jobserver`、
  `oai-mqserver`、`oai-mcpserver`；
- 每个服务一个 Secret，由 `files/<service>.yaml` 渲染；
- RBAC 与 Service。

## Docker Compose

单机部署可直接使用 `manifests/installation/compose/`：一条命令启动控制面、两个 API 服务、
任务 worker、A2A runtime、PostgreSQL、Redis 与 etcd。Kafka 与 `oai-mqserver`
不在其中。详见 [Docker Compose](docker-compose.md)。

默认 profile 是开发拓扑（bypass-aksk、关闭授权、匿名控制面、仅回环端口）；
`secure` profile 会 seed 调用方密钥与授权规则。生产加固清单见
`manifests/installation/compose/README.md`。

## 外部依赖

- **etcd**：托管服务或独立 StatefulSet 均可。每个部署使用独立的 `--etcd-prefix`；
  两个部署绝不能共用前缀。
- **PostgreSQL**：一个数据库、三个 schema（`agent`、`chat`、`job`）。AutoMigrate
  只建表、不建 schema，因此全新数据库需要预先创建（compose 通过
  `manifests/installation/compose/initdb/00-schemas.sql` 完成）。也支持
  `coredb.dialect: mysql`，但主路径是 PostgreSQL。
- **Kafka**：仅 `oai-mqserver` 使用，而它当前没有注册任何 topic 订阅；compose
  不包含它。**Redis** 可选（compose 默认包含，用于会话与 nonce）。
- **LLM 端点**：任意 OpenAI 兼容端点，包括本地 vLLM。即使 Agent 指定了 Model
  对象，`llm.base-url` 也必须非空。
- **飞书/Lark 应用**：订阅消息与卡片事件，回调指向 `oai-chat-apiserver`。

## 扩缩容说明

- `oai-chat-apiserver` 在进程内保存 WebSocket hub、Lark 连接与去重状态；除非把会话状态
  外置，否则只跑 **1 个副本**。
- `oai-agent-apiserver` 除 Redis/informer 缓存外无状态，可水平扩展。
- 多副本 `oai-controller-manager` 使用 leader election。

## 镜像

`build/docker/` 下的 Dockerfile 可构建全部二进制与 distroless 运行镜像。compose
栈使用 `manifests/installation/compose/Dockerfile`：一个参数化配方通过
`--build-arg SERVICE=<cmd 目录名>` 构建全部服务，并选择 Debian 运行时（健康检查
需要 `curl`）。通过 `REGISTRY`/`REGISTRY_PREFIX` 覆盖镜像仓库，不依赖私有
registry。

```bash
make image REGISTRY_PREFIX=ghcr.io/onexstack
```
