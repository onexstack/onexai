# 快速开始

本文在一台机器上启动控制面与两个主服务。

> 想要一条命令同时启动 PostgreSQL、Redis、任务 worker 与 A2A runtime，见
> [Docker Compose](docker-compose.md)。

## 1. 前置条件

- Go 1.27+
- etcd 3.5+
- PostgreSQL 16+（或 MySQL 8+）
- `kubectl`（用于访问 `oai-declserver`）
- 可选：Kafka、Redis、OpenAI 兼容 LLM 端点

## 2. 构建

```bash
make build
export PATH="$PWD/_output/platforms/$(go env GOOS)/$(go env GOARCH):$PATH"
```

## 3. 启动 etcd 与 PostgreSQL

任意本地安装均可，例如：

```bash
docker run -d --name onexai-etcd -p 2379:2379 quay.io/coreos/etcd:v3.5.15   /usr/local/bin/etcd --advertise-client-urls http://0.0.0.0:2379   --listen-client-urls http://0.0.0.0:2379

docker run -d --name onexai-pg -p 5432:5432   -e POSTGRES_USER=onexai -e POSTGRES_PASSWORD=onexai -e POSTGRES_DB=onexai   postgres:16-alpine
```

## 4. 启动控制面

`oai-declserver` 全部通过 flag 配置，不读配置文件：

```bash
mkdir -p ~/.onexai/controlplane
oai-declserver   --etcd-servers=http://127.0.0.1:2379   --etcd-prefix=/registry/onexai.io   --secure-port=6444   --bind-address=127.0.0.1   --cert-dir=$HOME/.onexai/controlplane/certs   --kubeconfig=$HOME/.onexai/declserver.kubeconfig   --token-auth-file=$HOME/.onexai/controlplane/tokens.csv   --authorization-mode=AlwaysAllow
```

服务会把自签服务端证书写入 `--cert-dir`。CoreAPI 的 kubeconfig 通过
`--kubeconfig`（或环境变量 `ONEXCONFIG`）指定，必须指向该服务；Helm chart 会自动
渲染；集群安装可用 `manifests/installation/controlplane-certs.sh` 生成证书与 token。
在安装可信证书之前，kubeconfig 应写 `https://127.0.0.1:6444` 并设置
`insecure-skip-tls-verify: true`。

启动控制器：

```bash
oai-controller-manager   --kubeconfig=$HOME/.onexai/declserver.kubeconfig   --config=configs/examples/oai-controller-manager.example.yaml
```

## 5. 启动 Agent 网关

```bash
cp configs/examples/oai-agent-apiserver.example.yaml /tmp/oai-agent-apiserver.yaml
# 修改 coredb.*、controlplane.kubeconfig、llm.* 与相关密钥
oai-agent-apiserver --config /tmp/oai-agent-apiserver.yaml
```

验证：

```bash
curl -s http://127.0.0.1:8080/healthz
# 模型 CRUD（当前代码没有 OpenAI 风格的 /v1/models 路由）
curl -s http://127.0.0.1:8080/v1/agent/models -H 'Authorization: Bearer <key>'
```

## 6. 启动聊天服务（可选）

```bash
cp configs/examples/oai-chat-apiserver.example.yaml /tmp/oai-chat-apiserver.yaml
# 修改 coredb.*、controlplane.kubeconfig、lark.*、agent.* 与 duty.*
oai-chat-apiserver --config /tmp/oai-chat-apiserver.yaml
```

没有飞书凭据时 HTTP 面仍会启动，但机器人不会连接。

## 7. Helm

chart 会渲染控制面、两个服务及其 Secret：

```bash
helm template onexai manifests/installation/charts/onexai
helm upgrade --install onexai manifests/installation/charts/onexai -n onexai-system
```

完整配置见 [配置](configuration.md)。
