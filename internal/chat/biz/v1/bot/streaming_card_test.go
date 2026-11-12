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

package bot

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// collectElementIDs counts every element_id literal that appears in a rendered
// card, at any nesting depth. The streaming path addresses elements by these
// ids, so they are wire contract, not implementation detail — and the count
// matters as much as the names: two elements sharing an id are rejected when the
// first update targets them, not when the card is created.
func collectElementIDs(t *testing.T, cardJSON []byte) map[string]int {
	t.Helper()

	var parsed any
	if err := json.Unmarshal(cardJSON, &parsed); err != nil {
		t.Fatalf("card is not valid JSON: %v", err)
	}

	found := map[string]int{}
	var walk func(node any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if id, ok := v["element_id"].(string); ok {
				found[id]++
			}
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
	return found
}

// cardElements decodes a card payload's body elements.
func cardElements(t *testing.T, cardJSON []byte) []map[string]any {
	t.Helper()

	var doc struct {
		Body struct {
			Elements []map[string]any `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal(cardJSON, &doc); err != nil {
		t.Fatalf("payload is not valid JSON: %v\n%s", err, cardJSON)
	}
	if len(doc.Body.Elements) == 0 {
		t.Fatal("card has no elements")
	}
	return doc.Body.Elements
}

// stopRowOf returns the card's stop row: the column_set parked in the bottom-right
// corner. It is the last element, below the answer, so it stays at the foot of the
// card while the answer above it streams and grows — where the progress line it
// used to share a row with no longer has any say in where it sits.
func stopRowOf(t *testing.T, cardJSON []byte) map[string]any {
	t.Helper()

	elements := cardElements(t, cardJSON)
	row := elements[len(elements)-1]
	if row["element_id"] != stopRowCardElementID {
		t.Fatalf("last element_id = %v, want %q (the stop row belongs below the answer)",
			row["element_id"], stopRowCardElementID)
	}
	if row["horizontal_align"] != "right" {
		t.Errorf("stop row horizontal_align = %v, want right", row["horizontal_align"])
	}
	return row
}

// stopButtonOf returns the stop button inside the stop row, and the callback value
// its click will carry.
func stopButtonOf(t *testing.T, row map[string]any) map[string]any {
	t.Helper()

	columns, ok := row["columns"].([]any)
	if !ok || len(columns) != 1 {
		t.Fatalf("stop row columns = %v, want exactly 1 — the button alone, now that the progress "+
			"line is a top-level element of its own", row["columns"])
	}
	column := columns[0].(map[string]any)
	if column["width"] != "auto" {
		t.Errorf("stop row column width = %v, want auto so the button keeps its natural size", column["width"])
	}

	buttonEl, ok := column["elements"].([]any)
	if !ok || len(buttonEl) != 1 {
		t.Fatalf("button column elements = %v, want exactly 1", column)
	}
	button, ok := buttonEl[0].(map[string]any)
	if !ok {
		t.Fatalf("button column element is not an object: %v", buttonEl[0])
	}
	if button["element_id"] != stopCardElementID {
		t.Errorf("button element_id = %v, want %q", button["element_id"], stopCardElementID)
	}
	return button
}

// TestStreamingCardToolPanelWrapsTheProgressLane pins where the live tool list
// lives: inside the same house-styled panel the final card draws, expanded so a
// running step stays visible, with the progress markdown one level down — still
// addressed by the same element_id, which is what keeps UpdateCardProgressText
// working. It used to be a bare top-level markdown element, which is why the
// running card and the finished card did not look like the same component.
func TestStreamingCardToolPanelWrapsTheProgressLane(t *testing.T) {
	stop := stopButton{RunID: "run-1", SessionKey: "oc_chat", ChatType: v1.ChatTypeGroup}

	got, err := buildStreamingCardJSON("", "text", false, stop, cardkitPrint{})
	if err != nil {
		t.Fatalf("buildStreamingCardJSON returned error: %v", err)
	}

	elements := cardElements(t, got)
	if len(elements) != 3 {
		t.Fatalf("elements = %d, want 3 (tool panel, answer, stop row)", len(elements))
	}
	panel := elements[0]
	if panel["tag"] != "collapsible_panel" {
		t.Fatalf("first element tag = %v, want collapsible_panel", panel["tag"])
	}
	if panel["expanded"] != true {
		t.Errorf("tool panel expanded = %v, want true (a running step must stay visible)", panel["expanded"])
	}
	if title := panelTitle(panel); title != toolLivePanelTitle {
		t.Errorf("tool panel title = %q, want %q", title, toolLivePanelTitle)
	}
	if id := elements[1]["element_id"]; id != streamingCardElementID {
		t.Errorf("element 1 element_id = %v, want %q", id, streamingCardElementID)
	}
	if id := elements[2]["element_id"]; id != stopRowCardElementID {
		t.Errorf("element 2 element_id = %v, want %q", id, stopRowCardElementID)
	}

	inner := panelInnerElements(t, panel)
	if len(inner) != 1 {
		t.Fatalf("tool panel holds %d elements, want exactly 1", len(inner))
	}
	if inner[0]["tag"] != "markdown" {
		t.Errorf("progress element tag = %v, want markdown (cardkit updates it by id)", inner[0]["tag"])
	}
	if id := inner[0]["element_id"]; id != progressCardElementID {
		t.Errorf("progress element_id = %v, want %q (UpdateCardProgressText targets it)", id, progressCardElementID)
	}
}

// panelTitle reads a panel header's title text.
func panelTitle(panel map[string]any) string {
	header, ok := panel["header"].(map[string]any)
	if !ok {
		return ""
	}
	title, ok := header["title"].(map[string]any)
	if !ok {
		return ""
	}
	content, _ := title["content"].(string)
	return content
}

// panelInnerElements returns a panel's child elements, failing the test if the
// panel carries none.
func panelInnerElements(t *testing.T, panel map[string]any) []map[string]any {
	t.Helper()
	raw, ok := panel["elements"].([]any)
	if !ok {
		t.Fatalf("panel elements = %v, want an array", panel["elements"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		el, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("panel child = %v, want an object", item)
		}
		out = append(out, el)
	}
	return out
}

// TestLiveAndFinalReasoningPanelsShareOneStyle is the contract this whole change
// exists to establish: the panel the reader watches fill while the turn runs and
// the panel they open afterwards are one component, so the card does not visibly
// re-skin itself at the moment it closes.
//
// Nothing else ties the two packages together. They build the panel from two
// different sides — the live one from this package's element ids, the final one
// from larkcard's step renderer — which is precisely how they drifted apart the
// first time, when the live side was written against a field name card 2.0 does
// not have and the difference was invisible to every test on both sides.
//
// The title is held to the same standard, which is why this test wants them
// equal rather than merely non-empty. The live panel used to be titled
// 「深度思考中…」 (thinking…): a claim about what the turn was doing that stopped being true
// the moment the turn parked on a decision, and that the card then visibly
// retracted when it closed. A title that describes the panel — 「深度思考过程」 (the thinking process)
// — is true for as long as the panel exists, so the live and final cards can
// and do agree on it.
func TestLiveAndFinalReasoningPanelsShareOneStyle(t *testing.T) {
	live, err := buildStreamingCardJSON("", "text", true, stopButton{}, cardkitPrint{})
	if err != nil {
		t.Fatalf("buildStreamingCardJSON returned error: %v", err)
	}
	livePanel := cardElements(t, live)[0]

	final := larkcard.BuildRichCard(larkcard.RichCardStatusDone, "推理内容", nil, "答", false, "")
	var doc struct {
		Body struct {
			Elements []map[string]any `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(final), &doc); err != nil {
		t.Fatalf("final card is not valid JSON: %v\n%s", err, final)
	}
	if len(doc.Body.Elements) == 0 {
		t.Fatal("final card has no elements")
	}
	finalPanel := doc.Body.Elements[0]
	if title := panelTitle(finalPanel); title != "深度思考过程" {
		t.Fatalf("final reasoning panel title = %q, want 深度思考过程", title)
	}

	// Every key that decides how the panel looks. element_id and the body are the
	// two things the two callers are meant to differ on, so they are not compared.
	styleKeys := []string{"tag", "background_color", "border", "padding", "vertical_spacing"}
	for _, key := range styleKeys {
		if !reflect.DeepEqual(livePanel[key], finalPanel[key]) {
			t.Errorf("panel %s differs: live = %v, final = %v", key, livePanel[key], finalPanel[key])
		}
	}
	if liveTitle, finalTitle := panelTitle(livePanel), panelTitle(finalPanel); liveTitle != finalTitle {
		t.Errorf("panel titles = %q (live) and %q (final), want the same title on both sides", liveTitle, finalTitle)
	}
	if liveTag, finalTag := titleTagOf(livePanel), titleTagOf(finalPanel); liveTag != finalTag {
		t.Errorf("header title tag differs: live = %v, final = %v", liveTag, finalTag)
	}
	// background_style is the card-1.0 field name that made the live panel render
	// transparent and borderless. It is not a card 2.0 field, so it is not a value
	// that can be compared — it is a key that must not come back.
	for _, panel := range []map[string]any{livePanel, finalPanel} {
		if _, ok := panel["background_style"]; ok {
			t.Error("panel carries background_style, which card 2.0 ignores; the panel will render unstyled")
		}
	}
}

// titleTagOf reads a panel header title's tag, which has to be plain_text on both
// sides — a header title is not markdown on either.
func titleTagOf(panel map[string]any) any {
	header, _ := panel["header"].(map[string]any)
	title, _ := header["title"].(map[string]any)
	return title["tag"]
}

// callbackValues returns every callback value a button carries, keyed by where
// it sits: "value" for the top-level property, "behaviors" for the callback
// behavior. It is a set rather than one value because the button sends both.
func callbackValues(button map[string]any) map[string]map[string]any {
	values := map[string]map[string]any{}
	if v, ok := button["value"].(map[string]any); ok {
		values["value"] = v
	}
	if behaviors, ok := button["behaviors"].([]any); ok {
		for _, b := range behaviors {
			behavior, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if v, ok := behavior["value"].(map[string]any); ok {
				values["behaviors"] = v
			}
		}
	}
	return values
}

// TestCardEntityContent locks the envelope Feishu requires when delivering a
// cardkit card entity via an interactive message. A bare {"card_id":...} is
// parsed as card JSON and rejected with 200621 "parse card json err".
func TestCardEntityContent(t *testing.T) {
	tests := []struct {
		name       string
		cardID     string
		wantType   string
		wantCardID string
	}{
		{
			name:       "regular card id",
			cardID:     "7371713483664506900",
			wantType:   "card",
			wantCardID: "7371713483664506900",
		},
		{
			name:       "empty card id still wrapped",
			cardID:     "",
			wantType:   "card",
			wantCardID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cardEntityContent(tt.cardID)
			if err != nil {
				t.Fatalf("cardEntityContent(%q) returned error: %v", tt.cardID, err)
			}

			var parsed struct {
				Type string `json:"type"`
				Data *struct {
					CardID string `json:"card_id"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(got), &parsed); err != nil {
				t.Fatalf("cardEntityContent(%q) = %s, not valid JSON: %v", tt.cardID, got, err)
			}
			if parsed.Type != tt.wantType {
				t.Errorf("type = %q, want %q (content: %s)", parsed.Type, tt.wantType, got)
			}
			if parsed.Data == nil {
				t.Fatalf("data wrapper missing (content: %s)", got)
			}
			if parsed.Data.CardID != tt.wantCardID {
				t.Errorf("data.card_id = %q, want %q (content: %s)", parsed.Data.CardID, tt.wantCardID, got)
			}

			// Guard against a wrapper whose field names only survive as JSON
			// tags while the raw payload drifts from what Feishu expects.
			if !strings.Contains(got, `"type":"card"`) {
				t.Errorf("content %s missing literal \"type\":\"card\"", got)
			}
			if !strings.Contains(got, `"data":{`) {
				t.Errorf("content %s missing literal \"data\" object", got)
			}
		})
	}
}

// wantInitialCardJSON is the cardkit 2.0 payload for a plain streaming turn
// (no reasoning panel, no stop button). It is the baseline every other variant
// is a delta against: Feishu validates this document, and cardkit reports a
// malformed element_id or sequence only at runtime, so the shape is pinned here
// rather than discovered in production.
//
// update_multi is in the golden because it is a requirement rather than a
// preference: the element-level writes (CardElement.Create/Delete/Settings) are
// rejected on a card whose config has update_multi false, and the decision block
// is written that way. Cardkit would refuse every pause on a card built without
// it, and nothing short of a golden catches its removal.
const wantInitialCardJSON = `{
  "schema": "2.0",
  "config": {
    "streaming_mode": true,
    "summary": {"content": "正在处理您的请求 ..."},
    "enable_forward": true,
    "width_mode": "fill",
    "update_multi": true,
    "streaming_config": {
      "print_frequency_ms": {"default": 50},
      "print_step": {"default": 2},
      "print_strategy": "fast"
    }
  },
  "body": {
    "elements": [
      {
        "tag": "collapsible_panel",
        "expanded": true,
        "background_color": "grey",
        "header": {"title": {"tag": "plain_text", "content": "工具调用"}},
        "border": {"color": "grey"},
        "vertical_spacing": "8px",
        "padding": "4px 8px",
        "elements": [
          {"tag": "markdown", "content": "", "element_id": "markdown_progress"}
        ]
      },
      {"tag": "markdown", "content": "https://example.com/init", "element_id": "main_text"}
    ]
  }
}`

// TestStreamingCardInitialJSONGolden locks the streaming card's wire format
// byte-for-byte at the semantic level: the actual payload is decoded to `any`
// and compared against the decoded golden, so key order and whitespace are free
// but every field, value and element ordering is fixed.
func TestStreamingCardInitialJSONGolden(t *testing.T) {
	got, err := buildStreamingCardJSON("", "https://example.com/init", false, stopButton{}, cardkitPrint{})
	if err != nil {
		t.Fatalf("buildStreamingCardJSON returned error: %v", err)
	}

	var gotDoc, wantDoc any
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("payload is not valid JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(wantInitialCardJSON), &wantDoc); err != nil {
		t.Fatalf("golden is not valid JSON: %v", err)
	}

	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("streaming card payload drifted from the golden.\n got: %s\nwant: %s", got, wantInitialCardJSON)
	}
}

// TestStreamingCardReasoningPanelIsFirstAndHouseStyled pins the reasoning
// variant: when the session asked for reasoning the panel is prepended (it
// renders above everything, because that is the order the turn unfolds), starts
// collapsed, and carries its own inner markdown element under a derived id.
//
// It also pins the styling, which is the part that was broken. The panel used to
// carry background_style — a card 1.0 field name that card 2.0 silently ignores —
// so it rendered transparent and borderless while the final card's panel was grey
// and bordered. Feishu accepts the card either way, so only a test like this one
// can hold the two sides together.
func TestStreamingCardReasoningPanelIsFirstAndHouseStyled(t *testing.T) {
	got, err := buildStreamingCardJSON("", "text", true, stopButton{}, cardkitPrint{})
	if err != nil {
		t.Fatalf("buildStreamingCardJSON returned error: %v", err)
	}

	var doc struct {
		Body struct {
			Elements []map[string]any `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("payload is not valid JSON: %v\n%s", err, got)
	}
	if len(doc.Body.Elements) != 3 {
		t.Fatalf("elements = %d, want 3 (reasoning + tools + answer)", len(doc.Body.Elements))
	}

	panel := doc.Body.Elements[0]
	if panel["element_id"] != reasoningCardElementID {
		t.Errorf("first element_id = %v, want %q (panel must render above the answer)",
			panel["element_id"], reasoningCardElementID)
	}
	if panel["tag"] != "collapsible_panel" {
		t.Errorf("first element tag = %v, want collapsible_panel", panel["tag"])
	}
	if panel["expanded"] != false {
		t.Errorf("reasoning panel expanded = %v, want false (collapsed by default)", panel["expanded"])
	}
	if title := panelTitle(panel); title != reasoningLivePanelTitle {
		t.Errorf("reasoning panel title = %q, want %q", title, reasoningLivePanelTitle)
	}
	for key, want := range map[string]any{
		"background_color": "grey",
		"border":           map[string]any{"color": "grey"},
		"padding":          "4px 8px",
		"vertical_spacing": "8px",
	} {
		if !reflect.DeepEqual(panel[key], want) {
			t.Errorf("reasoning panel %s = %v, want %v", key, panel[key], want)
		}
	}
	if _, ok := panel["background_style"]; ok {
		t.Error("reasoning panel carries background_style, which card 2.0 ignores; it renders unstyled")
	}

	inner := panelInnerElements(t, panel)
	if len(inner) != 1 {
		t.Fatalf("reasoning panel holds %d elements, want exactly 1", len(inner))
	}
	if inner[0]["element_id"] != reasoningCardElementID+"_text" {
		t.Errorf("inner element_id = %v, want %q (UpdateCardReasoningText targets it)",
			inner[0]["element_id"], reasoningCardElementID+"_text")
	}
	if inner[0]["tag"] != "markdown" {
		t.Errorf("inner element tag = %v, want markdown", inner[0]["tag"])
	}
	if inner[0]["content"] != reasoningLiveInitialText {
		t.Errorf("inner element content = %v, want %q", inner[0]["content"], reasoningLiveInitialText)
	}
}

// TestStreamingCardStopButtonCarriesItsConversation pins the stop button's
// callback value. It is the only thing that tells the handler which run to
// cancel and which conversation that run belongs to — and the conversation is
// the part the platform does not report back: a group's key is its chat id,
// while the callback leads with the clicker's open id. Neither a missing run_id
// nor a missing session_key announces itself; both turn the button into a click
// that appears to do nothing.
func TestStreamingCardStopButtonCarriesItsConversation(t *testing.T) {
	const (
		runID      = "run-abc-123"
		sessionKey = "oc_group_chat"
	)
	stop := stopButton{RunID: runID, SessionKey: sessionKey, ChatType: v1.ChatTypeGroup}

	got, err := buildStreamingCardJSON("", "text", false, stop, cardkitPrint{})
	if err != nil {
		t.Fatalf("buildStreamingCardJSON returned error: %v", err)
	}

	button := stopButtonOf(t, stopRowOf(t, got))

	// Glyph only: the label is the button, and a word beside it is what the user
	// asked to be rid of.
	text, ok := button["text"].(map[string]any)
	if !ok || text["content"] != stopButtonLabel {
		t.Errorf("button text = %v, want %q and nothing else", button["text"], stopButtonLabel)
	}

	values := callbackValues(button)
	if len(values) == 0 {
		t.Fatalf("button carries no callback value at all: %v", button)
	}
	for where, value := range values {
		if value["action"] != StopCardAction {
			t.Errorf("%s action = %v, want %q", where, value["action"], StopCardAction)
		}
		if value["run_id"] != runID {
			t.Errorf("%s run_id = %v, want %q", where, value["run_id"], runID)
		}
		if value["session_key"] != sessionKey {
			t.Errorf("%s session_key = %v, want %q (without it a group click resolves to the clicker)",
				where, value["session_key"], sessionKey)
		}
		if value["chat_type"] != v1.ChatTypeGroup {
			t.Errorf("%s chat_type = %v, want %q", where, value["chat_type"], v1.ChatTypeGroup)
		}
	}

	// Both forms must agree: they are the same click, and a handler that reads
	// one of them cannot be told apart from a handler that reads the other.
	if len(values) > 1 && !reflect.DeepEqual(values["value"], values["behaviors"]) {
		t.Errorf("the two callback values disagree:\n value: %v\n behaviors: %v", values["value"], values["behaviors"])
	}
}

// TestStopRowPinsTheButtonToTheBottomRightEdge pins where the stop button sits: the
// last element of the body, in a row aligned right, with the button right-aligned
// inside it.
//
// The row is no longer shared with the progress line, and that is what changed its
// alignment requirements. While the two were one row the set had to stay
// left-aligned — aligning it right dragged the live tool panel to the right edge of
// the card — and flex_mode "stretch" was what kept the button from trailing the tool
// text. With the button alone on its row there is nothing to trade width with, so
// the alignment moves to the row itself and the drift it guarded against cannot
// happen: the row's own width is the button's.
func TestStopRowPinsTheButtonToTheBottomRightEdge(t *testing.T) {
	stop := stopButton{RunID: "run-abc-123", SessionKey: "oc_group_chat", ChatType: v1.ChatTypeGroup}

	got, err := buildStreamingCardJSON("", "text", false, stop, cardkitPrint{})
	if err != nil {
		t.Fatalf("buildStreamingCardJSON returned error: %v", err)
	}
	row := stopRowOf(t, got)

	if _, ok := row["flex_mode"]; ok {
		t.Errorf("stop row flex_mode = %v, want it unset: with one column there is no width to trade", row["flex_mode"])
	}
	if row["horizontal_align"] != "right" {
		t.Errorf("stop row horizontal_align = %v, want right", row["horizontal_align"])
	}

	columns, ok := row["columns"].([]any)
	if !ok || len(columns) != 1 {
		t.Fatalf("stop row columns = %v, want the button's column alone", row["columns"])
	}
	buttonColumn := columns[0].(map[string]any)
	if buttonColumn["width"] != "auto" {
		t.Errorf("button column width = %v, want auto: a weighted button column would stretch the glyph",
			buttonColumn["width"])
	}
	if buttonColumn["horizontal_align"] != "right" {
		t.Errorf("button column horizontal_align = %v, want right: the row is right-aligned, and the "+
			"button keeps the edge even if the platform hands the column more width than it needs",
			buttonColumn["horizontal_align"])
	}
}

// TestStopButtonForOmitsAnUnknownChatType pins that the button writes no
// chat_type rather than a guess. The resolver trusts the field, so a guessed
// "group" on a direct message would send the reply to a chat id that is not one.
func TestStopButtonForOmitsAnUnknownChatType(t *testing.T) {
	tests := []struct {
		name     string
		recv     *v1.Receiver
		wantKey  string
		wantType string
	}{
		{
			name:     "group chat",
			recv:     &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: "oc_g"},
			wantKey:  "oc_g",
			wantType: v1.ChatTypeGroup,
		},
		{
			name:     "direct message",
			recv:     &v1.Receiver{Type: v1.ReceiverTypeOpenID, Id: "ou_u"},
			wantKey:  "ou_u",
			wantType: v1.ChatTypeP2P,
		},
		{
			name:    "unknown receiver type",
			recv:    &v1.Receiver{Type: "", Id: "oc_mystery"},
			wantKey: "oc_mystery",
		},
		{
			name: "no receiver",
			recv: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stop := stopButtonFor(tt.recv, "run-1")
			if stop.RunID != "run-1" {
				t.Errorf("RunID = %q, want run-1", stop.RunID)
			}
			if stop.SessionKey != tt.wantKey {
				t.Errorf("SessionKey = %q, want %q", stop.SessionKey, tt.wantKey)
			}
			if stop.ChatType != tt.wantType {
				t.Errorf("ChatType = %q, want %q", stop.ChatType, tt.wantType)
			}
		})
	}
}

// TestStreamingCardElementIDInvariant guards the card's element ids against
// drift. Every id the streaming path can emit must be one of the declared
// constants, and the answer element must stay identical to the id the final
// rich card renders into — otherwise the closing render would write a different
// element than the one the user watched stream, leaving a stale answer behind.
func TestStreamingCardElementIDInvariant(t *testing.T) {
	if streamingCardElementID != larkcard.RichCardMainTextElementID {
		t.Errorf("streamingCardElementID = %q, but larkcard.RichCardMainTextElementID = %q; "+
			"the streamed answer element and the finalized card element must be the same",
			streamingCardElementID, larkcard.RichCardMainTextElementID)
	}

	declared := map[string]bool{
		streamingCardElementID:           true,
		progressCardElementID:            true,
		reasoningCardElementID:           true,
		reasoningCardElementID + "_text": true,
		stopCardElementID:                true,
		stopRowCardElementID:             true,
	}

	// Render every variant so the scan sees the union of ids that can appear.
	for _, tc := range []struct {
		name          string
		showReasoning bool
		stop          stopButton
	}{
		{name: "plain"},
		{name: "reasoning", showReasoning: true},
		{name: "stop", stop: stopButton{RunID: "run-1", SessionKey: "oc_1", ChatType: v1.ChatTypeGroup}},
		{name: "both", showReasoning: true, stop: stopButton{RunID: "run-1", SessionKey: "ou_1", ChatType: v1.ChatTypeP2P}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := buildStreamingCardJSON("title", "text", tc.showReasoning, tc.stop, cardkitPrint{})
			if err != nil {
				t.Fatalf("buildStreamingCardJSON returned error: %v", err)
			}
			ids := collectElementIDs(t, payload)
			for id := range ids {
				if !declared[id] {
					t.Errorf("card emits element_id %q, which no declared constant covers; "+
						"the streaming path cannot address it", id)
				}
			}
			// The lanes the turn streams into must appear exactly once: a second
			// element with the same id makes the first update to it fail, which
			// shows up as a card that stops updating halfway through an answer.
			streamed := []string{progressCardElementID, streamingCardElementID}
			if tc.showReasoning {
				streamed = append(streamed, reasoningCardElementID)
			}
			for _, id := range streamed {
				if ids[id] != 1 {
					t.Errorf("element_id %q appears %d times, want exactly 1", id, ids[id])
				}
			}
			if !tc.showReasoning && ids[reasoningCardElementID] != 0 {
				t.Errorf("reasoning panel present without reasoning requested (id appears %d times)",
					ids[reasoningCardElementID])
			}
		})
	}
}

// TestCardkitPrintHintsAreConfigurable pins that the three cadence hints reach
// the wire, and that leaving them unset restores the platform defaults the golden
// test above pins — a zero value must never mean "a card that does not animate".
func TestCardkitPrintHintsAreConfigurable(t *testing.T) {
	read := func(t *testing.T, print cardkitPrint) map[string]any {
		t.Helper()
		payload, err := buildStreamingCardJSON("", "text", false, stopButton{}, print)
		if err != nil {
			t.Fatalf("buildStreamingCardJSON returned error: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(payload, &doc); err != nil {
			t.Fatalf("payload is not valid JSON: %v", err)
		}
		config, _ := doc["config"].(map[string]any)
		streaming, _ := config["streaming_config"].(map[string]any)
		return streaming
	}

	t.Run("configured", func(t *testing.T) {
		streaming := read(t, cardkitPrint{FrequencyMs: 120, Step: 5, Strategy: "delay"})
		if got := streaming["print_frequency_ms"].(map[string]any)["default"]; got != float64(120) {
			t.Errorf("print_frequency_ms = %v, want 120", got)
		}
		if got := streaming["print_step"].(map[string]any)["default"]; got != float64(5) {
			t.Errorf("print_step = %v, want 5", got)
		}
		if got := streaming["print_strategy"]; got != "delay" {
			t.Errorf("print_strategy = %v, want delay", got)
		}
	})

	t.Run("unset falls back to the platform defaults", func(t *testing.T) {
		streaming := read(t, cardkitPrint{})
		if got := streaming["print_frequency_ms"].(map[string]any)["default"]; got != float64(defaultPrintFrequencyMs) {
			t.Errorf("print_frequency_ms = %v, want %d", got, defaultPrintFrequencyMs)
		}
		if got := streaming["print_step"].(map[string]any)["default"]; got != float64(defaultPrintStep) {
			t.Errorf("print_step = %v, want %d", got, defaultPrintStep)
		}
		if got := streaming["print_strategy"]; got != defaultPrintStrategy {
			t.Errorf("print_strategy = %v, want %s", got, defaultPrintStrategy)
		}
	})
}

// TestCardEntityContentEnvelope pins the envelope's exact key set. Feishu
// rejects the message if a stray field appears alongside type/data, so the
// count assertions matter as much as the values.
func TestCardEntityContentEnvelope(t *testing.T) {
	got, err := cardEntityContent("card-1")
	if err != nil {
		t.Fatalf("cardEntityContent returned error: %v", err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &top); err != nil {
		t.Fatalf("content %s is not valid JSON: %v", got, err)
	}
	if len(top) != 2 {
		t.Errorf("envelope has %d top-level keys (%v), want exactly type and data", len(top), top)
	}

	var data map[string]json.RawMessage
	if err := json.Unmarshal(top["data"], &data); err != nil {
		t.Fatalf("envelope data is not a JSON object: %v", err)
	}
	if len(data) != 1 {
		t.Errorf("envelope data has %d keys (%v), want exactly card_id", len(data), data)
	}
	if _, ok := data["card_id"]; !ok {
		t.Error("envelope data is missing card_id")
	}
}
