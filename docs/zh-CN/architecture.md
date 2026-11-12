# 架构

onexai 由一组围绕声明式控制面协作的服务组成。

## 控制面

`oai-declserver` 是 Kubernetes 风格的聚合 API Server，后端为 etcd。它对外提供
`pkg/apis/{agent,chat}` 中的 CRD，通过 kubeconfig（`controlplane.kubeconfig`）访问。
`oai-controller-manager` 运行控制器，负责校验对象、解析引用并写回状态。

运行时服务不保存这些资源的第二份副本。Agent、Prompt、Skill、MCP Server、命令授权与
PeerBinding 都通过 informer 缓存从控制面读取。值班组例外：它是 chat 服务自有的命令式资源，
组元数据、排班与升级策略存放在 PostgreSQL 的 `chat.oncall_*` 表。BotApp 是已废弃的声明式对象，
chat 的 Lark 应用配置来自服务配置而不是控制面；DutyGroup、BotApp、ScheduledRun 仅保留 API
以便读取历史数据或迁移。

## 数据面

| 存储 | 用途 |
|---|---|
| PostgreSQL / MySQL（`coredb`） | 会话、消息、工单、任务、MCP 行、审计数据 |
| etcd | `oai-declserver` 提供的声明式对象 |
| Kafka（`kafka`） | `oai-mqserver` 消费的异步事件流 |
| Redis（可选） | 跨 Pod 会话与限流状态 |
| 对象存储（`oss`） | 资产、Skill 归档、任务产物 |

PostgreSQL 使用三个 schema（`agent`、`chat`、`job`）；MySQL 使用带服务前缀的表名。
通过 `coredb.dialect` 选择。

## 请求路径

- **OpenAI 客户端**调用 `oai-agent-apiserver`（`/v1/chat/completions`、`/v1/agent/agents`、
  `/v1/agent/skills`、`/v1/agent/mcpservers`、`/v1/agent/knowledgebases`）。
- **聊天用户**通过飞书/Lark 调用 `oai-chat-apiserver`；服务把消息转换成命令或 Agent
  turn，流式输出进度，并写入会话/消息。
- **长任务**以 job 或 Kafka 事件提交，由 `oai-jobserver` / `oai-mqserver` 消费。
- **工具**由 `oai-mcpserver` 暴露，Agent 通过 MCP 调用。

## 依赖方向

```text
cmd/<binary> → internal/<service>/{handler,biz,store} → pkg/*
```

`handler` 适配 HTTP/MCP/IM 传输，`biz` 承载领域逻辑与接口，`store` 实现持久化，
`wire` 组装依赖。公共契约放在 `pkg/`，外部客户端（包括私有 harness CLI）无需导入
`internal/` 即可使用。

## 可选集成

所有外部平台都是接口后的适配器，未配置即关闭：`pkg/duty`（Regen）、Zadig、CMDB、
Prometheus、RAGFlow 与用量上报。一个都不配置时，
网关、聊天机器人与控制面仍可正常运行。
