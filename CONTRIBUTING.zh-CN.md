# 贡献指南

感谢你对 onexai 的关注。

## 开始之前

- 先搜索已有 issue 与讨论；较大的改动请先开 issue。
- 阅读 [docs/zh-CN/development.md](docs/zh-CN/development.md) 了解目录、构建、代码生成与测试约定。

## 开发流程

1. Fork 仓库并创建主题分支。
2. 保持提交聚焦，写清提交信息（欢迎使用 conventional commits）。
3. 行为变更补测试。
4. 本地执行检查：

   ```bash
   make build
   CGO_ENABLED=0 go test -short ./internal/chat/duty/... ./pkg/duty/...
   make lint.defensive
   ```

5. 公共行为变更时同步更新中英文文档。
6. 提交 PR，说明问题、改动与验证方式。

## 代码约定

- 注释与日志使用英文。
- 保持 `handler → biz → store` 的依赖方向；公共契约放在 `pkg/`。
- 没有迁移就不要重命名持久化 CRD 字段或数据库列。
- 不要提交密钥、内部端点或构建产物。

## 许可证

提交即表示你同意以 Apache License 2.0 授权你的贡献。
