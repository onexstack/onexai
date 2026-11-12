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
	"strconv"
	"strings"
	"testing"
)

// TestCard2SchemaGuard renders every card shape this repo sends and checks each
// rendered property against the card 2.0 whitelists in card2schema.go.
//
// It is the offline half of Feishu's own validation, and it exists because a card
// is built as raw maps here: nothing but the API ever complained about a property
// on the wrong component, and by then the card was already undelivered. A shape
// this test rejects is a card that would have come back as
//
//	200621: parse card json err, unknown property, property: ..., path: ROOT -> ...
//
// Add a fixture whenever a new card shape starts being sent — a fixture is a
// sentence in this file, a rejected card is a user who got nothing.
func TestCard2SchemaGuard(t *testing.T) {
	for name, cardJSON := range card2GuardFixtures(t) {
		t.Run(name, func(t *testing.T) {
			if violations := Card2Violations([]byte(cardJSON)); len(violations) > 0 {
				t.Fatalf("card carries properties Feishu rejects:\n  %s", strings.Join(violations, "\n  "))
			}
		})
	}
}

// card2GuardFixtures builds the cards, keyed by the name they fail under. The
// builder-based ones are copies of what cardnav and approval hand to the same
// renderer — an element-for-element rebuild, because importing those packages
// from here would be a cycle.
func card2GuardFixtures(t *testing.T) map[string]string {
	t.Helper()

	const sessionKey = "feishu:oc_test:ou_user"
	fixtures := map[string]string{}

	// Every element type the builder can produce, in one card.
	everything := NewCard().
		Title("工单", "red").
		Markdown("**问题**：服务不可用").
		Divider().
		Buttons(PrimaryBtn("关闭", "act:/close 1")).
		ButtonsEqual(DefaultBtn("允许", "act:/allow"), DangerBtn("拒绝", "act:/deny")).
		ListItem("默认行", "执行", "act:/run 1").
		ListItemBtn("主色行", "执行", "primary", "act:/run 2").
		ListItemBtnExtra("带参行", "选择", "default", "act:/pick 3", map[string]string{"run_id": "r1"}).
		Select("请选择", []CardSelectOption{{Text: "甲", Value: "a"}, {Text: "乙", Value: "b"}}, "a").
		Select("无选项的占位", nil, "").
		Table(CardTable{
			Columns: []CardTableColumn{
				// The key column carries the pixel width /pod sizes to its widest
				// key; the value column carries none and takes the remainder.
				{Name: "field", DisplayName: "字段", DataType: "text", Width: "100px", HorizontalAlign: "left"},
				{Name: "value", DisplayName: "值", DataType: "lark_md", HorizontalAlign: "left"},
			},
			Rows:              []map[string]any{{"field": "集群", "value": "fz-prod"}},
			PageSize:          10,
			RowHeight:         "auto",
			FreezeFirstColumn: true,
		}).
		Table(CardTable{}). // an empty table is dropped, not rendered
		Note("点击按钮执行操作").
		TaggedNote("delete-mode-selected-count", "已选 1 项").
		Build()
	fixtures["every element"] = RenderCard(everything, sessionKey)

	// /help: buttons, one list row per command group, and a note.
	help := NewCard().
		Title("帮助", "blue").
		Markdown("输入 **/命令** 直接执行，或点击下方分组查看命令。").
		Buttons(
			DefaultBtn("📜 最近消息", "nav:/history"),
			DefaultBtn("🧠 推理开关", "nav:/reasoning"),
			DefaultBtn("🤖 切换 Agent", "nav:/switch"),
		).
		ListItem("job", "查看", "nav:group:job").
		ListItem("cronjob", "查看", "nav:group:cronjob").
		Note("ChatOps 命令，按需执行").
		Build()
	fixtures["help card"] = RenderCard(help, sessionKey)

	// /agent: the current agent marked with a glyph, one row per agent, and a note.
	agent := NewCard().
		Title("切换 Agent", "blue").
		Markdown("**当前**: default").
		ListItemBtn("默认 Agent", "选择", "primary", "act:/switch").
		ListItemBtn("✓ 运维助手", "选择", "default", "act:/switch ops").
		Note("选择后，本会话的 AI 回答由该 Agent 的设定驱动").
		Build()
	fixtures["agent card"] = RenderCard(agent, sessionKey)

	// A command group page, including the empty-group card behind the same note.
	command := NewCard().Title("job", "blue")
	for _, c := range []string{"get", "delete"} {
		command.ListItem("/"+c, "/job "+c, "act:/job "+c)
	}
	fixtures["command group card"] = RenderCard(command.Note("点击按钮执行对应命令").Build(), sessionKey)

	// The permission card: an equal-width allow/deny pair, a full-width
	// always-allow, and the note that says one click decides every listed tool.
	approval := NewCard().
		Title("权限请求", "orange").
		Markdown("**工具**：exec\n\n**请求**：是否允许执行 go test ./...？").
		ButtonsEqual(
			CardButton{Text: "✓ 允许", Type: "primary", Value: PermAllowValue, Extra: map[string]string{"run_id": "r1", "req_id": "q1"}},
			CardButton{Text: "✗ 拒绝", Type: "danger", Value: "perm:deny", Extra: map[string]string{"run_id": "r1", "req_id": "q1"}},
		).
		Buttons(CardButton{Text: "✓ 总是允许", Type: "default", Value: "perm:allow_all", Extra: map[string]string{"run_id": "r1"}}).
		Note("点击按钮以继续或终止本次执行").
		Build()
	fixtures["approval card"] = RenderCard(approval, sessionKey)

	// The question card in both shapes: one list row per option, and the
	// numbered-list fallback for a multi-select question.
	question := NewCard().Title("请选择", "blue").Markdown("**部署到哪个环境？**")
	for i, opt := range []string{"dev", "staging", "prod"} {
		question.ListItemBtnExtra(opt, "选择", "default", "askq:"+string(rune('1'+i)), map[string]string{
			"run_id": "r1", "req_id": "q1", "askq_addr": "deploy.env", "askq_label": opt,
		})
	}
	fixtures["question card"] = RenderCard(question.Note("点击选择答案").Build(), sessionKey)

	multi := NewCard().Title("请选择", "blue").Markdown("**请选择要重启的服务**").
		Markdown("1. **api**\n2. **web**").
		Note("请回复选项编号（多个用逗号分隔）")
	fixtures["multi-select question card"] = RenderCard(multi.Build(), sessionKey)

	// The decision block, as it is inserted into a card that is already on
	// screen: the log line, an equal-width allow/deny pair, the session-grant row
	// under it, and the row layout a question's options use. This is the one shape
	// in the repo that goes in through the element endpoints rather than through
	// card creation, so it is the one Feishu has the least precedent for.
	fixtures["decision block elements"] = marshalElements(t,
		ElementMarkdown(decisionLogFixtureID, decisionLogFixtureText),
		ElementButtonRow("decision_row_1", []CardButton{
			{Text: "✓ 允许", Type: "primary", Value: PermAllowValue, Extra: map[string]string{"run_id": "r1", "req_id": "q1"}},
			{Text: "✗ 拒绝", Type: "danger", Value: "perm:deny", Extra: map[string]string{"run_id": "r1", "req_id": "q1"}},
		}, sessionKey, CardActionLayoutEqualColumns),
		ElementButtonRow("decision_grant_1", []CardButton{
			{Text: "✓ 总是允许", Type: "default", Value: "perm:allow_all", Extra: map[string]string{"run_id": "r1"}},
		}, sessionKey, CardActionLayoutEqualColumns),
	)
	questionOptions := []CardButton{
		{Text: "edu-onex", Type: "default", Value: "askq:1", Extra: map[string]string{"run_id": "r1", "req_id": "q1"}},
		{Text: "prod", Type: "default", Value: "askq:2", Extra: map[string]string{"run_id": "r1", "req_id": "q1"}},
	}
	fixtures["question option row"] = marshalElements(t,
		ElementButtonRow("decision_row_2", questionOptions, sessionKey, CardActionLayoutRow),
	)

	// The delete-mode checker card. Its renderer refuses a card holding markdown,
	// so this fixture is asserted to have taken that path: otherwise the guard
	// would quietly be checking the plain renderer instead.
	deleteMode := NewCard().
		Title("删除确认", "orange").
		ListItemBtn("☑ job/foo", "选择", "default", "act:/delete-mode toggle job/foo").
		ListItemBtn("☐ job/bar", "选择", "default", "act:/delete-mode toggle job/bar").
		ListItem("job/baz", "不可选", "act:/delete-mode noop job/baz").
		TaggedNote("delete-mode-selected-count", "已选 1 项").
		Note("勾选后提交").
		ButtonsEqual(DangerBtn("删除", "act:/delete-mode confirm"), DefaultBtn("取消", "act:/delete-mode cancel")).
		Build()
	renderedDeleteMode := RenderCard(deleteMode, sessionKey)
	if !strings.Contains(renderedDeleteMode, `"form"`) {
		t.Fatalf("delete-mode fixture did not take the checker path: %s", renderedDeleteMode)
	}
	fixtures["delete-mode checker card"] = renderedDeleteMode

	// The two degraded roots: an empty card (the renderer substitutes a blank
	// markdown so the body is never empty) and a nil one.
	fixtures["empty card"] = RenderCard(NewCard().Build(), "")
	fixtures["nil card"] = RenderCard(nil, sessionKey)

	// The rich card, in the shapes the streaming path closes with: reasoning
	// panel, tool panel with detail and result lines, an answer, the footer whose
	// lines are notation-sized, and the streaming/final variants of them.
	ok, failed, exitCode := true, false, 1
	steps := []RichStep{
		{Kind: RichStepKindThinking, Name: "reasoning", Summary: "先确认失败的是哪一层"},
		{Kind: RichStepKindTool, ID: "t1", Name: "exec", Summary: "go test ./...", Result: "ok", Status: "ok", DurationMs: 1234, Tokens: 300, Success: &ok, Done: true},
		{Kind: RichStepKindTool, ID: "t2", Name: "exec", Summary: "make build", Result: "boom", Status: "fail", DurationMs: 20, ExitCode: &exitCode, Success: &failed, Done: true},
	}
	fixtures["rich card (streaming)"] = BuildRichCard(RichCardStatusWorking, "失败的是第 3 个用例，断言写错了。", steps, "## 结论\n\n已修复。", true, "⏱ 12.3s · 1.2k tokens")
	fixtures["rich card (final)"] = BuildRichCard(RichCardStatusDone, "失败的是第 3 个用例。", steps, "## 结论\n\n已修复。", false, "⏱ 12.3s")
	fixtures["rich card (bare)"] = BuildRichCard(RichCardStatusThinking, "", nil, "", false, "")

	return fixtures
}

// PermAllowValue is the button value the approval card carries. It is spelled here
// rather than imported: the constant lives in the bot package, which imports this
// one, and the value travels as a string either way.
const PermAllowValue = "perm:allow"

// The decision block's own spellings, copied from the bot package for the same
// reason PermAllowValue is: the id is what the log is rewritten through, and the
// text is one line of that log.
const (
	decisionLogFixtureID   = "decision_log"
	decisionLogFixtureText = "? **需要你的批准**：pipeline（env=edu-onex）"
)

// marshalElements encodes a list of elements as the guard reads one: the same
// walk as a card body, without a card around it, because these are written into
// a body that already exists.
func marshalElements(t *testing.T, elements ...map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(elements)
	if err != nil {
		t.Fatalf("marshal elements: %v", err)
	}
	return string(raw)
}

// TestCard2SchemaGuardCatchesTheNoteBug pins the guard against the shape it was
// written for: a note whose size and colour sit on the div instead of on its inner
// plain_text. That is the card Feishu refused with
//
//	200621: unknown property, property: text_color, path: ROOT -> body -> elements -> [4](tag: div)
//
// so the guard must name both properties, at a path the API's own error can be read
// against.
func TestCard2SchemaGuardCatchesTheNoteBug(t *testing.T) {
	const brokenNote = `{"schema":"2.0","config":{"width_mode":"fill"},"body":{"elements":[
		{"tag":"div","text":{"tag":"plain_text","content":"点击选择答案"},"text_size":"notation","text_color":"grey"}
	]}}`

	violations := Card2Violations([]byte(brokenNote))
	want := []string{
		"ROOT -> body -> elements -> [0](tag: div): unknown property text_color",
		"ROOT -> body -> elements -> [0](tag: div): unknown property text_size",
	}
	if len(violations) != len(want) {
		t.Fatalf("violations = %v, want %v", violations, want)
	}
	for i, v := range violations {
		if v != want[i] {
			t.Errorf("violation[%d] = %q, want %q", i, v, want[i])
		}
	}

	// The same properties on the inner plain_text are the fix, and must pass.
	const fixedNote = `{"schema":"2.0","config":{"width_mode":"fill"},"body":{"elements":[
		{"tag":"div","text":{"tag":"plain_text","content":"点击选择答案","text_size":"notation","text_color":"grey"}}
	]}}`
	if violations := Card2Violations([]byte(fixedNote)); len(violations) > 0 {
		t.Errorf("the fixed note shape is reported as invalid: %v", violations)
	}
}

// TestCard2SchemaGuardCatchesATableColumnProperty pins the whitelist a table's
// columns are checked against. They carry no tag — unlike a column_set's
// columns — so before the table existed they were checked as if they were
// components, and the only thing that would have complained is Feishu.
func TestCard2SchemaGuardCatchesATableColumnProperty(t *testing.T) {
	const broken = `{"schema":"2.0","config":{"width_mode":"fill"},"body":{"elements":[
		{"tag":"table","columns":[{"name":"field","display_name":"字段","text_size":"notation"}],
		 "rows":[{"field":"集群"}],"page_size":10}
	]}}`

	violations := Card2Violations([]byte(broken))
	if len(violations) != 1 || !strings.Contains(violations[0], "unknown property text_size") {
		t.Fatalf("violations = %v, want the column's unknown property reported", violations)
	}

	// The documented column properties are the fix, and must pass.
	const fixed = `{"schema":"2.0","config":{"width_mode":"fill"},"body":{"elements":[
		{"tag":"table","columns":[{"name":"field","display_name":"字段","data_type":"text",
		 "width":"auto","horizontal_align":"left","vertical_align":"top"}],
		 "rows":[{"field":"集群"}],"page_size":10,"row_height":"auto",
		 "freeze_first_column":true,"header_style":{"background_style":"grey","bold":true}}
	]}}`
	if violations := Card2Violations([]byte(fixed)); len(violations) > 0 {
		t.Errorf("the fixed table shape is reported as invalid: %v", violations)
	}
}

// TestCard2SchemaGuardCatchesAnElementIDThePlatformWouldRefuse pins the rule that
// cost a real tenant a second card per pause. The id below is the shape that was
// live — a row id with a suffix glued on — and cardkit's answer to it was
//
//	300315: elementID format error. Only alphabets, numbers, and underscores are
//	allowed. It must start with an alphabet and not exceed 20 characters.
//	ElementID:decision_actions_1_last
//
// which arrives when the component is being added to a card that already exists,
// where the caller cannot rebuild its way out of it.
func TestCard2SchemaGuardCatchesAnElementIDThePlatformWouldRefuse(t *testing.T) {
	cases := []struct {
		name string
		id   string
		bad  bool
	}{
		{"the id a pause draws today", "decision_grant_1", false},
		{"an id exactly at the cap", "decision_grant_12345", false},
		{"one character over the cap", "decision_grant_123456", true},
		// 24 characters, and the reason every approval arrived as two cards.
		{"the suffix shape that shipped", "decision_actions_1_last", true},
		{"a digit first", "1_decision", true},
		{"a dash", "decision-row", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card := `{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"x","element_id":` +
				strconv.Quote(tc.id) + `}]}}`
			violations := Card2Violations([]byte(card))
			if tc.bad && len(violations) == 0 {
				t.Errorf("element_id %q is reported as legal", tc.id)
			}
			if !tc.bad && len(violations) > 0 {
				t.Errorf("element_id %q is reported as illegal: %v", tc.id, violations)
			}
			if tc.bad && !strings.Contains(violations[0], tc.id) {
				t.Errorf("violation %q does not name the id it is about", violations[0])
			}
		})
	}
}
