# 自定义认证 / 鉴权 Webhook

本文定义平台与外部认证、鉴权服务之间的标准接口。任何服务按本文实现两个 HTTP
端点，就可以在控制台的「扩展中心 → Webhook」里创建为认证或鉴权后端，并被
Agent 的「运行治理」步骤引用。

平台自身也实现了这两个协议（`/apis/authentication.k8s.io/v1/tokenreviews` 与
`/apis/authorization.k8s.io/v1/subjectaccessreviews`），可以直接把一个 onexai
实例配置成另一个实例的后端。

## 一、资源模型

认证 / 鉴权后端复用现有的 `Webhook` 资源，通过 `spec.type` 区分用途：

| `spec.type` | 用途 | 协议 |
|---|---|---|
| 空 / `run` | 运行生命周期钩子 | 平台自有事件契约 |
| `authentication` | 认证后端 | Kubernetes `TokenReview` |
| `authorization` | 鉴权后端 | Kubernetes `SubjectAccessReview` |

公共字段：

```yaml
apiVersion: agent.onexai.io/v1alpha1
kind: Webhook
metadata:
  name: corporate-sso
  namespace: default
spec:
  type: authentication
  enabled: true
  # 完整 URL；平台直接 POST 到这里，不拼接路径。
  clientConfig:
    url: https://sso.internal/apis/authentication.k8s.io/v1/tokenreviews
    caBundle: <PEM>
    headers:
      X-Tenant: acme
  secretRef:
    name: sso-token          # 值作为 Authorization: Bearer 发送
  timeoutSeconds: 5
  failureMode: fail
  audiences: [onexai]        # 仅 authentication
```

约束：

- `events` / `template` 只属于运行钩子；认证、鉴权 Webhook 必须留空。
- `audiences` 只属于认证 Webhook。
- `failureMode`：运行钩子取 `ignore|fail`；鉴权 Webhook 取 `ignore`（无意见）或
  `fail`（拒绝）；认证 Webhook 始终 fail-closed，`failureMode` 不影响结果。
- `secretRef` 是引用而不是明文；平台每次调用前从 Secret 读取。

## 二、认证 Webhook — TokenReview

| 项 | 约定 |
|---|---|
| HTTP 方法 | `POST` |
| 路由 | `spec.clientConfig.url` 的完整 URL |
| Content-Type | `application/json` |
| 调用方凭据 | `Authorization: Bearer <secretRef 的值>`（同时发送 `X-Onexai-Token`） |
| 成功响应 | 任意 `2xx` + 同 kind 对象 |

请求：

```json
{
  "apiVersion": "authentication.k8s.io/v1",
  "kind": "TokenReview",
  "spec": {
    "token": "<调用方提供的 Bearer token>",
    "audiences": ["onexai"]
  }
}
```

响应：

```json
{
  "apiVersion": "authentication.k8s.io/v1",
  "kind": "TokenReview",
  "status": {
    "authenticated": true,
    "user": {
      "username": "alice",
      "uid": "10001",
      "groups": ["engineering", "oncall"],
      "extra": { "tenant": ["acme"] }
    },
    "audiences": ["onexai"],
    "error": ""
  }
}
```

判定：

- `authenticated=true` 且 `user.username` 非空 → 认证通过。该身份成为本次请求的
  调用者，并原样出现在 Agent 鉴权 Webhook 的 `spec.user` / `spec.groups` 中。
- `authenticated=false` → 该 Webhook 拒绝；平台继续尝试下一个认证 Webhook，全部
  拒绝则返回 `401`。
- 非 2xx、超时、无法解析 → 视为该 Webhook 调用失败，同样是拒绝。**不会**
  fallback 到平台 JWT/OIDC。

## 三、鉴权 Webhook — SubjectAccessReview

| 项 | 约定 |
|---|---|
| HTTP 方法 | `POST` |
| 路由 | `spec.clientConfig.url` 的完整 URL |
| 请求体 | `authorization.k8s.io/v1 SubjectAccessReview` |
| 成功响应 | 任意 `2xx` + 同 kind 对象 |

请求（由平台按运行上下文生成）：

```json
{
  "apiVersion": "authorization.k8s.io/v1",
  "kind": "SubjectAccessReview",
  "spec": {
    "user": "alice",
    "groups": ["engineering", "oncall"],
    "extra": {
      "onexai.io/subject-kind": ["user"],
      "onexai.io/agent-name": ["my-agent"],
      "onexai.io/tool-name": ["knowledge_search"],
      "onexai.io/mcp-name": ["kb-mcp"]
    },
    "resourceAttributes": {
      "namespace": "default",
      "verb": "get",
      "group": "onexai.io",
      "version": "v1",
      "resource": "agents",
      "name": "my-agent"
    }
  }
}
```

响应：

```json
{
  "apiVersion": "authorization.k8s.io/v1",
  "kind": "SubjectAccessReview",
  "status": {
    "allowed": false,
    "denied": true,
    "reason": "not in the oncall group",
    "evaluationError": ""
  }
}
```

判定（与 Kubernetes 一致）：

- `allowed=true` → 允许。
- `denied=true` → 拒绝；Deny 优先于 Allow。
- 两者都为 false → 无意见（NoOpinion）。
- `evaluationError` 非空 → 调用失败，按 `failureMode` 处理。

Agent 级鉴权是 fail-closed 的：所有被引用的 Webhook 都返回 NoOpinion 时拒绝。

## 四、Agent 中的配置

Agent 的「运行治理」步骤有三个入口：

| 字段 | 含义 |
|---|---|
| `spec.webhooks` | 运行生命周期钩子（onStart / onEnd） |
| `spec.authnWebhooks` | 认证 Webhook 名字列表；非空时**替代**平台认证 |
| `spec.authzWebhooks` | 鉴权 Webhook 名字列表；非空时**替代**平台授权（含 `authzBackends`） |
| `spec.authzBackends` | 平台授权后端子集（OPA / CMDB / Casdoor / …） |

覆盖规则：

- **认证**：`authnWebhooks` 非空时，Bearer 凭据只由这些 TokenReview Webhook
  认证，平台 JWT / OIDC 不参与；AK/SK 仍由平台验签（双通道）。全部 Webhook 拒绝
  → `401`，不会退回平台链。
- **鉴权**：`authzWebhooks` 非空时，Agent 级与工具级授权只由这些
  SubjectAccessReview Webhook 决定；平台授权器和 `authzBackends` 被忽略。
- 空列表 = 使用平台默认；旧对象行为不变。
- 只有 Agent 的调用入口（`/v1/agent/ask`、`/v1/chat/a2a`、
  `/v1/agent/agents/:name/invoke`）受这两组字段影响；控制台对 Agent 的增删改查
  仍走平台认证与授权。

## 五、在线测试

控制台的 Webhook 列表与详情页对认证 / 鉴权类型提供「测试」：

- 认证：填写 token（与可选 audiences），平台向**该 Webhook** 发一次 TokenReview。
- 鉴权：填写 user / groups / verb / resource / name / namespace / agent，平台向
  **该 Webhook** 发一次 SubjectAccessReview。
- 结果展示后端返回的 HTTP 状态、耗时、`status` 与原始响应体；平台侧的调用错误
  （不可达、超时、非 2xx、无法解析）单独展示。

测试只调用被测试的那一个后端，不经过平台的认证/授权链路，因此测出来的是后端
本身的判决。

## 六、失败语义速查

| 场景 | 认证 | 鉴权 |
|---|---|---|
| Webhook 返回拒绝 | 尝试下一个；全部拒绝 → 401 | Deny |
| 不可达 / 超时 / 非 2xx / 无法解析 | 尝试下一个；全部失败 → 401 | `failureMode=fail` → Deny；`ignore` → NoOpinion |
| 所有 Webhook 无意见 | —— | Deny（fail-closed） |
| 引用的 Webhook 被删除 / 停用 / 类型不符 | 401 | Deny（解析失败） |
| Agent 未配置该组 Webhook | 平台链 | 平台授权 |
