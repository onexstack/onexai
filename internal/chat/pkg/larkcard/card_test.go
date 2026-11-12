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
	"testing"
)

func TestRenderCardMap(t *testing.T) {
	card := NewCard().
		Title("工单", "red").
		Markdown("**问题**：服务不可用").
		Divider().
		Buttons(PrimaryBtn("关闭", "act:/close 1")).
		Note("点击按钮执行操作").
		Build()

	m := RenderCardMap(card, "feishu:oc_test:ou_user")

	// Every card must be schema 2.0 and span the full chat width.
	if got := m["schema"]; got != CardSchemaV2 {
		t.Errorf("schema = %v, want %v", got, CardSchemaV2)
	}
	if got := m["config"].(map[string]any)["width_mode"]; got != WidthModeFill {
		t.Errorf("width_mode = %v, want %v", got, WidthModeFill)
	}

	if got := m["header"].(map[string]any)["template"]; got != "red" {
		t.Fatalf("header template = %v, want red", got)
	}

	elems := bodyElements(t, m)
	if len(elems) != 4 {
		t.Fatalf("elements count = %d, want 4", len(elems))
	}
	if elems[0]["tag"] != "markdown" {
		t.Errorf("first element tag = %v, want markdown", elems[0]["tag"])
	}
	if elems[1]["tag"] != "hr" {
		t.Errorf("second element tag = %v, want hr", elems[1]["tag"])
	}

	// Card 2.0 dropped the v1 `action` container, so the button row is a column_set.
	row := elems[2]
	if row["tag"] != "column_set" {
		t.Fatalf("button row tag = %v, want column_set", row["tag"])
	}
	actionRow := row["columns"].([]map[string]any)[0]["elements"].([]map[string]any)
	value := actionRow[0]["value"].(map[string]string)
	if value["action"] != "act:/close 1" {
		t.Errorf("action = %q, want act:/close 1", value["action"])
	}
	if value["session_key"] != "feishu:oc_test:ou_user" {
		t.Errorf("session_key = %q, want feishu:oc_test:ou_user", value["session_key"])
	}

	// Card 2.0 dropped the `note` component; it renders as notation-sized grey text.
	// Both style fields live on the inner plain_text: on the div itself Feishu
	// rejects the whole card with 200621 "unknown property: text_color".
	note := elems[3]
	if note["tag"] != "div" {
		t.Fatalf("note element = %v, want a div", note)
	}
	for _, prop := range []string{"text_size", "text_color"} {
		if _, ok := note[prop]; ok {
			t.Errorf("note div carries %s; it belongs on the inner plain_text (%v)", prop, note)
		}
	}
	text, ok := note["text"].(map[string]any)
	if !ok {
		t.Fatalf("note div text = %v, want a plain_text object", note["text"])
	}
	if text["tag"] != "plain_text" || text["text_size"] != "notation" || text["text_color"] != "grey" {
		t.Errorf("note text = %v, want notation-sized grey plain_text", text)
	}
}

// TestRenderCardBodyLayout covers the body's own spacing. A card without a
// header starts at the body's top padding, so this is the air between the top of
// the card and its first line — the gap a quote reply needs between the quoted
// message and the card's title. An unset layout must stay out of the JSON rather
// than render empty properties the platform has to interpret.
func TestRenderCardBodyLayout(t *testing.T) {
	rendered := RenderCardMap(NewCard().BodyLayout("16px 12px 12px 12px", "8px").Markdown("x").Build(), "sk")
	body := rendered["body"].(map[string]any)
	if body["padding"] != "16px 12px 12px 12px" {
		t.Errorf("body padding = %v", body["padding"])
	}
	if body["vertical_spacing"] != "8px" {
		t.Errorf("body vertical_spacing = %v", body["vertical_spacing"])
	}

	plain, err := json.Marshal(RenderCardMap(NewCard().Markdown("x").Build(), "sk"))
	if err != nil {
		t.Fatalf("marshal = %v", err)
	}
	for _, unwanted := range []string{`"padding"`, `"vertical_spacing"`} {
		if strings.Contains(string(plain), unwanted) {
			t.Errorf("a card with no layout set carries %s: %s", unwanted, plain)
		}
	}
}

// bodyElements returns a rendered card's body elements, the only place Card 2.0
// keeps them.
func bodyElements(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()

	body, ok := m["body"].(map[string]any)
	if !ok {
		t.Fatalf("rendered card missing body: %v", m)
	}
	elems, ok := body["elements"].([]map[string]any)
	if !ok {
		t.Fatalf("rendered card body missing elements: %v", body)
	}
	return elems
}

// TestRenderDeleteModeCardCard2 covers the delete-mode form, the one card that
// gets rewritten after the root map is built: the rewrite has to land the form
// under body.elements, and nothing may emit the v1-only `action`/`note` tags.
func TestRenderDeleteModeCardCard2(t *testing.T) {
	card := NewCard().
		TaggedNote("delete-mode-selected-count", "已选 1 项").
		Note("勾选后提交").
		ListItemBtn("☑ ▶ 会话一", "", "", "act:/delete-mode toggle s1").
		ListItemBtn("◻ ▶ 会话二", "", "", "act:/delete-mode toggle s2").
		Buttons(
			DangerBtn("删除", "act:/delete-mode confirm"),
			DefaultBtn("取消", "act:/delete-mode cancel"),
			PrimaryBtn("返回", "act:/menu"),
		).
		Build()

	m := RenderCardMap(card, "feishu:oc_test:ou_user")

	if m["schema"] != CardSchemaV2 {
		t.Errorf("schema = %v, want %v", m["schema"], CardSchemaV2)
	}
	if got := m["config"].(map[string]any)["width_mode"]; got != WidthModeFill {
		t.Errorf("width_mode = %v, want %v", got, WidthModeFill)
	}
	if _, ok := m["elements"]; ok {
		t.Error("Card 2.0 must not keep elements at the top level")
	}

	elems := bodyElements(t, m)

	var form map[string]any
	for _, e := range elems {
		switch e["tag"] {
		case "form":
			form = e
		case "action", "note":
			t.Errorf("Card 2.0 rejects the v1 %q element", e["tag"])
		}
	}
	if form == nil {
		t.Fatalf("no form element in %v", elems)
	}

	var checkers int
	for _, e := range form["elements"].([]map[string]any) {
		if e["tag"] == "checker" {
			checkers++
		}
	}
	if checkers != 2 {
		t.Errorf("checker count = %d, want 2", checkers)
	}
}

func TestRenderCardButtonDefaultType(t *testing.T) {
	card := NewCard().Buttons(DefaultBtn("ok", "act:/ok")).Build()
	m := RenderCardMap(card, "")

	row := bodyElements(t, m)[0]
	actionRow := row["columns"].([]map[string]any)[0]["elements"].([]map[string]any)
	if got := actionRow[0]["type"]; got != "default" {
		t.Errorf("button type = %v, want default", got)
	}
}

func TestRenderCardMarshalRoundTrip(t *testing.T) {
	card := NewCard().Markdown("hello").Build()
	raw := RenderCard(card, "")

	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("render produced invalid JSON: %v", err)
	}
	body, ok := decoded["body"].(map[string]any)
	if !ok {
		t.Fatalf("rendered JSON missing body: %s", raw)
	}
	if _, ok := body["elements"]; !ok {
		t.Fatalf("rendered JSON body missing elements: %s", raw)
	}
}

func TestDeleteModeCheckerNameRoundTrip(t *testing.T) {
	name := deleteModeCheckerName("oc_session_123")
	id, ok := parseDeleteModeCheckerName(name)
	if !ok {
		t.Fatalf("parseDeleteModeCheckerName returned ok=false for %q", name)
	}
	if id != "oc_session_123" {
		t.Errorf("round-trip id = %q, want oc_session_123", id)
	}

	if _, ok := parseDeleteModeCheckerName("not_a_checker"); ok {
		t.Errorf("non-checker name should not parse")
	}
}

func TestCollectDeleteModeSelectedFromFormValue(t *testing.T) {
	tests := []struct {
		name  string
		value map[string]any
		want  []string
	}{
		{
			name:  "empty",
			value: map[string]any{},
			want:  nil,
		},
		{
			name: "two selected one unchecked",
			value: map[string]any{
				deleteModeCheckerName("b"): true,
				deleteModeCheckerName("a"): "true",
				deleteModeCheckerName("c"): false,
			},
			want: []string{"a", "b"},
		},
		{
			name: "duplicate session ids",
			value: map[string]any{
				deleteModeCheckerName("x"): "1",
				deleteModeCheckerName("x"): "on",
			},
			want: []string{"x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CollectDeleteModeSelectedFromFormValue(tt.value)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestIsTruthyFormValue(t *testing.T) {
	tests := []struct {
		in   any
		want bool
	}{
		{true, true}, {false, false},
		{"true", true}, {"1", true}, {"yes", true}, {"on", true},
		{"false", false}, {"0", false}, {"", false},
		{float64(1), true}, {float64(0), false},
		{int(1), true}, {int(0), false},
		{int64(1), true}, {int64(0), false},
		{[]string{}, false}, {nil, false},
	}
	for _, tt := range tests {
		if got := isTruthyFormValue(tt.in); got != tt.want {
			t.Errorf("isTruthyFormValue(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestRenderTextFallback(t *testing.T) {
	card := NewCard().Title("T", "blue").Markdown("body").Buttons(DefaultBtn("ok", "act:/ok")).Build()
	text := card.RenderText()
	if !strings.Contains(text, "**T**") {
		t.Errorf("RenderText missing header, got %q", text)
	}
	if !strings.Contains(text, "[ok]") {
		t.Errorf("RenderText missing button hint, got %q", text)
	}
}

// TestRenderCardTable pins the native table shape: columns carry no tag (unlike
// a column_set's), rows are keyed by column name, and the header is styled grey
// so the key column reads as a label rather than another row.
func TestRenderCardTable(t *testing.T) {
	card := NewCard().Table(CardTable{
		Columns: []CardTableColumn{
			{Name: "field", DisplayName: "字段", DataType: "text", HorizontalAlign: "left"},
			{Name: "value", DisplayName: "值", DataType: "lark_md"},
		},
		Rows:              []map[string]any{{"field": "集群", "value": "fz-prod"}},
		PageSize:          10,
		RowHeight:         "auto",
		FreezeFirstColumn: true,
	}).Build()

	table := bodyElements(t, RenderCardMap(card, "sk"))[0]
	if table["tag"] != "table" {
		t.Fatalf("element tag = %v, want table", table["tag"])
	}
	if table["page_size"] != 10 || table["row_height"] != "auto" || table["freeze_first_column"] != true {
		t.Errorf("table = %v, want the paging and row-height properties", table)
	}
	header, ok := table["header_style"].(map[string]any)
	if !ok || header["background_style"] != "grey" {
		t.Errorf("header_style = %v, want a grey header", table["header_style"])
	}

	cols := table["columns"].([]map[string]any)
	if len(cols) != 2 {
		t.Fatalf("columns = %v, want 2", cols)
	}
	for _, c := range cols {
		if _, ok := c["tag"]; ok {
			t.Errorf("column %v carries a tag; a table's columns are plain objects", c)
		}
	}
	if cols[1]["data_type"] != "lark_md" || cols[0]["data_type"] != "text" {
		t.Errorf("column data types = %v/%v, want text and lark_md", cols[0]["data_type"], cols[1]["data_type"])
	}
	if cols[0]["horizontal_align"] != "left" {
		t.Errorf("column alignment = %v, want left", cols[0]["horizontal_align"])
	}
	rows := table["rows"].([]map[string]any)
	if len(rows) != 1 || rows[0]["field"] != "集群" || rows[0]["value"] != "fz-prod" {
		t.Errorf("rows = %v, want the one key/value row", rows)
	}

	// The plain-text fallback has no table syntax, so it flattens the cells.
	text := card.RenderText()
	for _, want := range []string{"字段 | 值", "集群 | fz-prod"} {
		if !strings.Contains(text, want) {
			t.Errorf("RenderText = %q, want it to contain %q", text, want)
		}
	}
}

// TestCardTableIsDroppedWhenEmpty pins that the builder never emits a table the
// platform would refuse: no columns or no rows means no element at all.
func TestCardTableIsDroppedWhenEmpty(t *testing.T) {
	cases := map[string]CardTable{
		"no columns": {Rows: []map[string]any{{"field": "x"}}},
		"no rows":    {Columns: []CardTableColumn{{Name: "field"}}},
		"no name":    {Columns: []CardTableColumn{{DisplayName: "字段"}}, Rows: []map[string]any{{"field": "x"}}},
	}
	for name, table := range cases {
		t.Run(name, func(t *testing.T) {
			card := NewCard().Markdown("x").Table(table).Build()
			for _, elem := range card.Elements {
				if _, ok := elem.(CardTable); ok {
					t.Fatalf("an empty table was appended: %+v", table)
				}
			}
		})
	}
}
