# Vendored: github.com/feizaonet/cobrax

Upstream version: **v0.0.1** (the only release). The whole module is vendored
with its `*_test.go` files, `docs/`, `examples/`, `test/` and `makefile`
dropped — nothing in this repository runs them.

`go.mod` replaces the upstream module with this directory:

```
replace github.com/feizaonet/cobrax => ./third_party/cobrax
```

## Why patch it at all

`redirect_unix.go` redirected stdio with `syscall.Dup2`. That symbol does not
exist on linux/arm64 (the platform exposes `dup3` only), so `make
build.multiarch` failed on `linux_arm64` and `windows_*` never got to build:

```
../../../../pkg/mod/github.com/feizaonet/cobrax@v0.0.1/redirect_unix.go:26:20: undefined: syscall.Dup2
```

The only delta is that file: it now uses `golang.org/x/sys/unix` `Dup` /
`Dup2` / `Close`, which shims `Dup2` onto `dup3` where the kernel lacks it.
Behaviour on darwin and linux/amd64 is unchanged (`dup3` with `flags == 0` is
`dup2`).

Drop this fork once upstream ships a release with the same change.
