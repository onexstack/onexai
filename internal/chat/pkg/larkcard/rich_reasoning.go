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
	"unicode/utf8"
)

// ReasoningPanelLabel is the reasoning panel's title, used by the final card and
// by the live one. State only — no count, no duration, no emoji. A panel header is
// fixed when the card is created (cardkit replaces element contents, never
// headers), so anything that changes over the turn would be a claim the card could
// never correct, and the turn's cost is already the footer's business.
//
// Exported so the live streaming card titles its panel with this and not with a
// second literal: the live title used to be 深度思考中… (thinking…), which stated a state the
// card could not correct, and kept stating it while the turn was parked on a
// human's decision.
const ReasoningPanelLabel = "深度思考过程"

// reasoningPanelTitle is the panel title the renderers below pass on. It is the
// exported label under a private name so the two cannot drift.
const reasoningPanelTitle = ReasoningPanelLabel

// reasoningPanelElements renders the final card's reasoning panel body: the whole
// accumulated reasoning as one markdown element, sanitized and byte-budgeted.
//
// One markdown element rather than a row per step, because the live panel's body is
// a markdown element too — and it has to be, since cardkit can only replace an
// element's content, never re-type it. Anything else would reflow the text at the
// instant the card closes, which is the drift this whole panel exists to end.
//
// RichStepKindThinking is a separate, older lane that this does not use: nothing in
// production constructs a thinking step, and it stays because its rendering is
// specified and tested, not because a card draws it. Neither it nor this is dead
// code to be tidied away.
func reasoningPanelElements(reasoning string) ([]map[string]any, bool) {
	text := budgetReasoningForCard(sanitizeCardMarkdownForCard(strings.TrimSpace(reasoning)))
	if strings.TrimSpace(text) == "" {
		return nil, false
	}
	return []map[string]any{{"tag": "markdown", "content": text}}, true
}

// maxReasoningCardBytes bounds the reasoning the final card carries.
//
// The text reaching here is unbounded: the stream's MaxChars bounds only what the
// live card is shown, while Preview.Reasoning returns the whole lane. Without a
// bound, a long chain of thought pushes the payload past maxRichCardJSONBytes and
// trips the fallback that drops every panel — including the tool rows, which are
// the turn's only record of what it ran.
//
// 6000 is roughly a fifth of the payload limit: several screens inside a collapsed
// panel, and small enough that the largest everything-else case (the ladder's tool
// rows, the answer, the footer) cannot add up to the limit.
const maxReasoningCardBytes = 6000

// reasoningHeadRatio is how much of that budget the head keeps, in tenths. The head
// states the framing and the tail holds the conclusion the answer came from, so the
// middle — the part a chain of thought can spare — is what is left out.
const reasoningHeadRatio = 6

// reasoningOmittedNotice marks where reasoning was cut. Its length is reserved
// inside the budget, never added on top of it.
func reasoningOmittedNotice(omittedRunes int) string {
	return fmt.Sprintf("\n\n… 中间省略 %d 字 …\n\n", omittedRunes)
}

// budgetReasoningForCard cuts text to maxReasoningCardBytes, keeping the head and
// the tail. A head-only cut would leave the panel ending mid-thought right before
// the answer, which reads as broken text rather than as an omission the notice can
// name.
func budgetReasoningForCard(text string) string {
	if len(text) <= maxReasoningCardBytes {
		return text
	}
	// The notice is reserved before the cut and is never shorter than the one
	// finally printed: it counts the text's own runes, so its digits can only be
	// more than the omitted count's, never fewer. That is what keeps the result
	// inside the budget by construction, with nothing re-measuring it afterwards.
	reserved := reasoningOmittedNotice(utf8.RuneCountInString(text))
	head, tail := cutReasoningText(text, maxReasoningCardBytes-len(reserved))
	omitted := utf8.RuneCountInString(text) - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail)
	return head + reasoningOmittedNotice(omitted) + tail
}

// cutReasoningText keeps the head and the tail of text within max bytes. Both cuts
// land on rune boundaries — a byte slice through a Chinese character renders as the
// replacement glyph — and are then backed up to a newline, so the join cannot fall
// inside a markdown line or split the fence a kept code block opened.
func cutReasoningText(text string, max int) (head, tail string) {
	if max <= 0 {
		return "", ""
	}
	headBudget := max * reasoningHeadRatio / 10
	head = cutRunesToBytes(text, headBudget)
	if i := strings.LastIndexByte(head, '\n'); i > 0 {
		head = head[:i]
	}
	tail = cutTailRunesToBytes(text[len(head):], max-headBudget)
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
		tail = tail[i+1:]
	}
	return strings.TrimRight(head, "\n"), strings.TrimLeft(tail, "\n")
}

// cutRunesToBytes returns the longest prefix of text of at most max bytes that does
// not split a rune.
func cutRunesToBytes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(text) <= max {
		return text
	}
	end := max
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

// cutTailRunesToBytes is cutRunesToBytes' suffix counterpart.
func cutTailRunesToBytes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(text) <= max {
		return text
	}
	start := len(text) - max
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}
