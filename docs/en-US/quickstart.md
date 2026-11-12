# Quick start

This walkthrough runs the control plane and the two main services on one machine.

> For a one-command deployment that also starts PostgreSQL, Redis, the job
> workers, and an A2A runtime, see [Docker Compose](docker-compose.md).

## 1. Prerequisites

- Go 1.27+
- etcd 3.5+
- PostgreSQL 16+ (or MySQL 8+)
- `kubectl` (to talk to `oai-declserver`)
- Optional: Kafka, Redis, an OpenAI-compatible LLM endpoint

## 2. Build

```bash
make build
export PATH="$PWD/_output/platforms/$(go env GOOS)/$(go env GOARCH):$PATH"
```

## 3. Start etcd and PostgreSQL

Any local installation works. For example:

```bash
docker run -d --name onexai-etcd -p 2379:2379 quay.io/coreos/etcd:v3.5.15   /usr/local/bin/etcd --advertise-client-urls http://0.0.0.0:2379   --listen-client-urls http://0.0.0.0:2379

docker run -d --name onexai-pg -p 5432:5432   -e POSTGRES_USER=onexai -e POSTGRES_PASSWORD=onexai -e POSTGRES_DB=onexai   postgres:16-alpine
```

## 4. Start the control plane

`oai-declserver` is configured by flags, not a config file:

```bash
mkdir -p ~/.onexai/controlplane
oai-declserver   --etcd-servers=http://127.0.0.1:2379   --etcd-prefix=/registry/onexai.io   --secure-port=6444   --bind-address=127.0.0.1   --cert-dir=$HOME/.onexai/controlplane/certs   --kubeconfig=$HOME/.onexai/declserver.kubeconfig   --token-auth-file=$HOME/.onexai/controlplane/tokens.csv   --authorization-mode=AlwaysAllow
```

The server writes a self-signed serving certificate into `--cert-dir`. Point the
CoreAPI kubeconfig (`--kubeconfig`, or `ONEXCONFIG` in the environment) at the
server; the Helm chart renders this for you, and
`manifests/installation/controlplane-certs.sh` generates the certificates and
tokens for a cluster install. The kubeconfig must name
`https://127.0.0.1:6444` with `insecure-skip-tls-verify: true` until you install
a trusted serving certificate.

Start the controllers:

```bash
oai-controller-manager   --kubeconfig=$HOME/.onexai/declserver.kubeconfig   --config=configs/examples/oai-controller-manager.example.yaml
```

## 5. Start the agent gateway

```bash
cp configs/examples/oai-agent-apiserver.example.yaml /tmp/oai-agent-apiserver.yaml
# edit coredb.*, controlplane.kubeconfig, llm.*, and any secrets
oai-agent-apiserver --config /tmp/oai-agent-apiserver.yaml
```

Check it:

```bash
curl -s http://127.0.0.1:8080/healthz
# Model CRUD (the OpenAI-style /v1/models route does not exist in this tree).
curl -s http://127.0.0.1:8080/v1/agent/models -H 'Authorization: Bearer <key>'
```

## 6. Start the chat service (optional)

```bash
cp configs/examples/oai-chat-apiserver.example.yaml /tmp/oai-chat-apiserver.yaml
# edit coredb.*, controlplane.kubeconfig, lark.*, agent.*, and duty.*
oai-chat-apiserver --config /tmp/oai-chat-apiserver.yaml
```

Without Lark credentials the HTTP surface still starts, but the bot does not connect.

## 7. Helm

The chart renders the control plane, both services, and their secrets:

```bash
helm template onexai manifests/installation/charts/onexai
helm upgrade --install onexai manifests/installation/charts/onexai -n onexai-system
```

See [Configuration](configuration.md) for every block.
