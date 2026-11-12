# 设计：工具步骤进度卡片（Tool-Step Progress Card）

> 状态：设计稿（未实现）
> 范围：`oai-chat-apiserver` 流式卡片 + `oai-agent-apiserver` 流式协议
> 参考：cc-connect `platform/feishu/feishu.go` 的 `buildProgressCardJSONFromPayload` / `buildRichCard` / `toolDescriptors`

## 1. 背景与目标

当前 `oai-chat-apiserver` 的 AI 流式卡片（`biz/v1/bot/streaming_card.go` + `ai.go`）只渲染**一个文本增量**：`runAI` 把 `ChatCompletionsStream` 返回的 `delta.content` 逐步写进卡片的 `markdown_1` 元素，没有「推理过程 / 工具调用 / 工具结果」的结构化展示。

cc-connect 的进度卡片把一次 agent turn 拆成多 lane：**Reasoning**（推理）、**Tools**（工具调用）、**Updates**（结果），每个工具带图标 + 状态色（蓝=进行中 / 绿=成功 / 红=失败），并把工具名脱敏后展示。

本设计的目标是：在不破坏现有流式协议的前提下，让 `oai-agent-apiserver` 在流式输出中**额外下发结构化「步骤」事件**，`oai-chat-apiserver` 据此在流式卡片里渲染工具步骤进度。文本增量路径保持不变。

## 2. 现状

### 2.1 agent 流式协议（`pkg/api/agent/v1/chat_completion.proto`）

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

`ChatCompletionStreamChoice.delta` 只有 `role` + `content`，没有结构化步骤。

### 2.2 chat 流式卡片

- `CreateStreamingCard` 用 cardkit `POST /cardkit/v1/cards` 建卡，含 `progress` / `main_text` / `reasoning` / `stop` 等元素。
- `UpdateCardText` / `UpdateCardReasoningText` / `UpdateCardProgressText` 用 `element_id + sequence` 增量更新。
- `runAI` 的流式回调只消费 `delta.content`，`seq++` 每块递增。

## 3. 方案设计

### 3.1 ToolStep 数据模型（新增）

放在共享 API 层，让 agent 序列化、chat 反序列化。

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

要点：
- **向后兼容**：新增字段 `step=7`，旧客户端忽略即可，不破坏 SSE 格式。
- **文本与步骤正交**：一个 chunk 可以只有 `delta.content`，也可以只有 `step`，也可以两者都有；agent 按需混排。

### 3.2 agent 协议扩展

`ChatCompletionsStream` 的回调在产生文本 delta 之外，额外产生 `step` 事件：

| 时机 | 事件 | 说明 |
|---|---|---|
| agent 开始思考 | `{type:thinking, status:RUNNING}` | 显示「推理中」 |
| 工具开始 | `{type:tool_use, tool:read_file, status:RUNNING}` | 显示工具 + 蓝色图标 |
| 工具完成 | `{type:tool_result, tool:read_file, status:SUCCESS}` | 绿色 |
| 工具失败 | `{type:tool_result, tool:read_file, status:ERROR, summary:"..."}` | 红色 |
| 全部完成 | 既有 `finishReason` | 关闭卡片 |

实现上在 agent 的 streaming handler 中，把 agent 框架（如 Claude Code / 内部 agent）的 `tool_use`/`tool_result` 事件映射为 `ToolStep` 后写入 SSE。该映射不属于本设计稿的实现范围，但协议已预留。

### 3.3 chat 卡片渲染

1. `runAI` 的流式回调改为：
   - 收到 `delta.content` → 现有 `UpdateCardText`（不变）。
   - 收到 `step` → 调用新增 `UpsertStep`（见下）。
2. `streaming_card.go` 新增一个进度元素，用 cardkit 的 `UpdateCardElement`（或 `element_id` 粒度）维护「步骤列表」：
   - 每个 step 按 `id` 去重（新 id 追加，旧 id 更新状态）。
   - 用 `collapsible_panel` 分组 Reasoning/Tools/Updates（参考 cc-connect 的三 lane）。
   - 工具图标 + 状态色：蓝（running）/ 绿（success）/ 红（error）。
3. 新增接口方法（`LarkServices` + `botBiz`）：
   ```go
   // UpsertStep 在流式卡片里新增或更新一个工具步骤（id 去重）。
   UpsertStep(ctx context.Context, cardID string, step *v1.ToolStep, seq int) error
   ```

### 3.4 序列 / 幂等

- 沿用 cardkit 的 `sequence` 单调递增，步骤更新与文本更新共用同一 `seq` 计数（`runAI` 中 `seq++` 已存在，步骤更新也走它），保证时序。
- `ToolStep.id` 由 agent 生成（如 `tool_use:<index>` 或 uuid），chat 按 id 幂等 upsert，避免重放导致重复步骤。

## 4. 兼容性

- **协议**：新增 `step=7` 字段，proto3 向后兼容，旧 agent 不发该字段、旧 chat 忽略该字段。
- **卡片**：无 `step` 事件时渲染与现在完全一致（纯文本卡片），不影响非 agent 模型。
- **降级**：若 agent 未升级，chat 不渲染工具步骤，只显示文本——即现状。

## 5. 实施步骤（建议顺序）

1. **P2-1a**：`chat_completion.proto` 新增 `ToolStep` / `ToolStepStatus` + `step` 字段，重新生成 `*.pb.go`。
2. **P2-1b**：agent 流式 handler 产出 `step` 事件（依赖具体 agent 框架的 tool 事件源）。
3. **P2-1c**：chat `LarkServices` + `botBiz` 新增 `UpsertStep`；`streaming_card.go` 增加工具步骤元素渲染。
4. **P2-1d**：`runAI` 回调分支消费 `step`；补测试（tool_use→tool_result 状态流转、id 幂等）。

## 6. 风险与权衡

- **卡片体积**：大量工具步骤会撑满卡片。参考 cc-connect 的逐级压缩（单 lane 上限 10/6/3），超出时折叠或只保留最近 N 步 + 「+N 步」摘要。
- **脱敏**：`ToolStep.summary` 必须由 agent 脱敏（redact），不得透传命令原文/密钥。cc-connect 的 `redactInlineSecrets` / `toolDescriptors` 可复用思路。
- **时序竞态**：文本与步骤混排时，两者共用一个 `sequence` 避免乱序；agent 需保证同一 turn 内事件顺序。

## 7. 实现后的实际形态（补充，非本节之前的计划稿）

上面的 §3 是当时的计划稿，落地时几处不一样，以代码为准：步骤走的是 `AgentStreamEvent`（不是 `chat_completion.proto` 的 `step` 字段），没有"三 lane"，状态色是**五个单色字符**而不是蓝/绿/红。

`buildStreamingCardJSON` 产出的元素，顺次就是 turn 展开的顺序：

| 顺序 | `element_id` | 组件 | 说明 |
|---|---|---|---|
| 1 | `reasoning_1`（内含 `reasoning_1_text`） | `collapsible_panel` | 标题固定「深度思考过程」（live 与收尾后的卡片一致，不再写「深度思考中…」这种说了一半的话）；只在会话开着推理时出现 |
| 2 | `markdown_progress` | markdown | 工具调用面板（`▾ 工具调用` + 每行一个步骤） |
| 3 | `main_text` | markdown | 正文，流式写入 |
| 4 | `decision_log` → `decision_row_<n>` / `decision_grant_<n>` | markdown / column_set | **决策区，按需插入在停止行之前**（见 `human-in-the-loop.md` §6） |
| 5 | `stop_row`（内含 `stop_button`） | column_set | 停止按钮，右对齐、始终在最底 |

- **字符**：`?` 待决、`✓` 已完成/已允许、`✗` 失败/已拒绝、`○` 没有作出（取消/失效/被取代，也用于暂停中的工具行）、`⟳` 进行中。定义在 `larkcard.Glyph*`，卡片与文本消息共用；不再有 ✅/❌/⏳/⏹ 这类图形符号。
- **暂停时**：工具行从 `⟳` 改 `○ 等待人工确认` 并**停表**（不再显示增长的耗时），正文占位清空，决策区出现在同一张卡上。
- **收尾时**：`finish()` 用 `BuildRichCardWithDecisions` 重建整张卡（决策区必须传进去，否则会被整块抹掉）；`config.update_multi: true` 是元素级写接口（`CardElement.Create/Delete/Settings`）的前提。
