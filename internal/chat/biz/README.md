# Biz layer

`v1/` (and `v2/` when an incompatible change arrives) holds one directory per
resource, for example `v1/post` and `v1/user`. Versioning gives a breaking
change somewhere to live, and one directory per resource keeps a large layer
navigable.

## Rules

- A resource interface is named `XxxBiz`; the aggregate is `IBiz`. The
  constructor is `New`.
- Dependencies flow handler → biz → store. The handler does not read the store
  directly.
- Error mapping: `storeerr.GetError` for a by-key Get, `storeerr.Read` for a
  list, and `storeerr.Write` for a write. All three keep the cause.
- Pagination: a page/pageSize endpoint calls `pagination.Page`; a proto
  offset/limit endpoint calls `pagination.Where`; a bounded `limit` on a
  history/search read is clamped in biz. One resource must not mix shapes.
- Logging: request-scoped code with a `ctx` uses `slog.XxxContext`; startup
  code and background goroutines without a request context use `slog.Xxx`.
  `contextx.L(ctx)` is for callers that have already installed a
  request-scoped logger.
