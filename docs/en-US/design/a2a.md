# A2A Protocol Surface

`oai-agent-apiserver` exposes agent capabilities to other services via [A2A](https://a2a-protocol.org).
This document records **what this layer does, what it deliberately does not do, and why**. Read it before changing anything.

## 1. Layering

| Location | Responsibility |
|---|---|
| `internal/agent/pkg/a2a` | Path constants, AgentCard, construction of the three transports, `Executor` (request mapping and event translation), identity interceptor |
| `internal/agent/handler/a2a.go` | gin bridging: route registration, AK/SK middleware, connection-deadline clearing, output-limit injection, inbound audit adaptation (`NewA2AGuard`) |
| `internal/agent/di/providers_a2a.go` | Wiring only: config → `Options` → `NewHandlers` |

The dependency direction is fixed as `handler → pkg/a2a → pkg/agent`. `Path`/`PathREST` are written once in `pkg/a2a`, and the transport routes, the prefix strip and the interface URLs the card advertises all derive from them — changing the mount point never leaves behind a card that still points at the old address. `PathCard` is deliberately independent: it aliases the SDK's `a2asrv.WellKnownAgentCardPath`, because discovery lives at the origin root per RFC 8615.

## 2. Mounting and Authentication

| Path | Method | Authentication |
|---|---|---|
| `/v1/chat/a2a` | POST | AK/SK signature |
| `/v1/chat/a2a/rest/*` | any | AK/SK signature |
| `/.well-known/agent-card.json` | GET | **public** |

The card must be public: a client has no credentials at all when it fetches the card. It therefore also **must not list internal agents** — that would be an asset catalog handed to anonymous readers. The existence of the protocol surface does not depend on a configuration switch: all three routes exist in every deployment; without an agent engine, every task ends with "the agent engine is not configured".

The card is served at the **origin root**, not under the transport prefix: a client fetches it before it knows any transport URL. The former `/v1/chat/a2a/.well-known/agent-card.json` location is removed — no alias and no redirect. A deployment behind a path-prefixed proxy must route the origin-root well-known path to this service; an origin can publish only one agent card.

> Implementation detail: gin middleware is inherited along a group, so **the card must not be registered in the `/v1` group** — a route written there would also require a signature, and clients could never fetch the card. The server mounts it directly on the router inside `InstallRESTAPI` (the same place as `/healthz` and `/media`), at the origin-root well-known path; the handler package only provides `A2ACard`.

## 3. Card Contract

- `skills` is an **empty array, not null**. The field has no `omitempty`; nil would serialize to `null`, and strict clients would fail to decode.
- Authentication uses `apiKey` (header `X-API-KEY`) plus a description stating the signature requirement. The SDK's `SecurityScheme` is a closed union (APIKey/HTTPAuth/mTLS/OAuth2/OIDC) and cannot express HMAC signing, so a separate extension `…/extensions/aksk-signature/v1` declares the signature headers and signing string:
  `METHOD \n PATH \n hex(md5(body)) \n X-TIMESTAMP \n X-NONCE`.
  A generic A2A client carrying only `X-API-KEY` gets 401.
- The `agent` / `agents` selection convention is declared by the extension `…/extensions/agent-selection/v1`.
- URL: `agent.a2a-public-url` is the **absolute base of the transport interface URLs**, not the card's own location; config validation rejects relative values. When left empty, the card carries a path-relative value (same-origin card fetches work; strict clients do not). It is **not derived from request headers by default**: the card route has no authentication, and forging `Host`/`X-Forwarded-Host` could point the peer somewhere else; forwarded headers are trusted only when `agent.a2a-trusted-proxy` is explicitly enabled, and in that case `a2a-public-url` must be empty (validation rejects setting both).
- `version` comes from the build-injected `version.Get().GitVersion`.

## 4. Identity

1. The AK/SK middleware puts the signer into the request context (`authz.SubjectFromService(apiKey)`).
2. The identity interceptor (`a2asrv.WithCallInterceptors`) writes it into the SDK's `CallContext.User` — this is the SDK's own identity channel, and task ownership checks read exactly this.
3. `Executor` takes `execCtx.User.Name` as the run's `Subject`; if missing it falls back to `authz.SubjectFromContext`, and only if that is also missing does it record `a2a-executor` and log a warning (this happens locally when authentication is turned off).

Known limitation: `biz`'s `subjectForRun` currently always constructs a **user** principal, so at runtime an A2A call appears as "a user named after that AK" and the service-account Kind is not preserved. Authorizing by service account would require `AskRequest` to be able to pass the principal through directly.

## 5. Sessions and Tasks

- **Session key = `contextId`** (falling back to `taskId`). A2A's "same conversation" means multiple tasks under the same context; using taskId as the key would lose memory when a new task continues the same `contextId`.
- **run id = `taskId`**, so `Cancel` can directly `Stop(runID)` (effective across replicas, provided Redis is configured). Multiple turns of the same task reuse the same run id: stop registration/deregistration is keyed by run id, so it is safe, but `run_id` repeats in the audit.

### Cluster mode (enabled automatically when Redis is configured)

All four pieces go into Redis; if any one is missing, it falls back to the SDK's in-process implementation:

| Component | Key | Purpose |
|---|---|---|
| `RedisTaskStore` | `<prefix>:agent:a2a:task:<id>` + per-user ZSET `:tasks:<user>` | The task itself and the list; survives restarts, readable by multiple replicas; ownership checks read `CallContext.User.Name` |
| `RedisEventQueue` | `<prefix>:agent:a2a:events:<id>` (Stream) | A task's **event stream**; resubscribe on another replica can replay from the beginning, 24h expiry |
| `RedisWorkQueue` | `<prefix>:agent:a2a:work` (Stream + consumer group) + `:lease:exec\|cancel:<id>` | Execution dispatch and leases; lease is 2 minutes, renewed by heartbeat; entries unacknowledged for 10 minutes are taken over by another replica |
| `CallContextCodec` | Serialized with the work item | Carries the caller (A2A user), authz subject, and output limit to the **executing replica**; otherwise the run there degrades to "the surface itself as caller" |

The event queue uses a Stream rather than Pub/Sub: Pub/Sub does not replay events published before subscription, and "fill in what was missed after disconnection" is the entire point of resubscribe. Replay is safe — the SDK's subscription first delivers a snapshot of the task from the store, then discards events whose version is not higher than the snapshot's, so reading from the beginning does not duplicate answers.

**Still impossible**: cross-replica *live* takeover. If an executing replica crashes, the task stays in the running state until the lease expires; what gets taken over is entries **queued but not executed** and resubscription, not a running goroutine. True crash takeover would require migrating work items by lease shard, which belongs to the next layer of design.

The SDK's degradation path is also worth knowing: `SubscribeToTask` returns **a single task snapshot** and then ends when it cannot obtain a subscription (`handler.go:371-383`). A client seeing a non-terminal snapshot should keep polling with `tasks/get` rather than treating connection close as task completion.

This degradation has been seen once: during two-replica integration testing, one resubscribe returned only a single WORKING snapshot (returned in 0.02s). It could not be reproduced in 4 subsequent live runs + 10 automated runs. To make the next diagnosis easier, there are now two debug lines: `a2a: subscribing to a task's events` (whether the reader is constructed) and `a2a: masking a task owned by someone else as not found` (whether it was blocked by ownership masking).

### Automated tests

`cluster_e2e_test.go` uses miniredis + two `NewHandlers` instances to simulate two replicas: a request breaks its stream on A, resubscribes from B and runs to a terminal state; and a task running on A is cancelled from B. Both run entirely in-process, depending on no external Redis/PG/model.

## 6. Event Mapping

| agent event | A2A representation |
|---|---|
| `EventAnswer` | The first chunk opens an artifact; subsequent chunks append to the **same artifactID** |
| `EventFailed` | `failed` + redacted reason (upstream errors only go to logs) |
| `EventStopped` | `canceled` |
| `EventSuspended` / `EventInterrupted` | `failed` + "needs a human approval, which this surface cannot ask for" |
| `EventToolStep`/`EventProgress`/`EventReasoning`/`EventImage`/`EventSources`/`EventUsage`/`EventDone` | dropped |

Dropping is a decision, not an omission: tool steps and reasoning have no representation in A2A; images are references on this site (asset id or a URL needing credentials), and sending them to a peer amounts to handing over an image it cannot fetch.

Input accepts text and **image URLs** (`image/png`, `image/jpeg`, `image/webp`); images are converted into `media.Ref{URL, MimeType}` and passed to the run — this is the repo-wide "pass references, not bytes" form. The card's `defaultInputModes` declares these four truthfully.

Inline bytes (raw part) and structured data parts **fail explicitly** rather than silently taking only the text: the former would mean passing images through every layer that holds a message, and the latter is a capability this surface does not have.

## 7. Edge Policies

A2A and the chat surface share two edge policies, wired in `handler/a2a.go`:

- **Inbound audit**: `NewA2AGuard` connects the adapter's `Guard` port to `screenInput`, using the same screener, the same metric counter, and the same interaction record as the chat surface. Rejected input costs no model call.
- **Output limit**: the bridging layer uses `h.askContext(c)` to attach the limit resolved from the caller's AK to the request context.
- **Connection deadline**: the bridging layer first calls `detachConnDeadlines`; otherwise the server's absolute write deadline would sever streaming answers.
- Observability skip rules must cover the `/v1/chat/a2a/` prefix (including the `/rest` subtree), otherwise REST streams get wrapped by the body-capturing writer. The card at the origin root is intentionally **not** skipped: it is a small public GET with no request body and no credential.

## 8. Configuration

```yaml
agent:
  a2a-public-url: ""      # 两个传输接口发布的绝对基地址（不是卡片位置）；空 = 相对路径
  a2a-trusted-proxy: false
  a2a-default-agent: ""   # 请求未指定 agent 时的默认值
  a2a-description: ""     # 卡片描述；空则回落到 agent.description
```

The repository config (`configs/examples/oai-agent-apiserver.example.yaml`) and the local config (`~/.onexai/oai-agent-apiserver*.yaml`) must be updated together.
