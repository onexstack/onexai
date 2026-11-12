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
	"encoding/json"
	"strings"
)

// RichCardMainTextElementID is the fixed element_id assigned to the rich card's
// main answer markdown. It must match the element_id the streaming-card path
// updates incrementally.
const RichCardMainTextElementID = "main_text"

// MaxCardJSONBytes bounds any card payload sent to Feishu, rich or plain. An
// oversized card is rejected with 230099 "parse card json err", which from the
// sending side looks the same as a card that was never sent — so callers that
// can deliver their content another way should do that rather than try.
const MaxCardJSONBytes = 28000

// maxRichCardJSONBytes is the rich-card-local name for the same bound; longer
// rich payloads are progressively compacted.
const maxRichCardJSONBytes = MaxCardJSONBytes

// richCardTableLimit is the number of markdown tables kept in a rich card body;
// tables beyond it are wrapped in code fences.
const richCardTableLimit = 3

// RichStepKind distinguishes tool steps from thinking/reasoning steps.
type RichStepKind string

const (
	// RichStepKindTool marks a tool invocation.
	RichStepKindTool RichStepKind = "tool"
	// RichStepKindThinking marks a reasoning/thinking entry.
	RichStepKindThinking RichStepKind = "thinking"
)

// RichStepStatusWaiting marks a pending invocation whose wait is a person's
// decision rather than the tool's own running time: the run is parked on an
// approval, so the row must stop counting and say what it is waiting for.
//
// It is a client-side status — the tool-step port reports "pending", "ok" and
// "fail" — because the pause belongs to this side: the backend has stopped, the
// card is the only thing still on screen, and leaving a frozen "⟳ 1.2s" there
// would claim the tool was working while it was waiting on the reader.
const RichStepStatusWaiting = "waiting"

// RichStepStatusPending is the status the tool-step port reports for an
// invocation that has started and not finished. It is named here because the
// card's other statuses are decided against it — a row is drawn as running, as
// waiting on a person, or as never run, and that is a decision about this value.
const RichStepStatusPending = "pending"

// RichStepStatusSkipped marks an invocation that was waiting on a person and
// will now never run: the pause was cancelled, superseded or timed out, and the
// run it belonged to was abandoned rather than resumed.
//
// It is the closing state of RichStepStatusWaiting, and it exists so the final
// card does not have to choose between two lies — that the tool is still waiting
// (nobody is going to answer) or that it ran (it did not). A client-side status
// for the same reason: the port never reports it because the backend never saw
// one.
const RichStepStatusSkipped = "skipped"

// RichStep describes a single entry in the rich card's reasoning/tool panels.
type RichStep struct {
	Kind RichStepKind
	// ID is the model's tool-call id, when the backend reports one. It is what
	// lets a later update to the same invocation replace this row instead of
	// adding another: the backend reports a tool as pending and then again as
	// finished, and again when it has tokens to attribute.
	ID      string
	Name    string
	Summary string
	Result  string
	Status  string
	// DurationMs is how long the invocation took, as measured where it ran.
	// Zero means it was not reported and the row shows no figure.
	DurationMs int64
	// Tokens is how many prompt tokens the invocation's result added to the
	// context. It is derived by the backend, arrives one round after the
	// invocation, and is zero when unreported.
	Tokens   int
	ExitCode *int
	Success  *bool
	Done     bool
}

// RichCardStatus is the state of the turn a card was built for. Cards carry no
// header of their own — the caller renders the state into the status footer, so
// the value only survives here for callers that still pass it.
type RichCardStatus string

const (
	RichCardStatusThinking RichCardStatus = "thinking"
	RichCardStatusWorking  RichCardStatus = "working"
	RichCardStatusDone     RichCardStatus = "done"
	RichCardStatusError    RichCardStatus = "error"
)

// statusBadge is how one step's state is shown. A step row is a markdown element
// — the two tool-call lists have to render the same string — so the state travels
// inline: glyph is the bare character and markup is that same character carrying
// the colour.
//
// The two colour vocabularies are not the same list. The standard_icon colours
// spell the neutral one "grey"; a card 2.0 rich-text tag spells it "neutral", and
// an unrecognised colour name inside a tag is rendered as literal text rather than
// ignored.
//
// markup is empty for a badge built out of an unrecognised status. That text is
// upstream's, and a tag is exactly the thing it could break out of.
type statusBadge struct {
	glyph  string
	markup string
}

// Status glyphs. They are bare characters rather than emoji, and this is the
// whole of the vocabulary any card in this repo says states with: the tool rows,
// the badges below and the live card's decision block all render from here, so no
// two of them can drift into different pictures for the same idea.
//
// GlyphPending is the one the decision block adds — "your call, not the tool's".
// GlyphIdle covers every way a step ends without running (cancelled, superseded,
// expired): in all of them the tool did not execute, and the words around the
// glyph are what say which.
const (
	GlyphDone    = "✓"
	GlyphFailed  = "✗"
	GlyphRunning = "⟳"
	GlyphIdle    = "○"
	GlyphPending = "?"
)

var (
	badgeDone    = statusBadge{glyph: GlyphDone, markup: "<font color='green'>" + GlyphDone + "</font>"}
	badgeFailed  = statusBadge{glyph: GlyphFailed, markup: "<font color='red'>" + GlyphFailed + "</font>"}
	badgeRunning = statusBadge{glyph: GlyphRunning, markup: "<font color='blue'>" + GlyphRunning + "</font>"}
	badgeUnknown = statusBadge{glyph: GlyphIdle, markup: "<font color='neutral'>" + GlyphIdle + "</font>"}
)

// StepStatusMarkup is the status glyph a tool row opens with, coloured to match
// the state: green when the invocation succeeded, red when it failed, blue while
// it runs. It is the inline counterpart of the badge the card used to draw as a
// standard_icon — same glyph, same colour, carried in the text because a markdown
// element has no icon field.
func StepStatusMarkup(step RichStep) string {
	badge := richStepBadge(step)
	if badge.markup != "" {
		return badge.markup
	}
	return badge.glyph
}

// richStepBadge resolves a step's state to its badge. "pending", "ok" and "fail"
// are the values the tool-step port reports; Success is the older boolean form.
// Anything else is a contract change upstream rather than a state to guess at: the
// card shows a neutral icon, and the text paths print the raw value so it stays
// visible instead of being silently flattened into a dot.
func richStepBadge(step RichStep) statusBadge {
	switch strings.TrimSpace(step.Status) {
	case "ok":
		return badgeDone
	case "fail":
		return badgeFailed
	case RichStepStatusPending:
		return badgeRunning
	case RichStepStatusWaiting:
		// A row waiting on a person is drawn with the same neutral ○ as a row that
		// will never run: in both, the tool is not doing anything and it is a human
		// who will decide whether it does. What tells them apart is the text beside
		// the glyph — "等待人工确认" (awaiting confirmation) — from ToolStepFigures.
		return badgeUnknown
	}
	if step.Success != nil {
		return successBadge(*step.Success)
	}
	if raw := strings.TrimSpace(step.Status); raw != "" {
		return statusBadge{glyph: raw}
	}
	return badgeUnknown
}
func successBadge(ok bool) statusBadge {
	if ok {
		return badgeDone
	}
	return badgeFailed
}

// BuildRichCard renders a Card 2.0 "single-card" turn with collapsible
// reasoning/tool panels, streaming markdown body, and a pre-composed multi-line
// statusFooter. The card has no header: a brand title, a "Done"/"Error" label
// and a status colour band were all removed in favour of the footer, which
// already names a failure or a stop and is the only place the turn's cost is
// reported.
//
// reasoning is the turn's accumulated reasoning, which the session showed while it
// ran and can review here. An empty one leaves the panel off the card entirely —
// what a session with reasoning turned off and a backend that emits none both
// produce, so neither caller needs a branch. status is kept in the signature for
// callers that still pass it; it does not reach the card.
func BuildRichCard(status RichCardStatus, reasoning string, steps []RichStep, markdown string, streaming bool, statusFooter string) string {
	return BuildRichCardWithDecisions(status, reasoning, steps, markdown, streaming, statusFooter, nil)
}

// BuildRichCardWithDecisions is BuildRichCard carrying the turn's decision block:
// the components the live card grew while it was parked on a human, which the
// final card must keep, because the rebuild at the end of a turn replaces the
// whole card and would otherwise erase the record of what was asked and answered.
//
// decisions are card 2.0 components in the order they should appear, placed after
// the answer and before the footer. They are passed through every size-fallback
// rung: they are a few lines by construction, and a decision that scrolled off the
// payload limit would be a decision the reader cannot see they made.
func BuildRichCardWithDecisions(status RichCardStatus, reasoning string, steps []RichStep, markdown string, streaming bool, statusFooter string, decisions []map[string]any) string {
	b, err := buildRichCardJSONBytes(status, reasoning, steps, markdown, streaming, statusFooter, decisions)
	if err != nil {
		return buildCardJSONWithStatus(markdown, status)
	}
	if len(b) <= maxRichCardJSONBytes {
		return string(b)
	}

	// Keep Card 2.0 visible when long tool/reasoning history would exceed the
	// Feishu payload limit. Dropping to a body-only fallback is unsafe because
	// intermediate messages often live only in panels.
	for _, limit := range []struct {
		perLane int
		textLen int
	}{
		{perLane: 10, textLen: 180},
		{perLane: 6, textLen: 120},
		{perLane: 3, textLen: 80},
	} {
		compactSteps := compactRichStepsForCardSize(steps, limit.perLane, limit.textLen)
		compact, err := buildRichCardJSONBytes(status, reasoning, compactSteps, markdown, streaming, statusFooter, decisions)
		if err == nil && len(compact) <= maxRichCardJSONBytes {
			return string(compact)
		}
	}

	// Give up the reasoning before the tool panel. The body-only fallback below
	// drops every panel, and the tool rows are the only record of what the turn
	// actually ran — they cannot be recovered from the answer, while the reasoning
	// can at least be read again on the next turn. budgetReasoningForCard should
	// keep this rung from ever firing on the reasoning's account alone.
	if compact, err := buildRichCardJSONBytes(status, "", compactRichStepsForCardSize(steps, 3, 80), markdown, streaming, statusFooter, decisions); err == nil && len(compact) <= maxRichCardJSONBytes {
		return string(compact)
	}

	fallbackMarkdown := markdown
	if strings.TrimSpace(fallbackMarkdown) == "" {
		fallbackMarkdown = compactRichFallbackMarkdown(steps)
	}
	return buildCardJSONWithStatus(fallbackMarkdown, status)
}
func buildRichCardJSONBytes(status RichCardStatus, reasoning string, steps []RichStep, markdown string, streaming bool, statusFooter string, decisions []map[string]any) ([]byte, error) {
	reasoningSteps, toolSteps := splitRichStepsByLane(steps)
	panelMaps := make([]map[string]any, 0, 3)
	if elements, ok := reasoningPanelElements(reasoning); ok {
		panelMaps = append(panelMaps, PanelElement(reasoningPanelTitle, false, elements))
	}
	if len(reasoningSteps) > 0 {
		panelMaps = append(panelMaps, PanelElement(
			richLaneTitle("思考过程", len(reasoningSteps)),
			streaming,
			richPanelElements(reasoningSteps, "思考中…"),
		))
	}
	if len(toolSteps) > 0 {
		panelMaps = append(panelMaps, PanelElement(
			richLaneTitle(ToolPanelLabel, len(toolSteps)),
			streaming,
			richPanelElements(toolSteps, "暂无工具调用"),
		))
	}
	if len(panelMaps) == 0 && streaming {
		panelMaps = append(panelMaps, PanelElement("思考过程", true, richPanelElements(nil, "思考中…")))
	}

	markdownMap := map[string]any{
		"tag":        "markdown",
		"element_id": RichCardMainTextElementID,
		"content":    sanitizeCardMarkdownForCard(markdown),
	}

	var footerElements []map[string]any
	if statusFooter != "" {
		for _, line := range strings.Split(statusFooter, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			footerElements = append(footerElements, map[string]any{
				"tag":       "markdown",
				"content":   sanitizeCardMarkdownForCard(line),
				"text_size": "notation",
			})
		}
	}

	elements := append([]map[string]any{}, panelMaps...)
	elements = append(elements, markdownMap)
	// The decision block sits between the answer and the footer: after the answer
	// it belongs to, and above the footer that states the turn's outcome — which is
	// the same order the live card holds them in, so the card does not reshuffle
	// when the turn ends.
	elements = append(elements, decisions...)
	if len(footerElements) > 0 {
		elements = append(elements, map[string]any{"tag": "hr"})
		elements = append(elements, footerElements...)
	}

	config := CardWidthConfig()
	config["streaming_mode"] = streaming
	config["update_multi"] = true
	config["enable_forward_interaction"] = true
	card := NewCardRoot(config, elements)

	return json.Marshal(card)
}

// buildCardJSONWithStatus builds the schema 2.0 fallback card used when a turn's
// panels would exceed the payload limit. Like the rich card it replaces, it
// carries no header; status is kept in the signature only for callers.
func buildCardJSONWithStatus(content string, status RichCardStatus) string {
	content = sanitizeCardMarkdownForCard(content)
	card := NewCardRoot(CardWidthConfig(), []map[string]any{
		{"tag": "markdown", "content": content},
	})
	b, _ := json.Marshal(card)
	return string(b)
}
