# 配置

每个服务都接受 `--config <file>`。文件是映射到服务 options 的 YAML；flag 优先于文件，
部分密钥可通过 `OAI_<SERVICE>_<PATH>` 环境变量提供。

## 公共配置块

```yaml
insecure:
  addr: ":8080"          # HTTP 监听地址

coredb:
  dialect: postgres      # postgres | mysql
  addr: 127.0.0.1:5432
  username: onexai
  password: CHANGE_ME
  database: onexai

otel:
  endpoint: 127.0.0.1:4317
  level: info
  output-mode: classic    # otel | console | file | classic | hybrid
```

## Agent 网关（`oai-agent-apiserver`）

```yaml
llm:
  base-url: https://api.openai.com/v1
  api-key: CHANGE_ME
  model: gpt-4.1-mini

agent:
  mcp-enabled: true
  ragflow:
    base-url: https://ragflow.example.com
    api-key: CHANGE_ME

controlplane:
  kubeconfig: /etc/onexai/declserver.kubeconfig
  namespace: default
```

网关提供 OpenAI 兼容接口。API Key 与按 Agent 的策略都是声明式资源；
`auth.control-token` 可选地保护管理路由。

### OIDC 与 Kubernetes 结构化认证

`oai-chat-apiserver` 可在登录响应中同时签发 OIDC（RS256）令牌：

```yaml
chat:
  oidc:
    enabled: true
    issuer-url: https://auth.example.com
    audiences: [onexai]
    key-file: /etc/onexai/oidc-key.pem
```

启用后它还发布 `/.well-known/openid-configuration` 与 `/keys`。issuer 必须是
HTTPS；本地自签证书时，把该 CA 放进下面的 `certificateAuthority`。

`oai-agent-apiserver` 通过 `auth.authentication-config` 指向一份
`apiserver.config.k8s.io/v1 AuthenticationConfiguration`，即可把 OIDC token
接入现有 `/v1` 认证链；HS256 浏览器令牌继续可用。示例见
`configs/examples/authentication-config.example.yaml`。

### 外部授权（AuthorizationConfiguration）

`oai-agent-apiserver` 的 `authz.authorization-config` 指向一份
`apiserver.config.k8s.io/v1 AuthorizationConfiguration`。其中的 Webhook
authorizer 按配置顺序求值（首个非 NoOpinion 生效），设置后取代
`authz.webhook.endpoints`；平台内置 OPA/CMDB/Casdoor 仍在 union 中。示例见
`configs/examples/authorization-config.example.yaml`。

### 认证 / 授权 WebHook（TokenReview / SubjectAccessReview）

`oai-agent-apiserver` 提供 Kubernetes 原生的访问评审端点：

- `POST /apis/authentication.k8s.io/v1/tokenreviews`（认证）
- `POST /apis/authorization.k8s.io/v1/subjectaccessreviews`（授权）

调用方在 `Authorization: Bearer <auth.webhook-token>` 中携带共享凭据；未配置
`auth.webhook-token` 时端点返回 503。控制台的 `/auth-webhooks` 页面提供同一决策
链路的在线评审。授权评审与运行期使用同一份 `AuthorizationRule` / OPA 策略，
回答“无匹配规则”时按 fail-closed 返回拒绝。

Agent 也可以在自身的「运行治理」步骤引用认证 / 鉴权 Webhook：非空时该 Agent 的
调用入口改用这些 Webhook，忽略平台的认证或授权。标准接口（路由、HTTP 方法、
入参与回参）以及失败语义见
[自定义认证 / 鉴权 Webhook](design/custom-auth-webhook.md)。

## 控制面（`oai-declserver`）

控制面只接受命令行 flag（compose / chart 里已经写好），认证与授权全部是
kube-apiserver 的同一套开关：

```text
--authentication-config=<AuthenticationConfiguration>
--authentication-token-webhook-config-file=<kubeconfig>
--token-auth-file=<csv>
--client-ca-file=<pem>  --requestheader-client-ca-file=<pem>
--authorization-config=<AuthorizationConfiguration>
--authorization-mode=AlwaysAllow|AlwaysDeny|RBAC|Webhook
--authorization-webhook-config-file=<kubeconfig>
```

`--authorization-config` 与 `--authorization-mode` / `--authorization-webhook-*`
互斥，同时给出会被拒绝。“`--authentication-config` 与 `--oidc-*` 互斥”同理。

### 原生认证评审

控制面自己服务 `authentication.k8s.io/v1`：

- `POST /apis/authentication.k8s.io/v1/tokenreviews`
- `POST /apis/authentication.k8s.io/v1/selfsubjectreviews`

它用的就是守护本服务请求的那个 authenticator，所以“这个 token 是谁”与直接用该
token 访问得到的答案是同一个。

### 用显式 WebHook 做认证与授权

把评审转发给 Agent（或任意实现这两个 API 的服务）时，kubeconfig 的
`cluster.server` 必须是**完整的资源 URL**，而不是 host 根路径：

```yaml
clusters:
- name: agent
  cluster:
    server: https://agent.onexai.svc:9444/apis/authorization.k8s.io/v1/subjectaccessreviews
    certificateAuthority: <PEM>
users:
- name: agent
  user:
    token: <auth.webhook-token>
```

认证 webhook 同理，路径换成 `/apis/authentication.k8s.io/v1/tokenreviews`。
两个端点可以共享一个 kubeconfig 文件的内容，但 `server` 必须各自写到资源路径。
另外，client-go 不会把 kubeconfig 里的凭据发往明文 HTTP 地址，因此 webhook
目标必须是 HTTPS（Agent 用 `secure.enabled=true` 打开 TLS 监听）。

### CEL 准入策略

`ValidatingAdmissionPolicy` / `MutatingAdmissionPolicy` 及其 Binding 默认启用，
且排在两个 webhook 准入插件之前：只依赖对象本身的策略不需要一次网络往返。

### Service 引用

控制面服务 core/v1 `Service`，`APIService`、CRD conversion webhook 与准入
webhook 里的 `service:` 引用会解析成真实的 ClusterIP。

### 控制台的扩展对象页

控制台的「运维中心 → 扩展中心 → 扩展对象」（`/extensions`）读写的正是上面这些
对象：准入 Webhook 配置、CEL 准入策略及其绑定、`APIService`。它经 Agent 的
`/v1/admin/extensions` 代理访问控制面，不直连 declserver，因此控制面只有一条
访问路径。页面编辑的是 API Server 存储的原始 JSON —— 上游新增字段时控制台不会
把它丢掉。

## ChatOps（`oai-chat-apiserver`）

```yaml
lark:
  app-id: CHANGE_ME
  app-secret: CHANGE_ME
  verification-token: CHANGE_ME
  event-mode: websocket

agent:
  endpoint: http://127.0.0.1:8080

websocket:
  allowed-origins: []    # 允许打开 WebSocket 的浏览器源；留空保持全放行
  max-concurrent-frames: 64

duty:
  provider: regen        # none | regen；留空时按 backend.regen-endpoint 推断

backend:
  regen-endpoint: https://regen.example.com
  regen-username: bot@example.com
  regen-password: CHANGE_ME
  regen-user-map: "bot@example.com=ou_xxx"
```

`oai-chat-apiserver` 在没有 `controlplane.kubeconfig` 时拒绝启动：命令授权与
PeerBinding 都来自控制面，再读一份 SQL 会成为第二个事实源。值班组与排班是例外：
它们是 chat 服务自有的 PostgreSQL 表，不依赖控制面。

WebSocket 握手不受浏览器同源策略保护。控制台与服务分开部署时，应把控制台的源写进
`websocket.allowed-origins`；留空保持历史上的全放行行为。不带 `Origin` 的请求
（CLI 客户端）始终允许。

可选适配器（默认全部关闭）：`backend.zadig-*`、`backend.cmdb-*`、
`backend.prometheus-endpoint`、`backend.dns-whitelist-*`。

## Job、MQ 与 MCP Server

```yaml
coredb: { ... }

kafka:
  brokers: ["127.0.0.1:9092"]
  group-id: onexai

oss:
  endpoint: http://127.0.0.1:9000
  access-key: CHANGE_ME
  secret-key: CHANGE_ME
  bucket: onexai
```

## 密钥

不要把真实密钥提交到仓库。优先使用环境变量或平台的密钥管理。Helm chart 会把服务配置
渲染成 Kubernetes Secret；`values.yaml` 中的值刻意留空。
