# External A2A Agents

This document records how onexai **registers, discovers and calls third-party A2A agents**, and why it works this way.
The inbound protocol surface (onexai as an A2A server) is covered in [a2a.md](a2a.md); this file is outbound only.

## 1. Positioning

A single `Agent` has two orthogonal dimensions:

| Dimension | Values | Meaning |
|---|---|---|
| `spec.kind` | `native` / `external` | Behaviour defined by the platform, or registered from a third party |
| `spec.execution.mode` | `inproc` / `a2a` | Execute in this process, or execute remotely over A2A |

Three combinations are legal: a platform agent (`native + inproc`), a platform agent running on an
A2A runtime (`native + a2a`), and a registered third-party A2A agent (`external + a2a`).
`external + inproc` is invalid: an external agent has no platform prompt, model or tools.

An external agent **is not a new CRD**; it is one shape of the same `Agent` resource. Catalog,
authorization, Chat, `calls` and references therefore have one model instead of two that drift apart.

Platform behaviour fields (prompt, model, skills/MCPs/knowledge bases, approval, …) are rejected for
external agents by validation. `kind` is immutable after creation.

## 2. Registration and Card Discovery

Three sources:

- `card-url`: the address of an AgentCard;
- `json`: the card JSON itself;
- `manual`: endpoint, protocol version and transport entered by hand.

`POST /v1/agent/agents/preview` fetches and normalizes a card **without persisting it**; failure is
`ok=false`. The registration wizard uses it for live preview.

The controller (`internal/controller/agent/agent`) fetches the card and writes `status.a2a`:

- discovery order: explicit `cardUrl` → origin-root `/.well-known/agent-card.json` → legacy
  `agent.json` → well-known under the endpoint;
- both A2A **0.3** (`message/send|stream`, `tasks/get|cancel`) and **1.0**
  (`SendMessage`, `SendStreamingMessage`, `GetTask`, `CancelTask`) are understood; the business
  layer only consumes normalized DTOs;
- protocol version, transport, resolved absolute endpoint, capabilities, skills, card digest and
  version history are recorded;
- a failed refresh keeps the last good card, records `lastSyncError` and degrades the phase instead
  of clearing the status;
- relative interface URLs in a card (e.g. `/v1/chat/a2a`) are resolved to absolute addresses.

Every outbound request goes through `internal/pkg/netguard`: private addresses are refused unless the
deployment explicitly allows them.

## 3. Outbound Call Path

| Location | Responsibility |
|---|---|
| `internal/agent/pkg/a2aclient` | SDK-free JSON-RPC client; 0.3/1.0 dual stack; SSE parsing |
| `internal/agent/pkg/a2ainvoke` | Orchestrates one call: transport, sync/stream, poll to terminal, cancel, invocation record |
| `internal/agent/pkg/a2atool` | The `a2a_invoke` tool used by `calls` |
| `internal/agent/biz/v1/agent/resource.go` | Management invoke API |
| `internal/agent/biz/v1/agent/external.go` | Chat directly against an external agent |

`a2ainvoke.Service` is the single implementation shared by all three entry points (management invoke,
the `a2a_invoke` tool, Chat): same credential resolution, same timeout/polling, same records. Three
copies would be where they start disagreeing.

A synchronous call polls non-terminal states (`submitted`/`working`) to a terminal one, but **does
not poll** `input-required` / `auth-required`: those wait for the caller, not for time.

Timeout precedence: an explicit caller value, then the agent's
`execution.a2a.timeoutSeconds`, then the service default (120s). It is implemented once in
`a2ainvoke.callTimeout`, so the debug panel, the `a2a_invoke` tool and Chat cannot give the same
agent different deadlines. Chat also passes the run's deadline as the explicit value.

## 4. Authentication and Credentials

`none` / `apiKey` / `bearer` / custom headers / OAuth2 client credentials / `aksk` (onexai's own
signature format).

- Credentials live in the `<name>-a2a-credentials` Secret and are never projected back by reads;
- `clearCredentials` clears, changing the auth type replaces the whole secret, empty credentials are
  left untouched;
- `aksk` signs every request: `X-API-KEY / X-TIMESTAMP / X-NONCE / X-SIGN`, with method, path, body
  MD5, timestamp and nonce in the signing string;
- card fetching and runtime invocation share the same credential resolution, so "the fetch worked
  but the call cannot authenticate" is not a state this design can reach.

## 5. Invocation Records

Every outbound call writes one `agent.a2a_invocation` row:

- `source`: `console` / `chat` / `agent-tool`;
- caller identity, parent run, taskId, contextId, state, latency, artifacts;
- input and output are masked with `datamask` and length-capped per field.

The record is the only answer to "who made which peer do what", and the data source behind the Chat
and debug views.

## 6. Chat Direct

When Chat selects an external agent, the turn **does not go through the engine**: after admission,
`AgentBiz.Ask` hands the request to `a2ainvoke.Stream` and the peer executes it.

- Only `len(agents) == 1` and an `external` agent take this path; the platform-agent path is unchanged;
- composition (test mode) or a multi-agent selection is refused: the peer has no platform
  capabilities to narrow and there is no second peer to merge;
- `contextId` is the session ID, so a peer that supports context can continue the conversation;
- only text is sent; Chat hides image/attachment entries and the debug console for external agents;
- `delta` / `artifact` text becomes `answer` increments, the terminal state becomes `done`; when a
  peer answers with a whole task snapshot, that text becomes the answer too;
- `input-required` / `auth-required` are shown as the turn's answer and end it: Chat has no engine
  checkpoint to resume, so continuation belongs to the detail page's debug view or invocation records;
- admission, quota, the concurrency lease, the run row, session persistence and metering are the same
  as for a platform agent; the run's runtime is recorded as `a2a` and the invocation `source=chat`;
- stop cancels only the outbound call, never the run's context: the turn ends as `stopped`, the run
  row settles as `canceled`, and the invocation is recorded as `canceled` — the same shape as the
  engine's safe-point stop;
- if `a2ainvoke` is not wired the request fails explicitly and never falls back to the engine — that
  would answer as the deployment assistant wearing the selected agent's name.

## 7. `calls`

`Agent.spec.calls` may reference any Agent. The controller resolves it into `status.resolved.calls`:

- native targets join `Delegates` and use the existing `agent_invoke_agent`;
- external targets feed the `a2a_invoke` dispatch tool with `agent` (an enum of usable external
  agents) / `message` / `context_id` / `task_id`;
- the target must exist, be enabled, have a synced card when external, and be reachable across
  namespaces via `sharing`.

## 8. Frontend Surfaces

- `/agents`: one catalog with `All / Platform / A2A` tabs and a type column;
- `/agents/new?type=external`: registration wizard with four sources, auth and live card preview;
- Agent detail: `AgentCard` and `Skills` tabs, `Debug` (streaming invoke, `input-required`
  continuation) and `Invocation records`;
- Chat: external agents are selectable with an `(A2A)` marker; test mode, the debug console, images
  and attachments are hidden for them;
- `/a2a-agents` redirects to `/agents?tab=external`.

## 9. APIs

| Method | Path | Purpose |
|---|---|---|
| POST | `/v1/agent/agents` | Create (including external) |
| POST | `/v1/agent/agents/preview` | Fetch and preview a card, no persistence |
| POST | `/v1/agent/agents/:name/invoke` | Synchronous call |
| POST | `/v1/agent/agents/:name/invoke/stream` | Streaming call (SSE) |
| GET | `/v1/agent/agents/:name/tasks/:taskId` | Remote task snapshot |
| POST | `/v1/agent/agents/:name/tasks/:taskId/cancel` | Cancel the remote task |
| GET | `/v1/agent/agents/:name/invocations` | Invocation records |
| GET | `/v1/agent/agents/:name/invocations/:invocationId` | One invocation record |

## 10. Boundaries and Known Limits

- An external agent's capabilities, model and tools belong entirely to the peer; the platform neither
  proxies nor injects any;
- Chat sends text only — no images, attachments, `calls` or platform capabilities;
- Chat does not resume `input-required`; continue in the detail page's debug panel;
- outbound networking is bounded by `netguard`; reaching private peers requires an explicit
  deployment opt-in;
- one external agent maps to one card and one endpoint; a 1.0 card with several interfaces uses the
  declared primary one.
