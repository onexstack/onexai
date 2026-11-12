# Contributing

Thanks for your interest in onexai.

## Before you start

- Search existing issues and discussions; open an issue before a large change.
- Read [docs/en-US/development.md](docs/en-US/development.md) for the layout, build,
  codegen, and test conventions.

## Development workflow

1. Fork the repository and create a topic branch.
2. Keep commits focused and write a clear message (conventional commits are welcome).
3. Add tests for behavior changes.
4. Run the checks locally:

   ```bash
   make build
   CGO_ENABLED=0 go test -short ./internal/chat/duty/... ./pkg/duty/...
   make lint.defensive
   ```

5. Update the English and Chinese docs when a public behavior changes.
6. Open a pull request; describe the problem, the change, and how you verified it.

## Code conventions

- Comments and log messages are English.
- Keep the dependency direction `handler → biz → store`; public contracts belong in `pkg/`.
- Do not rename persisted CRD fields or database columns without a migration.
- Do not commit secrets, private endpoints, or generated binaries.

## License

By contributing you agree that your contributions are licensed under the Apache License
2.0.
