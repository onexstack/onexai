// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package larkcard

import (
	"fmt"
	"strings"
)

func splitRichStepsByLane(steps []RichStep) (reasoning []RichStep, tools []RichStep) {
	for _, step := range steps {
		if step.Kind == RichStepKindThinking {
			reasoning = append(reasoning, step)
			continue
		}
		tools = append(tools, step)
	}
	return reasoning, tools
}
func richLaneTitle(label string, count int) string {
	if count > 0 {
		return fmt.Sprintf("%s (%d)", label, count)
	}
	return label
}

// ToolPanelLabel names the panel the tool-call list lives in. The live streaming
// card titles its panel with the bare label and the final card with the labelled
// count, so the two read as the same component; holding the label here is what
// keeps a rename from reaching only one of them.
const ToolPanelLabel = "工具调用"

// richStepRowText renders a step for the degraded fallback card, whose body is one
// markdown element with no panels. It is the same row the tool-call lists show,
// with the detail and result lines — which need a width the fallback does not have
// to spare — following underneath.
func richStepRowText(step RichStep) string {
	if step.Kind == RichStepKindThinking {
		return richThinkingText(step)
	}
	lines := []string{ToolStepLine(step, step.DurationMs)}
	if detail := richStepDetail(step); detail != "" && detail != richStepDisplayName(step) {
		lines = append(lines, detail)
	}
	if result := strings.TrimSpace(step.Result); result != "" {
		lines = append(lines, result)
	}
	return strings.Join(lines, "\n")
}

// richThinkingText is a reasoning entry's body. A reasoning step the backend
// summarized shows that summary; one it did not is named by its lane label rather
// than left blank.
func richThinkingText(step RichStep) string {
	if detail := richStepDetail(step); detail != "" {
		return detail
	}
	return richStepDisplayName(step)
}

// richStepElements renders one step into its panel's element list.
//
// A tool step is a single markdown line built by ToolStepLine, the same function
// the live panel uses. It used to be a two-column column_set — the name in one
// column and a standard_icon badge in the other, because an icon can only live on
// an element's icon field and never inline in text. Rendering the state as a
// coloured glyph instead is what lets both tool-call lists share one formatter and
// therefore one string; the columns bought alignment the one-line row does not
// need. The detail and result lines are too wide for either and follow underneath.
func richStepElements(step RichStep) []map[string]any {
	if step.Kind == RichStepKindThinking {
		return []map[string]any{{
			"tag":  "div",
			"icon": map[string]any{"tag": "standard_icon", "token": reasoningToolIcon},
			"text": map[string]any{
				"tag":        "plain_text",
				"content":    richThinkingText(step),
				"text_size":  "notation",
				"text_color": "grey",
			},
		}}
	}

	elements := []map[string]any{{
		"tag":     "markdown",
		"content": ToolStepLine(step, step.DurationMs),
	}}
	if detail := richStepDetail(step); detail != "" && detail != richStepDisplayName(step) {
		elements = append(elements, richPlaceholderElement(detail))
	}
	if result := strings.TrimSpace(step.Result); result != "" {
		elements = append(elements, richPlaceholderElement(result))
	}
	return elements
}

// richPlaceholderElement is the house footnote, shared with the typed card
// renderer (see noteDiv) so the panel's dimmed lines and a card's `Note` cannot
// drift into two different shapes.
func richPlaceholderElement(text string) map[string]any {
	return noteDiv(text)
}
func richPanelElements(steps []RichStep, emptyText string) []map[string]any {
	if len(steps) == 0 {
		return []map[string]any{richPlaceholderElement(emptyText)}
	}
	const maxPanelSteps = 10
	visible := steps
	hidden := 0
	if len(steps) > maxPanelSteps {
		hidden = len(steps) - maxPanelSteps
		visible = steps[hidden:]
	}
	elements := make([]map[string]any, 0, 2*len(visible)+1)
	if hidden > 0 {
		elements = append(elements, richPlaceholderElement(fmt.Sprintf("… 前面还有 %d 步", hidden)))
	}
	for _, step := range visible {
		elements = append(elements, richStepElements(step)...)
	}
	return elements
}

// PanelElement builds the card 2.0 collapsible panel every step list in this repo
// draws into: grey background, grey border, the card's standard padding.
//
// It is exported because two packages build this panel — the final rich card below
// and the live streaming card (biz/v1/bot/streaming_card.go) — and nothing tied the
// two together. The live panel was hand-built against `background_style`, which is
// not a card 2.0 field: the panel still parses, so it renders transparent and
// borderless while this one is grey and bordered, and neither the platform nor a
// unit test says a word about it.
//
// A caller that needs to address the panel itself (the streaming path does) adds
// its own element_id to the returned map. Only such a caller should: an id nothing
// updates is one more literal that can go stale.
func PanelElement(title string, expanded bool, elements []map[string]any) map[string]any {
	return map[string]any{
		"tag":              "collapsible_panel",
		"expanded":         expanded,
		"background_color": "grey",
		"header": map[string]any{
			"title": map[string]any{"tag": "plain_text", "content": title},
		},
		"border":           map[string]any{"color": "grey"},
		"vertical_spacing": "8px",
		"padding":          "4px 8px",
		"elements":         elements,
	}
}
func compactRichStepsForCardSize(steps []RichStep, perLaneLimit, textLimit int) []RichStep {
	if len(steps) == 0 || perLaneLimit <= 0 {
		return nil
	}
	kept := make([]RichStep, 0, min(len(steps), perLaneLimit*2))
	reasoning := 0
	tools := 0
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if step.Kind == RichStepKindThinking {
			if reasoning >= perLaneLimit {
				continue
			}
			reasoning++
		} else {
			if tools >= perLaneLimit {
				continue
			}
			tools++
		}
		kept = append(kept, compactRichStepText(step, textLimit))
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return kept
}
func compactRichStepText(step RichStep, textLimit int) RichStep {
	step.Summary = compactRichText(step.Summary, textLimit)
	step.Result = compactRichText(step.Result, textLimit)
	return step
}
func compactRichText(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= maxRunes {
		return string(rs)
	}
	return string(rs[:maxRunes]) + "..."
}
func compactRichFallbackMarkdown(steps []RichStep) string {
	compactSteps := compactRichStepsForCardSize(steps, 3, 120)
	if len(compactSteps) == 0 {
		return ""
	}
	lines := []string{"内容过大，仅展示最近的活动："}
	for _, step := range compactSteps {
		line := strings.TrimSpace(richStepRowText(step))
		if line == "" {
			continue
		}
		line = strings.ReplaceAll(line, "\n", " - ")
		lines = append(lines, "- "+line)
	}
	return strings.Join(lines, "\n")
}
func richStepDisplayName(step RichStep) string {
	if step.Kind == RichStepKindThinking {
		return "思考"
	}
	return buildToolDisplay(step.Name, step.Summary).Title
}

// richStepDetail is a step's detail line: what the tool was asked to do, already
// extracted and redacted by buildToolDisplay. Empty when there is nothing to show,
// which is why callers must not substitute the name for it — the row already opens
// with the name, and printing it twice is what the old single-string row did.
func richStepDetail(step RichStep) string {
	return buildToolDisplay(step.Name, step.Summary).Detail
}

// ToolStepLine renders one tool-call record as the single line both tool-call
// lists show: the live panel while a turn runs, and the final card's tool-call (工具调用)
// panel once it has finished.
//
//	✓ Read（⚡ 62ms | ✨ 19.9k）
//
// One function for both is the point. The two lists used to format the same
// record differently — different name, different icon carrier, different
// separator — and no test could see it, because each side was only ever compared
// against itself.
//
// elapsedMs belongs to the caller: the final card passes the duration the backend
// measured, while the live panel passes how long the client has been waiting for a
// row the backend has not priced yet.
func ToolStepLine(step RichStep, elapsedMs int64) string {
	var b strings.Builder
	b.WriteString(StepStatusMarkup(step))
	b.WriteByte(' ')
	b.WriteString(escapeMarkdownText(buildToolDisplay(step.Name, step.Summary).Title))
	if figures := ToolStepFigures(step, elapsedMs); figures != "" {
		b.WriteString("（")
		b.WriteString(figures)
		b.WriteString("）")
	}
	return b.String()
}

// waitingFigures is what a row says while the run is parked on a decision, in the
// slot the duration and token figures would otherwise take. Chinese only, matching
// the card it appears on: every other label around it (工具调用 = tool call, 请选择 = choose, the buttons)
// is Chinese, and the row is short by design.
const waitingFigures = "等待人工确认"

// skippedFigures is what a row says once the decision it was waiting on went
// unmade. It is stated rather than left blank because a row with no figures and
// a neutral ○ reads as "still going somewhere"; this reads as "did not run".
const skippedFigures = "未执行"

// ToolStepFigures is a tool step's measured metadata, joined for display inside the
// row's parentheses. A reasoning step carries none: it is not an invocation, so it
// has no duration and no tokens to attribute.
//
// elapsedMs is a parameter rather than a read of step.DurationMs because the live
// panel shows a running row's waiting time in that slot.
func ToolStepFigures(step RichStep, elapsedMs int64) string {
	if step.Kind == RichStepKindThinking {
		return ""
	}
	// A waiting row has no figures to state: nothing about the invocation has been
	// measured yet, and the one thing worth saying is who it is waiting on.
	if strings.TrimSpace(step.Status) == RichStepStatusWaiting {
		return waitingFigures
	}
	if strings.TrimSpace(step.Status) == RichStepStatusSkipped {
		return skippedFigures
	}
	var parts []string
	if step.ExitCode != nil {
		parts = append(parts, fmt.Sprintf("退出码：%d", *step.ExitCode))
	}
	// Both figures are omitted when unreported rather than shown as zero: an
	// upstream that returned no usage must not make every tool look free.
	if elapsedMs > 0 {
		parts = append(parts, "⚡ "+FormatDurationMs(elapsedMs))
	}
	if step.Tokens > 0 {
		parts = append(parts, "✨ "+FormatTokens(step.Tokens))
	}
	return strings.Join(parts, " | ")
}
