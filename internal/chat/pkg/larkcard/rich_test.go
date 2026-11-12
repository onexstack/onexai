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
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// walkCard calls fn for every object in a rendered card, descending through
// panels, columns and arrays. Card structure is nested enough that a test wanting
// one node kind should not hand-roll the traversal.
func walkCard(t *testing.T, cardJSON string, fn func(map[string]any)) {
	t.Helper()

	var parsed any
	if err := json.Unmarshal([]byte(cardJSON), &parsed); err != nil {
		t.Fatalf("card is not valid JSON: %v", err)
	}

	var walk func(node any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			fn(v)
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(parsed)
}

// elementIDs collects every element_id present in a rendered card, walking
// nested panels/columns. Used to assert the ids the streaming path addresses.
func elementIDs(t *testing.T, cardJSON string) map[string]bool {
	t.Helper()

	found := map[string]bool{}
	walkCard(t, cardJSON, func(node map[string]any) {
		if id, ok := node["element_id"].(string); ok {
			found[id] = true
		}
	})
	return found
}

// toolRowLines returns the tool panel's step rows as the markdown each renders.
// A tool step is one markdown element, so that is what a row is; the panel's other
// elements are the grey placeholder lines a detail or a result becomes.
func toolRowLines(t *testing.T, cardJSON string) []string {
	t.Helper()

	var lines []string
	for _, raw := range toolPanelElements(t, cardJSON) {
		el, ok := raw.(map[string]any)
		if !ok || el["tag"] != "markdown" {
			continue
		}
		content, _ := el["content"].(string)
		lines = append(lines, content)
	}
	return lines
}

// toolPanelElements returns the elements of the card's tool panel, which is where
// a step row and the lines under it live.
func toolPanelElements(t *testing.T, cardJSON string) []any {
	t.Helper()

	var panel map[string]any
	walkCard(t, cardJSON, func(node map[string]any) {
		if node["tag"] != "collapsible_panel" {
			return
		}
		title, _ := node["header"].(map[string]any)["title"].(map[string]any)["content"].(string)
		if strings.HasPrefix(title, "工具调用") {
			panel = node
		}
	})
	if panel == nil {
		t.Fatalf("card has no tool panel:\n%s", cardJSON)
	}
	return panel["elements"].([]any)
}

// TestBuildRichCardElementID locks the answer element_id. The streaming path
// addresses the answer element by exactly this id (biz/v1/bot.streamingCardElementID
// is defined as this constant, and the bot-side invariant test asserts they stay
// equal), so a change to the literal here is a wire-format break that must be
// deliberate and visible rather than silent.
func TestBuildRichCardElementID(t *testing.T) {
	if RichCardMainTextElementID != "main_text" {
		t.Errorf("RichCardMainTextElementID = %q, want %q", RichCardMainTextElementID, "main_text")
	}

	steps := []RichStep{
		{Kind: RichStepKindThinking, Summary: "weighing options"},
		{Kind: RichStepKindTool, Name: "exec", Status: "ok"},
	}
	card := BuildRichCard(RichCardStatusDone, "", steps, "the answer", false, "footer")
	if !elementIDs(t, card)[RichCardMainTextElementID] {
		t.Errorf("rich card is missing the answer element_id %q", RichCardMainTextElementID)
	}
}

// TestBuildRichCardCompactionUnderLimit pins the compaction ladder: a turn with
// far more tool steps than fit in the Feishu payload must still produce a card
// under maxRichCardJSONBytes, and must not silently drop the answer itself
// (intermediate history lives only in the panels, so dropping to a body-only
// card is unsafe — see BuildRichCard).
func TestBuildRichCardCompactionUnderLimit(t *testing.T) {
	steps := make([]RichStep, 0, 60)
	for i := range 60 {
		steps = append(steps, RichStep{
			Kind:    RichStepKindTool,
			Name:    fmt.Sprintf("tool_%02d", i),
			Summary: strings.Repeat("x", 400),
			Status:  "ok",
		})
	}
	answer := strings.Repeat("answer ", 200)

	card := BuildRichCard(RichCardStatusDone, "", steps, answer, false, "footer")
	if len(card) > maxRichCardJSONBytes {
		t.Errorf("compacted card = %d bytes, want <= %d", len(card), maxRichCardJSONBytes)
	}
	if !strings.Contains(card, "answer") {
		t.Error("compaction dropped the answer text")
	}
	var parsed any
	if err := json.Unmarshal([]byte(card), &parsed); err != nil {
		t.Errorf("compacted card is not valid JSON: %v", err)
	}
}

func TestBuildRichCardDone(t *testing.T) {
	steps := []RichStep{
		{Kind: RichStepKindThinking, Name: "Thinking", Summary: "reasoning"},
		{Kind: RichStepKindTool, Name: "exec", Summary: "ls -la", Status: "ok"},
	}
	card := BuildRichCard(RichCardStatusDone, "", steps, "answer text", false, "elapsed: 1s")
	if card == "" {
		t.Fatal("BuildRichCard returned empty string")
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(card), &parsed); err != nil {
		t.Fatalf("BuildRichCard output is not valid JSON: %v", err)
	}
	if parsed["schema"] != CardSchemaV2 {
		t.Errorf("schema = %v, want %v", parsed["schema"], CardSchemaV2)
	}
	if got := parsed["config"].(map[string]any)["width_mode"]; got != WidthModeFill {
		t.Errorf("width_mode = %v, want %v", got, WidthModeFill)
	}

	body := parsed["body"].(map[string]any)
	elements := body["elements"].([]any)
	// Expect Reasoning panel, Tools panel, main markdown, hr, footer.
	if len(elements) < 4 {
		t.Fatalf("expected at least 4 body elements, got %d", len(elements))
	}

	// Main answer markdown carries the fixed element_id.
	main := elements[2].(map[string]any)
	if main["element_id"] != RichCardMainTextElementID {
		t.Errorf("main element_id = %v, want %s", main["element_id"], RichCardMainTextElementID)
	}

	// The card carries no header at all — no brand title, no "Done", no status
	// colour band. The turn's state is the footer's business (see
	// TestBuildRichCardErrorHasNoHeader and composeStatusFooter).
	if header, ok := parsed["header"]; ok {
		t.Errorf("done card must not carry a header, got %v", header)
	}

	// Panels are titled in the same language as the rest of the card.
	panel := elements[0].(map[string]any)
	panelTitle := panel["header"].(map[string]any)["title"].(map[string]any)["content"]
	if panelTitle != "思考过程 (1)" {
		t.Errorf("reasoning panel title = %v, want %q", panelTitle, "思考过程 (1)")
	}
}

// TestBuildRichCardToolRowsAreTheSharedLine pins the shape a step row has to have
// and the reason it has it: one markdown line, the same string larkcard.ToolStepLine
// hands the live panel, so a tool call reads identically while the turn runs and
// after it ends.
//
// The row used to be a two-column column_set — the name in one column, a
// standard_icon badge in the other — which is exactly what could not match the live
// panel: a standard_icon can only sit on an element's icon field, and the live panel
// is a markdown element with no such field. The card draws no column_set for a step
// any more, and that is asserted here rather than left to a real card to reveal.
func TestBuildRichCardToolRowsAreTheSharedLine(t *testing.T) {
	steps := []RichStep{
		{Kind: RichStepKindTool, ID: "a", Name: "exec", Summary: "ls -la", Status: "ok", DurationMs: 234, Tokens: 820},
		{Kind: RichStepKindTool, ID: "b", Name: "read", Summary: "/tmp/x", Status: "fail", DurationMs: 12},
		{Kind: RichStepKindTool, ID: "c", Name: "grep", Status: "pending"},
	}
	// The expected lines are written out rather than compared against
	// ToolStepLine's own return, so this test can fail if the format itself drifts.
	// It pins all four of the things the two lists used to disagree about: the
	// coloured glyph, the display title rather than the raw tool name, the " | "
	// separator, and the fullwidth parentheses around the figures.
	want := []string{
		"<font color='green'>✓</font> Inspect files（⚡ 234ms | ✨ 820）",
		"<font color='red'>✗</font> Read（⚡ 12ms）",
		"<font color='blue'>⟳</font> Search text",
	}

	card := BuildRichCard(RichCardStatusWorking, "", steps, "", true, "")
	lines := toolRowLines(t, card)
	if len(lines) != len(want) {
		t.Fatalf("tool rows = %d, want %d:\n%v", len(lines), len(want), lines)
	}
	for i, expected := range want {
		if lines[i] != expected {
			t.Errorf("row %d = %q, want %q", i, lines[i], expected)
		}
	}

	for _, raw := range toolPanelElements(t, card) {
		if el, ok := raw.(map[string]any); ok && el["tag"] == "column_set" {
			t.Errorf("step row is still laid out in columns: %v", el)
		}
	}
	if strings.Contains(card, "状态") {
		t.Errorf("card carries a status word:\n%s", card)
	}
}

// TestBuildRichCardRowWithoutFigures covers the row of a tool that ran and reported
// nothing: the parentheses go with the figures rather than staying behind empty. A
// tool that fails without reporting a duration gets exactly this row on the final
// card, so it is the common case for a failure, not a corner one.
func TestBuildRichCardRowWithoutFigures(t *testing.T) {
	steps := []RichStep{{Kind: RichStepKindTool, ID: "a", Name: "exec", Summary: "ls -la", Status: "fail"}}
	card := BuildRichCard(RichCardStatusError, "", steps, "", false, "")

	lines := toolRowLines(t, card)
	if len(lines) != 1 {
		t.Fatalf("tool rows = %d, want 1", len(lines))
	}
	if !strings.HasPrefix(lines[0], badgeFailed.markup+" ") {
		t.Errorf("row = %q, want it to open with the failure badge", lines[0])
	}
	if strings.ContainsAny(lines[0], "（）") {
		t.Errorf("row = %q, want no parentheses for a step that reported no figures", lines[0])
	}
}

// TestRichStepDetailIsAFullWidthLine covers where a tool's detail goes now that the
// name has a column of its own: a path or a command is too wide for either column,
// so it follows the row as its own line — and a step the backend gave nothing to
// say about emits no such line at all.
func TestRichStepDetailIsAFullWidthLine(t *testing.T) {
	withDetail := []RichStep{{Kind: RichStepKindTool, ID: "a", Name: "exec", Summary: "go test ./...", Status: "ok"}}
	card := BuildRichCard(RichCardStatusDone, "", withDetail, "", false, "")
	if !strings.Contains(card, "go test ./...") {
		t.Errorf("card dropped the tool detail:\n%s", card)
	}
	if elements := toolPanelElements(t, card); len(elements) != 2 {
		t.Errorf("panel elements = %d, want the row plus one detail line", len(elements))
	}

	withoutDetail := []RichStep{{Kind: RichStepKindTool, ID: "a", Name: "exec", Status: "ok"}}
	card = BuildRichCard(RichCardStatusDone, "", withoutDetail, "", false, "")
	if elements := toolPanelElements(t, card); len(elements) != 1 {
		t.Errorf("panel elements = %d, want just the row when there is no detail", len(elements))
	}
}

// TestBuildRichCardCompactionLadderRuns exists because the test above it does not
// reach the compaction ladder: a panel caps at 10 visible rows, so its card lands
// far under the payload limit and BuildRichCard returns on the first branch. The
// ladder is the guard that keeps a card deliverable, and this change makes every
// row several times larger, so it needs a test that actually runs it.
func TestBuildRichCardCompactionLadderRuns(t *testing.T) {
	steps := make([]RichStep, 0, 40)
	for i := range 40 {
		steps = append(steps, RichStep{
			Kind: RichStepKindTool, ID: fmt.Sprintf("call_%02d", i),
			Name:    fmt.Sprintf("tool_%02d", i),
			Summary: strings.Repeat("x", 400), Result: strings.Repeat("y", 400),
			Status: "ok", DurationMs: 1200, Tokens: 820,
		})
	}
	// The answer is the part of the card compaction never touches, so it sets the
	// floor the ladder has to squeeze the panels under.
	answer := strings.Repeat("answer ", 2500)

	card := BuildRichCard(RichCardStatusDone, "", steps, answer, false, "footer")
	if len(card) > maxRichCardJSONBytes {
		t.Errorf("compacted card = %d bytes, want <= %d", len(card), maxRichCardJSONBytes)
	}
	if !strings.Contains(card, "answer") {
		t.Error("compaction dropped the answer text")
	}
	if lines := toolRowLines(t, card); len(lines) == 0 {
		t.Error("compaction dropped every tool row")
	}
	// And the ladder really ran: an uncompacted row carries the full 400-character
	// summary, which no rung of the ladder allows. Without this the test would pass
	// on a BuildRichCard that never compacted anything.
	if strings.Contains(card, strings.Repeat("x", 200)) {
		t.Error("card was not compacted: it still carries an uncompacted summary")
	}
}

// TestBuildCardJSONWithStatusFillsWidth pins the degraded fallback card's width:
// long turns fall back to it, so it has to span the chat window too.
func TestBuildCardJSONWithStatusFillsWidth(t *testing.T) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(buildCardJSONWithStatus("x", RichCardStatusError)), &parsed); err != nil {
		t.Fatalf("fallback card is not valid JSON: %v", err)
	}
	if parsed["schema"] != CardSchemaV2 {
		t.Errorf("schema = %v, want %v", parsed["schema"], CardSchemaV2)
	}
	config := parsed["config"].(map[string]any)
	if config["width_mode"] != WidthModeFill {
		t.Errorf("width_mode = %v, want %v", config["width_mode"], WidthModeFill)
	}
	if _, ok := parsed["body"].(map[string]any)["elements"]; !ok {
		t.Errorf("fallback card missing body elements: %v", parsed)
	}
	// Same contract as the rich card it degrades from: no header.
	if header, ok := parsed["header"]; ok {
		t.Errorf("fallback card must not carry a header, got %v", header)
	}
}

// TestBuildRichCardErrorHasNoHeader pins the failure case of the same contract:
// a failed turn is told apart by the footer's "失败" (failed) marker, not by a red header
// (the bot composes it in composeStatusFooter), and the body still renders.
func TestBuildRichCardErrorHasNoHeader(t *testing.T) {
	card := BuildRichCard(RichCardStatusError, "", nil, "boom", false, "")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(card), &parsed); err != nil {
		t.Fatal(err)
	}
	if header, ok := parsed["header"]; ok {
		t.Errorf("error card must not carry a header, got %v", header)
	}
	if !strings.Contains(card, "boom") {
		t.Error("error card dropped the body")
	}
}

func TestSplitRichStepsByLane(t *testing.T) {
	steps := []RichStep{
		{Kind: RichStepKindThinking},
		{Kind: RichStepKindTool, Name: "grep"},
		{Kind: RichStepKindThinking},
	}
	reasoning, tools := splitRichStepsByLane(steps)
	if len(reasoning) != 2 {
		t.Errorf("reasoning lane = %d, want 2", len(reasoning))
	}
	if len(tools) != 1 {
		t.Errorf("tools lane = %d, want 1", len(tools))
	}
}

func TestResolveToolDescriptor(t *testing.T) {
	d := resolveToolDescriptor("exec")
	if d == nil || d.Title != "Run command" {
		t.Errorf("resolveToolDescriptor(exec) = %v, want Run command", d)
	}
	if got := buildToolDisplay("exec", "git status").Title; got != "Git" {
		t.Errorf("buildToolDisplay(exec, git status).Title = %q, want Git", got)
	}
}

func TestRedactInlineSecrets(t *testing.T) {
	// Each row is a spelling the credential can arrive in, with the secret's own
	// text asserted absent by name — a redaction that replaced the wrong half of
	// the match would leave the secret readable while still passing a check for
	// the word "[redacted]".
	for _, tc := range []struct {
		name   string
		in     string
		secret string
	}{
		{
			name:   "an auth header",
			in:     "curl -H 'Authorization: Bearer abc123' https://x",
			secret: "abc123",
		},
		{
			name:   "an environment assignment",
			in:     "TENANT_TOKEN=tok_live_9f3a encountered while reading config",
			secret: "tok_live_9f3a",
		},
		{
			// Tool output is mostly JSON, which the assignment pattern above does
			// not match: the key is quoted and the separator is a colon. Without
			// this row the coverage would say the redactor works while the path
			// that carries tool results to the card leaked.
			name:   "a JSON member",
			in:     `{"app_secret":"s3cr3t-value","task_id":39}`,
			secret: "s3cr3t-value",
		},
		{
			name:   "a JSON member whose value is not a string",
			in:     `{"client_secret":99887}`,
			secret: "99887",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := redactInlineSecrets(tc.in)
			if strings.Contains(out, tc.secret) {
				t.Errorf("secret not redacted: %q", out)
			}
			if !strings.Contains(out, "redacted") {
				t.Errorf("nothing was redacted: %q", out)
			}
		})
	}

	t.Run("ordinary output is left alone", func(t *testing.T) {
		// The patterns are narrow on purpose: a job listing and a pipeline URL are
		// what these cards mostly carry, and mangling them would make the panel
		// useless in exchange for nothing.
		for _, in := range []string{
			`{"triggered":true,"task_id":39,"console_url":"https://zadig.example/v2"}`,
			"job 1234 finished in 2m",
			`{"status":"ok","stage":"build"}`,
		} {
			if out := redactInlineSecrets(in); out != in {
				t.Errorf("redactInlineSecrets(%q) = %q, want it unchanged", in, out)
			}
		}
	})
}

func TestSplitMarkdownByTables(t *testing.T) {
	md := strings.TrimSpace(`intro line

| a | b |
|---|---|
| 1 | 2 |

middle line

| c | d |
|---|---|
| 3 | 4 |
`)
	parts := SplitMarkdownByTables(md, 1)
	if len(parts) != 2 {
		t.Fatalf("SplitMarkdownByTables = %d parts, want 2", len(parts))
	}
}

func TestSanitizeCardMarkdownForCardRedactsLink(t *testing.T) {
	in := "[x](ftp://host/path)"
	out := sanitizeCardMarkdownForCard(in)
	if strings.Contains(out, "](ftp://") {
		t.Errorf("invalid-scheme link not rewritten: %q", out)
	}
}

// findPanel returns the collapsible panel whose header title is exactly title, or
// nil. An exact match rather than a prefix: the legacy thinking lane's panel is
// titled "思考过程 (N)" (thinking process), and the two titles must not be able to satisfy each
// other's assertions.
func findPanel(t *testing.T, cardJSON, title string) map[string]any {
	t.Helper()

	var found map[string]any
	walkCard(t, cardJSON, func(node map[string]any) {
		if node["tag"] != "collapsible_panel" {
			return
		}
		header, _ := node["header"].(map[string]any)
		heading, _ := header["title"].(map[string]any)
		if heading["content"] == title {
			found = node
		}
	})
	return found
}

// panelBodyText returns a panel's child markdown content, which is where a
// single-element panel keeps its text.
func panelBodyText(t *testing.T, panel map[string]any) string {
	t.Helper()

	children, ok := panel["elements"].([]any)
	if !ok || len(children) == 0 {
		t.Fatalf("panel elements = %v, want a non-empty array", panel["elements"])
	}
	el, ok := children[0].(map[string]any)
	if !ok {
		t.Fatalf("panel child = %v, want an object", children[0])
	}
	content, _ := el["content"].(string)
	return content
}

// TestBuildRichCardReasoningPanel pins the reasoning panel the final card carries:
// it leads the card, starts collapsed, wears the same house style as every other
// panel, and holds exactly one markdown element — the same shape the live panel's
// body has, so the text does not reflow the moment the turn ends.
//
// The panel is the reason this work exists at all: the reasoning used to live only
// in the live card, which the final card replaced wholesale, so the one part of a
// turn a reader might want to go back over was the part thrown away.
func TestBuildRichCardReasoningPanel(t *testing.T) {
	const reasoning = "先看题意，再想边界。"
	card := BuildRichCard(RichCardStatusDone, reasoning, nil, "答案", false, "")

	var doc struct {
		Body struct {
			Elements []map[string]any `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(card), &doc); err != nil {
		t.Fatalf("card is not valid JSON: %v\n%s", err, card)
	}
	if len(doc.Body.Elements) == 0 {
		t.Fatal("card has no elements")
	}

	first := doc.Body.Elements[0]
	if first["tag"] != "collapsible_panel" {
		t.Fatalf("first element tag = %v, want collapsible_panel (the reasoning leads the card)", first["tag"])
	}
	header, _ := first["header"].(map[string]any)
	heading, _ := header["title"].(map[string]any)
	if heading["content"] != reasoningPanelTitle {
		t.Errorf("panel title = %v, want %q", heading["content"], reasoningPanelTitle)
	}
	if heading["tag"] != "plain_text" {
		t.Errorf("panel title tag = %v, want plain_text", heading["tag"])
	}
	if first["expanded"] != false {
		t.Errorf("reasoning panel expanded = %v, want false (collapsed by default)", first["expanded"])
	}
	for key, want := range map[string]any{
		"background_color": "grey",
		"border":           map[string]any{"color": "grey"},
		"padding":          "4px 8px",
		"vertical_spacing": "8px",
	} {
		got, _ := first[key]
		if !reflectDeepEqual(got, want) {
			t.Errorf("reasoning panel %s = %v, want %v", key, got, want)
		}
	}
	if _, ok := first["background_style"]; ok {
		t.Error("reasoning panel carries background_style, which card 2.0 ignores")
	}

	children := first["elements"].([]any)
	if len(children) != 1 {
		t.Fatalf("reasoning panel holds %d elements, want exactly 1", len(children))
	}
	body := children[0].(map[string]any)
	if body["tag"] != "markdown" {
		t.Errorf("reasoning body tag = %v, want markdown (the live panel's body is markdown too)", body["tag"])
	}
	if !strings.Contains(panelBodyText(t, first), "先看题意") {
		t.Errorf("reasoning body = %q, want it to carry the reasoning", panelBodyText(t, first))
	}

	// An empty lane is how "reasoning off" and "this backend emits none" both
	// arrive, and neither may leave an empty panel behind.
	for _, empty := range []string{"", "   ", "\n\n"} {
		blank := BuildRichCard(RichCardStatusDone, empty, nil, "答案", false, "")
		if panel := findPanel(t, blank, reasoningPanelTitle); panel != nil {
			t.Errorf("reasoning %q produced a panel; want none", empty)
		}
	}
}

// reflectDeepEqual compares two decoded JSON values without importing reflect for
// one call. Maps and slices are what the style assertions hold.
func reflectDeepEqual(a, b any) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ab) == string(bb)
}

// TestBuildRichCardReasoningIsBudgeted pins the bound that keeps a long chain of
// thought from taking the whole card down with it. The fallback BuildRichCard
// reaches when nothing fits emits a body-only card, dropping every panel —
// including the tool rows, which are the turn's only record of what it ran.
func TestBuildRichCardReasoningIsBudgeted(t *testing.T) {
	reasoning := "【头】" + strings.Repeat("推理内容", 50000) + "【尾】"
	if utf8.RuneCountInString(reasoning) < 200000 {
		t.Fatalf("fixture reasoning is only %d runes; it must far exceed the budget",
			utf8.RuneCountInString(reasoning))
	}

	steps := make([]RichStep, 0, 40)
	for i := range 40 {
		steps = append(steps, RichStep{
			Kind:    RichStepKindTool,
			Name:    fmt.Sprintf("tool_%02d", i),
			Summary: strings.Repeat("y", 300),
			Status:  "ok",
		})
	}

	card := BuildRichCard(RichCardStatusDone, reasoning, steps, strings.Repeat("答案 ", 100), false, "footer")
	if len(card) > maxRichCardJSONBytes {
		t.Errorf("card = %d bytes, want <= %d", len(card), maxRichCardJSONBytes)
	}

	panel := findPanel(t, card, reasoningPanelTitle)
	if panel == nil {
		t.Fatalf("the reasoning panel was dropped entirely:\n%.400s", card)
	}
	body := panelBodyText(t, panel)
	if !strings.Contains(body, "中间省略") {
		t.Errorf("the cut reasoning carries no omission notice:\n%.200s", body)
	}
	if !strings.HasPrefix(body, "【头】") {
		t.Errorf("the head of the reasoning was lost, body starts %q", body[:min(20, len(body))])
	}
	if !strings.HasSuffix(body, "【尾】") {
		t.Errorf("the tail of the reasoning was lost, body ends %q", body[max(0, len(body)-20):])
	}
	// The tool panel is the control: the budget exists so this survives. Matched by
	// label rather than by count because the ladder decides how many rows are kept.
	if !strings.Contains(card, ToolPanelLabel) {
		t.Errorf("the tool panel was dropped along with the reasoning:\n%.400s", card)
	}
	var parsed any
	if err := json.Unmarshal([]byte(card), &parsed); err != nil {
		t.Errorf("card is not valid JSON: %v", err)
	}
}

// TestReasoningBudgetIsMeasuredAfterSanitize is the ordering assertion. Sanitizing
// only ever grows markdown — an overflow table gains a code fence, a mid-line fence
// gains a newline — so a budget measured before it is a budget that lies by however
// much the sanitizer added. This fixture is built so that the raw text fits the
// budget and the sanitized text does not.
func TestReasoningBudgetIsMeasuredAfterSanitize(t *testing.T) {
	// Each table is wrapped in a fence once sanitizeCardMarkdownTables runs past
	// richCardTableLimit, and each wrap adds 8 bytes. 461 tables is 5993 raw bytes:
	// inside the budget, and far outside it once the fences are added.
	raw := strings.Repeat("|a|\n|-|\n|1|\n\n", 461)
	if len(raw) > maxReasoningCardBytes {
		t.Fatalf("fixture raw text is %d bytes; it must fit the budget for this test to mean anything",
			len(raw))
	}

	sanitized := sanitizeCardMarkdownForCard(raw)
	if len(sanitized) <= maxReasoningCardBytes {
		t.Fatalf("fixture sanitized to %d bytes, which still fits the budget; this test can no longer "+
			"distinguish measuring before sanitizing from measuring after", len(sanitized))
	}

	elements, ok := reasoningPanelElements(raw)
	if !ok {
		t.Fatal("reasoningPanelElements dropped the text entirely")
	}
	content, _ := elements[0]["content"].(string)
	if len(content) > maxReasoningCardBytes {
		t.Errorf("emitted reasoning = %d bytes, want <= %d; the budget was measured before sanitizing",
			len(content), maxReasoningCardBytes)
	}
	if !utf8.ValidString(content) {
		t.Error("the cut split a multi-byte rune")
	}
	if !strings.Contains(content, "中间省略") {
		t.Error("the cut text carries no omission notice")
	}
}

// TestPanelElementIsTheOnlyPanelShape pins that every panel in a rendered card is
// built by PanelElement. background_style is the card 1.0 field name the live panel
// was written against — card 2.0 ignores it, so the panel parses and renders
// unstyled, which is a difference no platform error and no id assertion can see.
func TestPanelElementIsTheOnlyPanelShape(t *testing.T) {
	steps := []RichStep{
		{Kind: RichStepKindTool, Name: "exec", Status: "ok"},
		{Kind: RichStepKindThinking, Summary: "weighing options"},
	}
	card := BuildRichCard(RichCardStatusDone, "推理内容", steps, "答案", true, "footer")

	panels := 0
	walkCard(t, card, func(node map[string]any) {
		if node["tag"] != "collapsible_panel" {
			return
		}
		panels++
		if _, ok := node["background_style"]; ok {
			t.Error("panel carries background_style, which card 2.0 ignores; it renders unstyled")
		}
	})
	if panels < 3 {
		t.Errorf("found %d panels, want at least 3 (reasoning + thinking lane + tools)", panels)
	}

	panel := PanelElement("标题", false, nil)
	want := []string{"tag", "expanded", "background_color", "header", "border", "vertical_spacing", "padding", "elements"}
	if len(panel) != len(want) {
		t.Fatalf("PanelElement has %d keys, want %d: %v", len(panel), len(want), panel)
	}
	for _, key := range want {
		if _, ok := panel[key]; !ok {
			t.Errorf("PanelElement is missing %q", key)
		}
	}
}
