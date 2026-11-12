# Vendored: github.com/polarismesh/grpc-go-polaris

Upstream version: **v1.5.0**. Only the root package is vendored — nothing here
imports the module's `examples/`, `test/`, `doc/` or `ci/` directories, so those
were dropped.

`go.mod` replaces the upstream module with this directory:

```
replace github.com/polarismesh/grpc-go-polaris => ./third_party/grpc-go-polaris
```

## Why this version

v1.13.1 (the latest release) is not usable: it dropped `NewServer`, and
`github.com/onexstack/onexstack@v1.0.4/pkg/server/polaris_grpc_server.go` — the
only consumer, and a module we do not control — calls `polarisgrpc.NewServer`,
`srv.Serve` and `srv.GracefulStop`. v1.5.0 is the version onexstack was written
against, so patching it keeps the local delta to a single added method.

## Why patch it at all

`google.golang.org/grpc` added `ExitIdle()` to the `balancer.Balancer` interface
after v1.69, and no released version of grpc-go-polaris implements it. The
module is reached through `onexstack/pkg/server`, which we need for the HTTP/gRPC
server helpers, and `polaris_grpc_server.go` lives in that same package — so the
module must compile, whether or not polaris is used at runtime.

Pinning grpc back below the `ExitIdle` change is not an option either:
`go.etcd.io/etcd/client/v3@v3.6.15` (pulled in by `onexstack/pkg/server` via the
agent server options) calls the `resolver/manual.Resolver.CC()` *method*, which
only exists from grpc v1.78 onwards, while v1.69 and below expose `CC` as a
field. There is no grpc version that satisfies both, so the module is patched.

## The delta

Exactly one change: `ExitIdle()` added to `*polarisNamingBalancer` in
`balancer.go`, marked `[LOCAL PATCH]`. Everything else is byte-identical to
upstream v1.5.0.

The method is a deliberate no-op. This balancer never leaves a SubConn in the
idle state — `createSubConnection` calls `sc.Connect()` immediately after
`NewSubConn`, and `UpdateSubConnState` calls `sc.Connect()` on every transition
back to `connectivity.Idle`:

```go
case connectivity.Idle:
	sc.Connect()
```

## Upstream fix

If a future grpc-go-polaris release both implements `ExitIdle` and keeps the
`NewServer`/`Serve`/`GracefulStop` API that onexstack calls, drop this directory
and the `replace` directive in favour of the upstream module.
