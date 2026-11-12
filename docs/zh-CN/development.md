# 开发

## 目录

```text
cmd/<binary>/        精简 main 包
internal/<service>/  handler、biz、store、model、wire
pkg/                 公共契约与共享客户端
pkg/apis/            CRD 类型（agent、chat）
pkg/generated/       代码生成产物（不要手改）
```

依赖方向是 `handler → biz → store`；`biz` 声明接口，`store` 实现。新代码应复用
onexstack 的辅助包（`pkg/store`、`pkg/errorsx`、`pkg/options`、`pkg/server`、`pkg/ptr`），
不要重复造轮子。

## 构建与测试

```bash
make build
CGO_ENABLED=0 go test -short ./...   # macOS/arm64：CGO 会触发 go-m1cpu 崩溃
go test -v -run TestName ./internal/chat/duty
```

不要在笔记本上跑 `go test ./...`：部分包需要 SQLite/CGO 或真实 Kubernetes 集群。请按包
指定测试目标。

## 代码生成

```bash
make generate      # go:generate
make protoc        # protobuf、gRPC、OpenAPI、defaults
make gen-k8s       # deepcopy/conversion/client/informer
```

生成文件会入库。修改 `*.proto` 后运行 `make protoc`（它还会执行
`protoc-go-inject-tag`；直接跑 `protoc` 会丢掉 `form:`/`uri:` tag）。

## Lint 与格式化

```bash
make lint.defensive   # 合并门禁
make format           # goimports
```

`.golangci.yaml` 刻意保持宽松，仅作参考；仓库历史早于全量合规，它不是合并门禁。

## 约定

- 代码注释使用英文。
- 错误带上下文包装；日志使用 `slog` 结构化字段。
- 资源名与 CRD 字段是稳定 API：没有迁移就不要重命名持久化字段。
- 声明式变更先落到控制面类型与控制器，再让运行时依赖它。

## Pull Request

保持改动聚焦，行为变更补测试；新增或修改公共行为时同步更新中英文文档。
