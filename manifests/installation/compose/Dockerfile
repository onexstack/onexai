# syntax=docker/dockerfile:1.7

# One Dockerfile builds every onexai Go service. Select the binary with
# --build-arg SERVICE=<cmd directory name>. Compose passes it per service, so a
# single build context serves the whole stack.
#
# The runtime stage is Debian rather than distroless because the compose
# healthchecks use curl. Release images that do not need a shell can keep using
# build/docker/<service>/Dockerfile.runtime-only.

# Base images are arguments so a restricted network can point them at a
# registry mirror without editing this file. The defaults are the upstream
# images.
ARG GO_IMAGE=golang:1.27.0
ARG RUNTIME_IMAGE=debian:bookworm-slim

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build

ARG TARGETOS
ARG TARGETARCH
ARG SERVICE
ARG GOPROXY

ENV CGO_ENABLED=0
ENV GOPROXY=${GOPROXY}
WORKDIR /src

COPY go.mod go.sum ./
# go.mod replaces two modules with directories under third_party; they must be
# present before `go mod download` resolves the module graph.
COPY third_party ./third_party
# The working tree replaces onexstack with ../onexstack, which is outside the
# build context. The only local change there is a log-noise suppression, and
# go.mod already requires v1.0.8, so the image builds against the released
# module. Drop the replace only inside the build.
RUN go mod edit -dropreplace=github.com/onexstack/onexstack
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# COPY above restores the repository go.mod, so drop the outside-context replace
# again before building.
RUN go mod edit -dropreplace=github.com/onexstack/onexstack

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false -ldflags "-s -w" \
      -o /out/service ./cmd/${SERVICE}

FROM ${RUNTIME_IMAGE} AS runtime

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl tzdata \
 && rm -rf /var/lib/apt/lists/*

RUN groupadd -g 65532 noroot 2>/dev/null || true \
 && useradd -u 65532 -g 65532 -M -d /data/home -s /usr/sbin/nologin noroot 2>/dev/null || true

COPY --from=build /out/service /app/service

# A writable HOME is required: the services use $HOME/.onexai for their local
# files. Compose mounts a named volume at /data so this survives a restart and
# the read-only root filesystem stays usable.
ENV HOME=/data/home

# Several binaries link both go.etcd.io/etcd/api/v3/authpb and the Polarismesh
# specification, which each register a protobuf file named auth.proto. Neither
# auth.proto is used by these processes; warn and ignore the duplicate instead
# of panicking in init. See protobuf.dev/reference/go/faq#namespace-conflict.
ENV GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn

RUN mkdir -p /data/home /data/media /data/sessions /data/skills /workspace /var/lib/onexai \
 && chown -R 65532:65532 /data /workspace /var/lib/onexai

USER 65532:65532

ENTRYPOINT ["/app/service"]
