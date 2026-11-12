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
	"strings"
	"testing"
)

// TestParseCard pins what each card shape reads as. The shapes are the ones this
// service actually meets: what a producer sends in an event, what the message
// APIs answer once the card's own JSON is asked for, and — the case that used to
// read as nothing at all — the platform's degraded rendering.
func TestParseCard(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		source  CardSource
		want    string
		images  []string
		wantErr bool
	}{
		{
			name: "schema 2.0",
			raw: `{"schema":"2.0","header":{"title":{"tag":"plain_text","content":"工单"},"template":"red"},
			      "body":{"elements":[{"tag":"markdown","content":"第一行"},{"tag":"hr"},{"tag":"markdown","content":"第二行"}]}}`,
			want: "**工单**\n\n第一行\n\n---\n\n第二行",
		},
		{
			name: "schema 2.0 with a div's fields",
			raw: `{"schema":"2.0","body":{"elements":[{"tag":"div","fields":[
			      {"is_short":true,"text":{"tag":"lark_md","content":"**状态**：运行中"}},
			      {"is_short":true,"text":{"tag":"lark_md","content":"**重启**：3 次"}}]}]}}`,
			want: "**状态**：运行中\n**重启**：3 次",
		},
		{
			// The buttons of a row are laid out one per column; read, they are
			// one row again, which is how the card was written and how it is
			// rendered back.
			name: "a column_set of buttons is one row",
			raw: `{"schema":"2.0","body":{"elements":[{"tag":"column_set","columns":[
			      {"tag":"column","elements":[{"tag":"button","text":{"tag":"plain_text","content":"允许"}}]},
			      {"tag":"column","elements":[{"tag":"button","text":{"tag":"plain_text","content":"拒绝"}}]}]}]}}`,
			want: "[允许]  [拒绝]",
		},
		{
			// A button that opens a link carries the target, which is the part a
			// reader cannot guess from the label.
			name: "a link button keeps its target",
			raw: `{"schema":"2.0","body":{"elements":[{"tag":"button","text":{"tag":"plain_text","content":"查看"},
			      "behaviors":[{"type":"open_url","url":"https://example.com/x"}]}]}}`,
			want: "[查看](https://example.com/x)",
		},
		{
			name: "a table keeps its columns and rows",
			raw: `{"schema":"2.0","body":{"elements":[{"tag":"table",
			      "columns":[{"name":"k","display_name":"项目"},{"name":"v","display_name":"值"}],
			      "rows":[{"k":"状态","v":"运行中"}]}]}}`,
			want: "项目 | 值\n状态 | 运行中",
		},
		{
			// The raw-card shape writes a rich cell as an element under "data".
			name: "a rich table cell is read",
			raw: `{"schema":"2.0","body":{"elements":[{"tag":"table","columns":[{"name":"a","display_name":"A"}],
			      "rows":[{"a":{"data":{"tag":"markdown","content":"**粗**"}}}]}]}}`,
			want: "A\n**粗**",
		},
		{
			// A table whose columns are missing still holds content; dropping it
			// would lose the values to keep the arrangement.
			name: "a table without columns keeps its values",
			raw:  `{"schema":"2.0","body":{"elements":[{"tag":"table","rows":[{"k":"状态","v":"运行中"}]}]}}`,
			want: "k=状态  v=运行中",
		},
		{
			name: "a code block keeps its language",
			raw: `{"schema":"2.0","body":{"elements":[{"tag":"code_block","language":"bash","contents":[
			      {"contents":[{"content":"kubectl "},{"content":"get pods"}]},{"contents":[{"content":"ls"}]}]}]}}`,
			want: "```bash\nkubectl get pods\nls\n```",
		},
		{
			name: "a list keeps its items",
			raw: `{"schema":"2.0","body":{"elements":[{"tag":"list","items":[
			      {"elements":[{"tag":"markdown","content":"第一条"}]},
			      {"elements":[{"tag":"markdown","content":"第二条"}]}]}]}}`,
			want: "- 第一条\n- 第二条",
		},
		{
			name: "a collapsible panel keeps its title",
			raw: `{"schema":"2.0","body":{"elements":[{"tag":"collapsible_panel",
			      "header":{"title":{"tag":"plain_text","content":"详情"}},
			      "elements":[{"tag":"markdown","content":"里面的话"}]}]}}`,
			want: "详情\n\n里面的话",
		},
		{
			name: "legacy 1.0",
			raw: `{"header":{"title":{"tag":"plain_text","content":"结论"}},
			      "elements":[{"tag":"div","text":{"tag":"lark_md","content":"磁盘满了"}}]}`,
			want: "**结论**\n\n磁盘满了",
		},
		{
			// Card 1.0's button container, gone in 2.0 but still in every card
			// sent before the migration.
			name: "legacy 1.0 action container",
			raw: `{"elements":[{"tag":"action","actions":[
			      {"tag":"button","text":{"tag":"plain_text","content":"关闭"}}]}]}`,
			want: "[关闭]",
		},
		{
			// The footnote keeps its words and its picture: the picture is
			// content, and losing it to the note's shape would lose the point of
			// an alert that is mostly a chart.
			name: "legacy 1.0 note with a picture",
			raw: `{"elements":[{"tag":"note","elements":[{"tag":"plain_text","content":"备注"},
			      {"tag":"img","image_key":"img_n"}]}]}`,
			want: "备注\n[图片]", images: []string{"img_n"},
		},
		{
			name: "a select keeps its options",
			raw: `{"schema":"2.0","body":{"elements":[{"tag":"select_static",
			      "placeholder":{"tag":"plain_text","content":"选择值班组"},
			      "options":[{"text":{"tag":"plain_text","content":"一组"},"value":"1"},
			                 {"text":{"tag":"plain_text","content":"二组"},"value":"2"}]}]}}`,
			want: "选择值班组: 一组 | 二组",
		},
		{
			// The platform's own rendering of a card it cannot draw: content
			// lines grouped into rows, a screenshot, and a notice that is the
			// platform talking rather than the producer.
			name: "the degraded rendering",
			raw: `{"title":null,"elements":[
			      [{"tag":"img","image_key":"img_shot"},{"tag":"text","text":"请升级至最新版本客户端"}],
			      [{"tag":"text","text":"磁盘使用率 95%"}]]}`,
			want: "[图片]\n\n磁盘使用率 95%", images: []string{"img_shot"},
		},
		{
			name: "the degraded rendering keeps its title",
			raw:  `{"title":"告警","elements":[[{"tag":"text","text":"磁盘使用率 95%"}]]}`,
			want: "**告警**\n\n磁盘使用率 95%",
		},
		{
			// A picture anywhere on a card is a picture this process can fetch:
			// img_key in card 2.0, image_key in the degraded rendering.
			name:   "card 2.0 picture",
			raw:    `{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"看这张图"},{"tag":"img","img_key":"img_card"}]}}`,
			want:   "看这张图\n\n[图片]",
			images: []string{"img_card"},
		},
		{
			// A remote template: Lark keeps the copy, so what can be said is
			// what the card is and what went into it.
			name:   "a template card is named",
			source: CardSourceTemplate,
			raw: `{"type":"template","data":{"template_id":"AAqXVPGVTmWpR",
			      "template_variable":{"chat_name":"值班群","count":3}}}`,
			want: "**帮助卡片**\n\n模板 ID：AAqXVPGVTmWpR\n\n变量：\n- chat_name：值班群\n- count：3",
		},
		{
			// A template this service does not send is still self-describing.
			name:   "an unknown template is reported by its id",
			source: CardSourceTemplate,
			raw:    `{"type":"template","data":{"template_id":"AAqUnknown"}}`,
			want:   "**[卡片消息]**\n\n模板 ID：AAqUnknown",
		},
		{
			name:   "a card entity is reported by its id",
			source: CardSourceEntity,
			raw:    `{"type":"card","data":{"card_id":"c_1"}}`,
			want:   "**[卡片消息]**\n\n卡片实体 ID：c_1",
		},
		{
			// The wrapper some read APIs answer with; the card is inside it as a
			// string, not as an object.
			name: "the json_card envelope",
			raw:  `{"json_card":"{\"schema\":\"2.0\",\"body\":{\"elements\":[{\"tag\":\"markdown\",\"content\":\"包在里面\"}]}}"}`,
			want: "包在里面",
		},
		{
			// An element this package has no case for still contributes its
			// children — a container is layout, not content.
			name: "an unknown container is walked",
			raw: `{"schema":"2.0","body":{"elements":[{"tag":"interactive_container","elements":[
			      {"tag":"markdown","content":"里面的话"}]}]}}`,
			want: "里面的话",
		},
		{
			// One of the read APIs writes a component's fields one level down,
			// under "property", with the tag left beside it. Read as flat, such a
			// card comes back empty — which is the answer that looks like a card
			// nobody wrote, so the shape is flattened instead.
			name: "a component's fields under property",
			raw:  `{"schema":"2.0","body":{"elements":[{"tag":"markdown","property":{"content":"包在 property 里"}}]}}`,
			want: "包在 property 里",
		},
		{
			name: "the body's elements under property",
			raw: `{"schema":"2.0","body":{"tag":"body","property":{"elements":[
			      {"tag":"markdown","content":"深一层"}]}}}`,
			want: "深一层",
		},
		{
			// A container's own fields are not prose. Taking them would put
			// layout words in the middle of a sentence.
			name:    "a container's own content is not text",
			raw:     `{"schema":"2.0","body":{"elements":[{"tag":"column_set","horizontal_align":"right","columns":[]}]}}`,
			wantErr: true,
		},
		{name: "empty body", raw: "", wantErr: true},
		{name: "not JSON", raw: "not json at all", wantErr: true},
		{name: "an empty object", raw: `{}`, wantErr: true},
		{name: "a card with nothing in it", raw: `{"schema":"2.0","body":{"elements":[]}}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			card, source, err := ParseCard(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseCard() = %q, want an error", card.RenderText())
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCard() error = %v", err)
			}
			if source != tt.source {
				t.Errorf("source = %v, want %v", source, tt.source)
			}
			if got := card.RenderText(); got != tt.want {
				t.Errorf("RenderText() =\n%q\nwant\n%q", got, tt.want)
			}
			if got := card.ImageKeys(); !equalStrings(got, tt.images) {
				t.Errorf("ImageKeys() = %v, want %v", got, tt.images)
			}
		})
	}
}

// TestParseCardKeepsTheHeaderColour pins the one piece of a header that is not
// text: a card read back and rendered again should not lose its colour band.
func TestParseCardKeepsTheHeaderColour(t *testing.T) {
	card, _, err := ParseCard(`{"schema":"2.0","header":{"title":{"tag":"plain_text","content":"工单"},"template":"red"},
		"body":{"elements":[{"tag":"markdown","content":"x"}]}}`)
	if err != nil {
		t.Fatalf("ParseCard() error = %v", err)
	}
	if card.Header == nil || card.Header.Color != "red" {
		t.Errorf("Header = %+v, want the template colour kept", card.Header)
	}
}

// TestParseCardSurvivesDeepNesting is the bound on the walk. Cards nest for
// layout — a panel inside a column inside a form — and the depths real ones
// reach are shallow, so the bound is far above them; what it exists for is the
// shape that nests without end, which must come back as an answer rather than as
// a stack that ran out.
func TestParseCardSurvivesDeepNesting(t *testing.T) {
	// Ordinary nesting is read all the way down.
	const ordinary = 4
	card, _, err := ParseCard(nestedCard(ordinary))
	if err != nil {
		t.Fatalf("ParseCard(%d deep) error = %v", ordinary, err)
	}
	if !strings.Contains(card.RenderText(), "底") {
		t.Errorf("RenderText() = %q, want the content of a card nested %d deep", card.RenderText(), ordinary)
	}

	// Pathological nesting returns rather than recursing until it dies. What it
	// answers is beside the point; that it answers at all is the point.
	if _, _, err := ParseCard(nestedCard(200)); err == nil {
		t.Log("deep nesting read to the bottom; the bound is not reached")
	}
}

// nestedCard builds a card whose content sits under depth nested containers.
func nestedCard(depth int) string {
	body := `{"tag":"markdown","content":"底"}`
	for range depth {
		body = `{"tag":"column_set","columns":[{"tag":"column","elements":[` + body + `]}]}`
	}
	return `{"schema":"2.0","body":{"elements":[` + body + `]}}`
}

// TestRenderParseRoundTrip is the invariant the two halves of this package are
// built around: a card this repo renders, read back, renders to the same text.
// If the renderer and the parser ever drift, this is what notices — which is why
// the text a person sees in the TUI and the text a model reads in a quoted
// message can be the same string by construction.
//
// LIST ITEMS AND SELECTS ARE DELIBERATELY ABSENT. Neither survives the round trip
// exactly: a list item is laid out as a two-column row whose button is read back
// as a button row of its own, and a select's placeholder is laid out as a text
// object. The reading is right in both cases; only the byte-for-byte comparison
// would be wrong, and a test that lied about that would be worse than one that
// says so.
func TestRenderParseRoundTrip(t *testing.T) {
	card := NewCard().
		Title("工单", "red").
		Markdown("**问题**：服务不可用").
		Divider().
		Buttons(PrimaryBtn("关闭", "act:/close 1"), DefaultBtn("忽略", "act:/ignore 1")).
		Table(CardTable{
			Columns: []CardTableColumn{{Name: "k", DisplayName: "项目"}, {Name: "v", DisplayName: "值"}},
			Rows:    []map[string]any{{"k": "状态", "v": "运行中"}},
		}).
		Note("点击按钮执行操作").
		Build()

	raw := RenderCard(card, "feizao:oc_test:ou_user")

	parsed, source, err := ParseCard(raw)
	if err != nil {
		t.Fatalf("ParseCard(rendered) error = %v", err)
	}
	if source != CardSourceInline {
		t.Errorf("source = %v, want inline", source)
	}
	if got, want := parsed.RenderText(), card.RenderText(); got != want {
		t.Errorf("round trip changed the text:\ngot\n%q\nwant\n%q", got, want)
	}
}

// TestParseCardIsStableAcrossRenders pins that reading a card twice, or reading
// a card whose variables arrived in a different order, gives the same text: a
// prompt that reorders itself between two identical questions reads as two
// different cards.
func TestParseCardIsStableAcrossRenders(t *testing.T) {
	const raw = `{"type":"template","data":{"template_id":"AAqXVPGVTmWpR",
		"template_variable":{"b":"2","a":"1","c":"3"}}}`
	first, _, err := ParseCard(raw)
	if err != nil {
		t.Fatalf("ParseCard() error = %v", err)
	}
	second, _, err := ParseCard(raw)
	if err != nil {
		t.Fatalf("ParseCard() error = %v", err)
	}
	if first.RenderText() != second.RenderText() {
		t.Errorf("two reads disagree:\n%q\n%q", first.RenderText(), second.RenderText())
	}
	if !strings.Contains(first.RenderText(), "- a：1\n- b：2\n- c：3") {
		t.Errorf("variables = %q, want them in key order", first.RenderText())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
