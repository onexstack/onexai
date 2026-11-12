# Development

## Layout

```text
cmd/<binary>/        thin main package
internal/<service>/  handler, biz, store, model, wire
pkg/                 public contracts and shared clients
pkg/apis/            CRD types (agent, chat)
pkg/generated/       codegen output (do not edit)
```

The dependency direction is `handler → biz → store`; `biz` declares the interfaces the
store implements. New code should reuse onexstack helpers (`pkg/store`, `pkg/errorsx`,
`pkg/options`, `pkg/server`, `pkg/ptr`) instead of re-implementing them.

## Build and test

```bash
make build
CGO_ENABLED=0 go test -short ./...   # macOS/arm64: CGO triggers a go-m1cpu crash
go test -v -run TestName ./internal/chat/duty
```

Do not run `go test ./...` on a laptop: a few packages need SQLite/CGO or a live
Kubernetes cluster. Use package-scoped targets.

## Code generation

```bash
make generate      # go:generate directives
make protoc        # protobuf, gRPC, OpenAPI, defaults
make gen-k8s       # deepcopy/conversion/client/informer code
```

Generated files are checked in. After changing a `*.proto`, run `make protoc` (it also
runs `protoc-go-inject-tag`; running raw `protoc` drops `form:`/`uri:` tags).

## Lint and formatting

```bash
make lint.defensive   # the merge gate
make format           # goimports
```

`.golangci.yaml` is intentionally broad and informational; it is not a merge gate
because the repository predates full compliance.

## Conventions

- Code comments are English.
- Errors are wrapped with context; logs use `slog` with structured attributes.
- Resource names and CRD fields are stable APIs: do not rename a persisted field without
  a migration.
- Declarative changes must land in the control-plane types and the controller before the
  runtime starts depending on them.

## Pull requests

Keep changes focused, add tests for behavior changes, and update the bilingual docs when
you add or change a public behavior.
