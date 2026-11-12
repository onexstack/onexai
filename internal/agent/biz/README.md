# Biz layer

`v1/` (and `v2/` when an incompatible change arrives) holds one directory per
resource, for example `v1/post` and `v1/user`. Versioning gives a breaking
change somewhere to live, and one directory per resource keeps a large layer
navigable.

## Rules

- A resource interface is named `XxxBiz`; the aggregate is `IBiz`. The
  constructor is `New`.
- Dependencies flow handler → biz → store. A write that must merge the stored
  row (an Update that fills fields the client did not send) belongs in biz;
  the validator checks the request as sent.
- Error mapping: `storeerr.GetError` for a by-key Get, `storeerr.Read` for a
  list, and `storeerr.Write` for a write. All three keep the cause.
- Pagination: a page/pageSize endpoint calls `pagination.Page`; a proto
  offset/limit endpoint calls `pagination.Where`. One resource must not mix the
  two shapes.
- Logging: request-scoped code with a `ctx` uses `slog.XxxContext`; startup
  code and background goroutines without a request context use `slog.Xxx`.
  `contextx.L(ctx)` is for callers that have already installed a
  request-scoped logger.
