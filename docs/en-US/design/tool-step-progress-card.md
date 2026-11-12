# Design: Tool-Step Progress Card

> Status: design draft (not implemented)
> Scope: `oai-chat-apiserver` streaming cards + `oai-agent-apiserver` streaming protocol
> Reference: `buildProgressCardJSONFromPayload` / `buildRichCard` / `toolDescriptors` in cc-connect `platform/feishu/feishu.go`

## 1. Background and goals

The current AI streaming card in `oai-chat-apiserver` (`biz/v1/bot/streaming_card.go` + `ai.go`) renders only **one text delta**: `runAI` writes the `delta.content` returned by `ChatCompletionsStream` incrementally into the card's `markdown_1` element, with no structured display of "reasoning / tool calls / tool results".

cc-connect's progress card splits an agent turn into multiple lanes: **Reasoning** (reasoning), **Tools** (tool calls), **Updates** (results), each tool carrying an icon + status color (blue = in progress / green = success / red = failure), with tool names displayed after redaction.

The goal of this design is: without breaking the existing streaming protocol, have `oai-agent-apiserver` **additionally emit structured "step" events** in the stream, so that `oai-chat-apiserver` can render tool-step progress in the streaming card. The text delta path stays unchanged.

## 2. Current state

### 2.1 agent streaming protocol (`pkg/api/agent/v1/chat_completion.proto`)

```proto
message ChatCompletionStreamResponse {
    string id = 1;
    string object = 2;
    int64 created = 3;
    string model = 4;
    repeated ChatCompletionStreamChoice choices = 5;  // 只含 delta.content（文本）
    string sessionID = 6;
}
```

`ChatCompletionStreamChoice.delta` has only `role` + `content`, with no structured steps.

### 2.2 chat streaming card

- `CreateStreamingCard` creates a card with cardkit `POST /cardkit/v1/cards`, containing elements such as `progress` / `main_text` / `reasoning` / `stop`.
- `UpdateCardText` / `UpdateCardReasoningText` / `UpdateCardProgressText` update incrementally with `element_id + sequence`.
- `runAI`'s streaming callback consumes only `delta.content`, incrementing `seq++` per chunk.

## 3. Design

### 3.1 ToolStep data model (new)

Placed in the shared API layer, so agent serializes and chat deserializes.

```proto
// 追加到 chat_completion.proto
enum ToolStepStatus {
    TOOL_STEP_STATUS_UNSPECIFIED = 0;
    TOOL_STEP_STATUS_RUNNING   = 1; // 进行中
    TOOL_STEP_STATUS_SUCCESS   = 2; // 成功
    TOOL_STEP_STATUS_ERROR     = 3; // 失败
}

message ToolStep {
    // id 唯一标识一个步骤，用于「更新同一工具状态」的去重/合并。
    string id = 1;
    // type 是步骤类型：thinking | tool_use | tool_result | error。
    string type = 2;
    // tool 是工具名（tool_use/tool_result 时有效），如 "read_file"。
    string tool = 3;
    // status 是步骤状态。
    ToolStepStatus status = 4;
    // summary 是面向用户的摘要（已脱敏），如 "读取文件 src/main.go"。
    string summary = 5;
}

// ChatCompletionStreamResponse 追加一个字段（向后兼容）：
message ChatCompletionStreamResponse {
    // ... 既有字段 1..6
    // step 是结构化的工具/推理步骤事件；纯文本流时为空。
    ToolStep step = 7;
}
```

Key points:
- **Backward compatible**: adding field `step=7`; old clients can simply ignore it, and the SSE format is not broken.
- **Text and steps orthogonal**: a chunk may have only `delta.content`, only `step`, or both; the agent interleaves them as needed.

### 3.2 agent protocol extension

`ChatCompletionsStream`'s callback, besides producing text deltas, additionally produces `step` events:

| Timing | Event | Description |
|---|---|---|
| agent starts thinking | `{type:thinking, status:RUNNING}` | Shows "reasoning" |
| tool starts | `{type:tool_use, tool:read_file, status:RUNNING}` | Shows the tool + a blue icon |
| tool completes | `{type:tool_result, tool:read_file, status:SUCCESS}` | Green |
| tool fails | `{type:tool_result, tool:read_file, status:ERROR, summary:"..."}` | Red |
| all done | existing `finishReason` | Closes the card |

In implementation, the agent's streaming handler maps the agent framework's (e.g. Claude Code / internal agent) `tool_use`/`tool_result` events into `ToolStep` and writes them to SSE. That mapping is outside the implementation scope of this design draft, but the protocol reserves it.

### 3.3 chat card rendering

1. `runAI`'s streaming callback becomes:
   - Receives `delta.content` → existing `UpdateCardText` (unchanged).
   - Receives `step` → calls the new `UpsertStep` (see below).
2. `streaming_card.go` adds a progress element, using cardkit's `UpdateCardElement` (or `element_id` granularity) to maintain a "step list":
   - Each step is deduplicated by `id` (new ids append, old ids update status).
   - Use `collapsible_panel` to group Reasoning/Tools/Updates (referencing cc-connect's three lanes).
   - Tool icons + status colors: blue (running) / green (success) / red (error).
3. New interface method (`LarkServices` + `botBiz`):
   ```go
   // UpsertStep 在流式卡片里新增或更新一个工具步骤（id 去重）。
   UpsertStep(ctx context.Context, cardID string, step *v1.ToolStep, seq int) error
   ```

### 3.4 sequencing / idempotency

- Keep cardkit's `sequence` monotonically increasing; step updates and text updates share the same `seq` counter (`seq++` already exists in `runAI`, and step updates use it too), guaranteeing ordering.
- `ToolStep.id` is generated by the agent (e.g. `tool_use:<index>` or uuid); chat upserts idempotently by id, preventing replays from duplicating steps.

## 4. Compatibility

- **Protocol**: new `step=7` field, proto3 backward compatible; old agents do not emit it and old chats ignore it.
- **Card**: with no `step` events the rendering is exactly the same as now (plain text card), not affecting non-agent models.
- **Degradation**: if the agent is not upgraded, chat renders no tool steps and displays only text — i.e. the current state.

## 5. Implementation steps (suggested order)

1. **P2-1a**: add `ToolStep` / `ToolStepStatus` + the `step` field to `chat_completion.proto`, regenerate `*.pb.go`.
2. **P2-1b**: the agent streaming handler emits `step` events (depends on the specific agent framework's tool event source).
3. **P2-1c**: chat `LarkServices` + `botBiz` add `UpsertStep`; `streaming_card.go` adds tool-step element rendering.
4. **P2-1d**: the `runAI` callback branches on `step`; add tests (tool_use→tool_result status transitions, id idempotency).

## 6. Risks and trade-offs

- **Card size**: many tool steps will fill up the card. Referencing cc-connect's progressive compression (per-lane caps 10/6/3), collapse when exceeded or keep only the latest N steps + a "+N steps" summary.
- **Redaction**: `ToolStep.summary` must be redacted by the agent; raw command text/keys must never pass through. cc-connect's `redactInlineSecrets` / `toolDescriptors` offer reusable ideas.
- **Timing races**: when text and steps interleave, both share one `sequence` to avoid out-of-order; the agent must guarantee event order within a turn.

## 7. Actual shape after implementation (supplement; not part of the plan draft above)

§3 above was the plan draft at the time; several things differ in the delivery. Code is authoritative: steps go through `AgentStreamEvent` (not the `step` field of `chat_completion.proto`), there are no "three lanes", and status colors are **five monochrome characters** rather than blue/green/red.

The elements produced by `buildStreamingCardJSON`, in order, are the order a turn unfolds:

| Order | `element_id` | Component | Description |
|---|---|---|---|
| 1 | `reasoning_1` (containing `reasoning_1_text`) | `collapsible_panel` | Title is fixed as "深度思考过程" (identical in live and finalized cards; no longer writes half-sentences like "深度思考中…"); appears only when reasoning is on for the conversation |
| 2 | `markdown_progress` | markdown | Tool-call panel (`▾ 工具调用` + one step per line) |
| 3 | `main_text` | markdown | Body text, written streaming |
| 4 | `decision_log` → `decision_row_<n>` / `decision_grant_<n>` | markdown / column_set | **Decision area, inserted before the stop row as needed** (see `human-in-the-loop.md` §6) |
| 5 | `stop_row` (containing `stop_button`) | column_set | Stop button, right-aligned, always at the bottom |

- **Characters**: `?` pending, `✓` completed/allowed, `✗` failed/denied, `○` not made (cancelled/expired/superseded, also used for a parked tool row), `⟳` in progress. Defined in `larkcard.Glyph*`, shared by cards and text messages; no more graphic symbols such as ✅/❌/⏳/⏹.
- **When paused**: the tool row changes from `⟳` to `○ 等待人工确认` and **stops the clock** (no longer shows growing elapsed time), the body placeholder is cleared, and the decision area appears on the same card.
- **When finalizing**: `finish()` rebuilds the entire card with `BuildRichCardWithDecisions` (the decision area must be passed in, otherwise it is wiped entirely); `config.update_multi: true` is a prerequisite for the element-level write APIs (`CardElement.Create/Delete/Settings`).
