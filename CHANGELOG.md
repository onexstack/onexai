# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Public `pkg/duty` contract with the Regen implementation in `pkg/duty/regen`.
- Bilingual documentation under `docs/en-US` and `docs/zh-CN`.

### Changed

- Breaking: the A2A agent card is served only at the origin-root
  `/.well-known/agent-card.json`; the former
  `/v1/chat/a2a/.well-known/agent-card.json` route was removed (no alias or
  redirect).
- The module path is `github.com/onexstack/onexai`.
- The private `harness` CLI is no longer part of this repository.
- Shared media, wshub, lock, and agent-client packages moved from `internal/`
  to `pkg/` so external clients can use them.
- Project license is Apache-2.0.
- Breaking: `namespace` is the only tenancy spelling on the management API; the
  pre-rename `workspace` field is gone.

### Removed

- Breaking: `Agent.sysPrompt` and the deprecated `workspace` fields on `Agent`,
  `AgentTemplate`, `Skill`, `MCP`, `KnowledgeBase`, `CustomCommand`, and
  `InvokeCustomCommandRequest`. Their proto field numbers are reserved.
- The real-time chatroom bounded context (`/v2` rooms and WebSocket) moved to the
  private harness project; `oai-chat-apiserver` no longer serves it.
- Internal deployment seeds and company-specific configuration from the public tree.
