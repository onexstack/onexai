# 变更日志

本文件记录项目的重要变更。格式遵循
[Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本遵循
[语义化版本](https://semver.org/lang/zh-CN/)。

## [未发布]

### 新增

- 公共 `pkg/duty` 契约，Regen 实现位于 `pkg/duty/regen`。
- `docs/en-US` 与 `docs/zh-CN` 双语文档。

### 变更

- 破坏性变更：A2A 卡片只发布在源站根路径 `/.well-known/agent-card.json`；
  原 `/v1/chat/a2a/.well-known/agent-card.json` 路由已移除（不提供别名或重定向）。
- module 路径改为 `github.com/onexstack/onexai`。
- 私有 `harness` CLI 不再属于本仓库。
- media、wshub、lock、agent client 等共享包从 `internal/` 提升到 `pkg/`，
  供外部客户端使用。
- 项目许可证改为 Apache-2.0。
- 破坏性变更：管理面只保留 `namespace` 这一种命名空间拼写，改名前的
  `workspace` 字段已移除。

### 移除

- 破坏性变更：`Agent.sysPrompt`，以及 `Agent`、`AgentTemplate`、`Skill`、
  `MCP`、`KnowledgeBase`、`CustomCommand`、`InvokeCustomCommandRequest` 上
  已弃用的 `workspace` 字段。它们的 proto 字段号已 `reserved`。
- 实时 chatroom 有界上下文（`/v2` 房间与 WebSocket）迁移到私有 harness 项目；
  `oai-chat-apiserver` 不再提供该能力。
- 公开树中的内部部署种子与公司特定配置。
