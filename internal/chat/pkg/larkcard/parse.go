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
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/onexstack/onexstack/pkg/ptr"

	knownchat "github.com/onexstack/onexai/pkg/known/chat"
)

// This file is the inbound half of the package: ParseCard reads a card someone
// else produced — one the platform handed this process, one it fetched back —
// into the same Card model RenderCardMap writes.
//
// # Why decode into the model instead of extracting text directly
//
// A card's text is then defined exactly once, by Card.RenderText, and the two
// surfaces that need it cannot drift: the mirror this service shows in the TUI
// and the words a model reads in a quoted message are the same string because
// they are the same call. It also makes the inverse property testable — render a
// Card, parse it back, render again, and the two renderings must be identical.
//
// # Best-effort by construction
//
// An element whose tag this package has never seen is walked for the children it
// does know rather than dropped, so a component Feishu adds tomorrow degrades
// into missing detail instead of an empty card. Only a body with no recognizable
// content at all is an error.

// The three ways a body can fail to be a card. They are sentinels rather than
// errors built at the point of failure because none of them carries anything the
// caller can act on: every caller treats them the same way — answer with the
// placeholder — and what a reader needs is where it happened, which the wrap at
// the call site gives.
var (
	errEmptyCardBody = errors.New("larkcard: empty card body")
	errNotJSONObject = errors.New("larkcard: card body is not a JSON object")
	errNoCardContent = errors.New("larkcard: no readable content in card body")
)

// CardContentTypeUser is the card_msg_content_type the message APIs are asked
// for. Left out, `GET im/v1/messages/:id` answers a card with the platform's own
// degraded rendering — its visible text lines plus a screenshot and a "请升级至
// 最新版本客户端" (please upgrade to the latest client) notice — and never with the card's JSON; asked for, it answers
// the JSON the producer sent.
const CardContentTypeUser = "user_card_content"

// CardPlaceholder stands in for a card whose content this process can identify
// but not read: a remote template, or a cardkit card entity. Callers use it as
// the "a card was here" text, never as content.
const CardPlaceholder = "[卡片消息]"

// maxCardElementDepth bounds the element walk. Cards nest for layout rather than
// for meaning, so the deepest shape this service has seen is a handful of levels
// (column_set → column → div → text, plus a collapsible_panel). The bound is
// what keeps a pathological body — or a cycle the JSON decoder allowed — from
// becoming unbounded recursion.
const maxCardElementDepth = 16

// CardSource says where a parsed card's content came from, which is the one thing
// the text alone cannot tell a caller: whether the words are the card's own or
// this package's description of a card it could not read.
type CardSource uint8

const (
	// CardSourceInline is a card whose content was in the body: schema 2.0, legacy
	// 1.0, or the platform's degraded rendering.
	CardSourceInline CardSource = iota
	// CardSourceTemplate is a reference to a remote template. Lark renders those
	// server-side and keeps their copy, so not even the platform can hand the text
	// back — only the id and the variables the producer filled in.
	CardSourceTemplate
	// CardSourceEntity is a reference to a cardkit card entity. cardkit v1 has no
	// read back — Create, Update, Settings and the element APIs are all writes —
	// so the entity's content is out of reach once it is on screen.
	CardSourceEntity
)

// String renders the source for a log line.
func (s CardSource) String() string {
	switch s {
	case CardSourceTemplate:
		return "template"
	case CardSourceEntity:
		return "entity"
	default:
		return "inline"
	}
}

// ParseCard reads a Lark interactive-card body into the Card model.
//
// The shapes it accepts, in the order they are told apart:
//
//	{"json_card":"<escaped JSON>"}       the wrapper some read APIs answer with
//	{"type":"template","data":{...}}     a remote template reference
//	{"type":"card","data:{card_id}}      a cardkit card entity reference
//	{"schema":"2.0","body":{"elements"}} schema 2.0, raw card JSON
//	{"header":{...},"elements":[...]}    legacy 1.0
//	{"title":..,"elements":[[{...}]]}    the platform's degraded rendering
//
// The returned Card is a lossy reading — it is built to be rendered as text, not
// to be sent back out — so callers use it for RenderText and ImageKeys, never as
// a card to re-render. unknown is reported through CardSource rather than as a
// distinct type so a caller that only wants the words does not have to branch.
func ParseCard(rawJSON string) (*Card, CardSource, error) {
	body := strings.TrimSpace(rawJSON)
	if body == "" {
		return nil, CardSourceInline, errEmptyCardBody
	}
	body = unwrapJSONCard(body)

	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &top); err != nil {
		return nil, CardSourceInline, fmt.Errorf("%w: %w", errNotJSONObject, err)
	}
	if len(top) == 0 {
		return nil, CardSourceInline, errNoCardContent
	}

	// A reference, not a card. Both kinds are answered from what the reference
	// itself carries, because that is all there is.
	switch jsonString(top["type"]) {
	case "template":
		return templateRefCard(top), CardSourceTemplate, nil
	case "card":
		return entityRefCard(top), CardSourceEntity, nil
	}

	card := &Card{Header: headerOf(top)}

	// Schema 2.0 moved the body's elements under "body". A 1.0 card never has
	// one, so its presence — not the schema field, which not every producer
	// writes — is what tells the two apart.
	if bodyRaw, ok := top["body"]; ok {
		var body struct {
			Elements []json.RawMessage `json:"elements"`
			// Some producers wrap the list one level deeper; the read API's raw card
			// shape has been seen both ways.
			Property struct {
				Elements []json.RawMessage `json:"elements"`
			} `json:"property"`
		}
		_ = json.Unmarshal(bodyRaw, &body)
		elems := body.Elements
		if len(elems) == 0 {
			elems = body.Property.Elements
		}
		card.Elements = coalesceActionRows(decodeElements(elems, 0))
	} else {
		card.Elements = coalesceActionRows(decodeElements(topLevelElements(top["elements"]), 0))
	}

	if len(card.Elements) == 0 && (card.Header == nil || card.Header.Title == "") {
		return nil, CardSourceInline, errNoCardContent
	}
	return card, CardSourceInline, nil
}

// unwrapJSONCard peels the {"json_card": "<escaped JSON>"} envelope that the read
// APIs wrap a raw card in, and answers the body unchanged when there is none.
func unwrapJSONCard(raw string) string {
	var wrapper struct {
		JSONCard string `json:"json_card"`
	}
	if json.Unmarshal([]byte(raw), &wrapper) == nil && strings.TrimSpace(wrapper.JSONCard) != "" {
		return strings.TrimSpace(wrapper.JSONCard)
	}
	return raw
}

// headerOf reads a card header, from either the header object (2.0 and 1.0) or
// the bare title string the degraded rendering carries.
func headerOf(top map[string]json.RawMessage) *CardHeader {
	if header := headerFrom(top["header"]); header != nil {
		return header
	}
	if title := jsonString(top["title"]); title != "" {
		return &CardHeader{Title: title}
	}
	return nil
}

// headerFrom reads one header object. The title is a text object in both card
// schemas — {"tag":"plain_text","content":"…"} — so only its content is taken.
func headerFrom(raw json.RawMessage) *CardHeader {
	if len(raw) == 0 {
		return nil
	}
	var header struct {
		Title struct {
			Content string `json:"content"`
		} `json:"title"`
		Template string `json:"template"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil
	}
	title := strings.TrimSpace(header.Title.Content)
	if title == "" {
		return nil
	}
	return &CardHeader{Title: title, Color: header.Template}
}

// topLevelElements reads a top-level "elements" list, in either of the two
// shapes it comes in: a flat array of elements (card 1.0), or an array of arrays
// (the degraded rendering, which groups a card's content into lines).
//
// The two are told apart by which decode succeeds, not by inspecting the bytes:
// an array of objects unmarshals into [][]RawMessage not at all, which is exactly
// the discrimination wanted.
func topLevelElements(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var nested [][]json.RawMessage
	if err := json.Unmarshal(raw, &nested); err == nil {
		var flat []json.RawMessage
		for _, line := range nested {
			flat = append(flat, line...)
		}
		return flat
	}
	var flat []json.RawMessage
	_ = json.Unmarshal(raw, &flat)
	return flat
}

// templateRefCard describes a remote template reference. Lark keeps the
// template's copy on its own side, so all that can be said about it is what it is
// (when known) and what the producer filled in.
func templateRefCard(top map[string]json.RawMessage) *Card {
	var data struct {
		TemplateID string         `json:"template_id"`
		Vars       map[string]any `json:"template_variable"`
	}
	_ = json.Unmarshal(top["data"], &data)

	title := knownchat.CardTemplateTitle(data.TemplateID)
	if title == "" {
		title = CardPlaceholder
	}
	card := &Card{Header: &CardHeader{Title: title}}

	if data.TemplateID != "" {
		card.Elements = append(card.Elements, CardMarkdown{Content: "模板 ID：" + data.TemplateID})
	}
	if len(data.Vars) > 0 {
		// Sorted, because a Go map has no order and a prompt that reorders itself
		// between two identical questions is a prompt that reads as two cards.
		keys := make([]string, 0, len(data.Vars))
		for k := range data.Vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lines := make([]string, 0, len(keys))
		for _, k := range keys {
			lines = append(lines, "- "+k+"："+variableValue(data.Vars[k]))
		}
		card.Elements = append(card.Elements, CardMarkdown{Content: "变量：\n" + strings.Join(lines, "\n")})
	}
	return card
}

// entityRefCard describes a cardkit card entity reference. cardkit v1 can create
// and update an entity but not read one back, so the id is all there is; a
// follow-up question about such a card is answered from the conversation's own
// context, where the answer that card carries already is.
func entityRefCard(top map[string]json.RawMessage) *Card {
	var data struct {
		CardID string `json:"card_id"`
	}
	_ = json.Unmarshal(top["data"], &data)

	card := &Card{Header: &CardHeader{Title: CardPlaceholder}}
	if data.CardID != "" {
		card.Elements = append(card.Elements, CardMarkdown{Content: "卡片实体 ID：" + data.CardID})
	}
	return card
}

// variableValue renders one template variable. Strings are taken as they are —
// they are what a producer writes for a human — and anything else is written as
// compact JSON, because Go's own rendering of a map or a slice prints the
// internals rather than the value.
func variableValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// rawElement is the union of the fields the element walk reads, across every
// shape and schema version. One struct rather than a decode per tag: an element
// carries a handful of fields, and the alternative is a dozen small anonymous
// types whose differences are what the tags already say.
type rawElement struct {
	Tag     string          `json:"tag"`
	Type    string          `json:"type"`
	Content string          `json:"content"`
	Text    json.RawMessage `json:"text"` // a text object, or a bare string in the degraded shape
	// Header is a collapsible_panel's title bar.
	Header   json.RawMessage   `json:"header"`
	Fields   []json.RawMessage `json:"fields"`
	Elements []json.RawMessage `json:"elements"`
	// Columns is a column_set's children or a table's column definitions; the
	// tag decides which, so both are read as raw JSON.
	Columns []json.RawMessage `json:"columns"`
	Actions []json.RawMessage `json:"actions"`
	Items   []json.RawMessage `json:"items"`
	// Contents is a code_block's lines, each a list of tokens.
	Contents    json.RawMessage `json:"contents"`
	Language    string          `json:"language"`
	Rows        json.RawMessage `json:"rows"`
	Behaviors   []rawBehavior   `json:"behaviors"`
	Options     []rawOption     `json:"options"`
	Value       json.RawMessage `json:"value"`
	Alt         json.RawMessage `json:"alt"`
	Placeholder json.RawMessage `json:"placeholder"`
	InitialOpt  string          `json:"initial_option"`
	// ImgKey is card 2.0's spelling of a picture; ImageKey is the degraded
	// rendering's and card 1.0's.
	ImgKey   string `json:"img_key"`
	ImageKey string `json:"image_key"`
	// Property is where one of the read APIs puts a component's fields, with the
	// tag left beside it at the top level. See unwrapProperty.
	Property *rawElement `json:"property"`
}

// unwrapProperty flattens the shape that carries a component's fields one level
// down, under "property", keeping the tag that sits beside it.
//
// No card schema writes components that way, so this exists for exactly one
// caller: a card read back with raw_card_content, whose fields are wrapped. The
// alternative is a parser that silently reads such a card as empty, which is the
// failure that looks like a card nobody wrote.
func unwrapProperty(e rawElement) rawElement {
	if e.Property == nil {
		return e
	}
	tag := e.Tag
	flattened := ptr.From(e.Property)
	if flattened.Tag == "" {
		flattened.Tag = tag
	}
	return flattened
}

// rawBehavior is one entry of a button's behaviours list.
type rawBehavior struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// rawOption is one entry of a select's options list.
type rawOption struct {
	Text  json.RawMessage `json:"text"`
	Value string          `json:"value"`
}

// decodeElements walks a list of elements, in order.
func decodeElements(raws []json.RawMessage, depth int) []CardElement {
	var out []CardElement
	for _, raw := range raws {
		out = append(out, decodeElement(raw, depth)...)
	}
	return out
}

// decodeElement reads one element. It answers a slice rather than a single
// element because the mapping is not one to one at either end: a container
// stands for its children, and a note can carry a picture beside its words.
//
// Every branch is written so that a tag it does not know contributes its
// children and nothing else. Reading a container's own content field is the
// mistake to avoid — containers carry layout strings that are not prose, and
// folding them into the text puts " " and "right" in the middle of a sentence.
func decodeElement(raw json.RawMessage, depth int) []CardElement {
	if depth > maxCardElementDepth {
		return nil
	}
	var e rawElement
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil
	}
	e = unwrapProperty(e)

	switch e.Tag {
	case "markdown", "lark_md", "plain_text", "text", "text_tag":
		if s := firstNonEmpty(e.Content, textOf(e.Text)); s != "" && !isCardNoise(s) {
			return []CardElement{CardMarkdown{Content: s}}
		}
		return nil

	case "div":
		// A div's text is a text object; its fields are the short key/value
		// columns beside it. Both are prose, so both are kept.
		var parts []string
		if s := textOf(e.Text); s != "" {
			parts = append(parts, s)
		}
		for _, field := range e.Fields {
			var f struct {
				Text json.RawMessage `json:"text"`
			}
			if json.Unmarshal(field, &f) == nil {
				if s := textOf(f.Text); s != "" {
					parts = append(parts, s)
				}
			}
		}
		if len(parts) == 0 {
			return nil
		}
		return []CardElement{CardMarkdown{Content: strings.Join(parts, "\n")}}

	case "code_block":
		if code := codeBlockText(e); code != "" {
			lang := strings.TrimSpace(e.Language)
			return []CardElement{CardMarkdown{Content: "```" + lang + "\n" + code + "\n```"}}
		}
		return nil

	case "code_span":
		if content := strings.TrimSpace(firstNonEmpty(e.Content, textOf(e.Text))); content != "" {
			return []CardElement{CardMarkdown{Content: "`" + content + "`"}}
		}
		return nil

	case "list":
		return listElement(e, depth)

	case "hr":
		return []CardElement{CardDivider{}}

	case "note":
		// Card 1.0's footnote. Its children are text and, often, a picture — the
		// picture is content too, and is kept beside the words rather than lost
		// with the note's shape.
		children := decodeElements(e.Elements, depth+1)
		var (
			texts []string
			rest  []CardElement
		)
		for _, child := range children {
			if md, ok := child.(CardMarkdown); ok {
				texts = append(texts, md.Content)
				continue
			}
			rest = append(rest, child)
		}
		if len(texts) == 0 {
			return rest
		}
		return append([]CardElement{CardNote{Text: strings.Join(texts, " ")}}, rest...)

	case "action":
		// Card 1.0's button container; card 2.0 removed it.
		return decodeElements(e.Actions, depth+1)

	case "button":
		return decodeButton(e)

	case "select_static", "select_person", "multi_select_static", "overflow":
		options := make([]CardSelectOption, 0, len(e.Options))
		for _, opt := range e.Options {
			text := textOf(opt.Text)
			if text == "" {
				continue
			}
			options = append(options, CardSelectOption{Text: text, Value: opt.Value})
		}
		placeholder := textOf(e.Placeholder)
		if placeholder == "" && len(options) == 0 {
			return nil
		}
		return []CardElement{CardSelect{Placeholder: placeholder, Options: options, InitValue: e.InitialOpt}}

	case "table":
		return tableElement(e, depth)

	case "img", "image":
		key := firstNonEmpty(e.ImgKey, e.ImageKey)
		if key == "" {
			return nil
		}
		return []CardElement{CardImage{Key: key, Alt: textOf(e.Alt)}}

	case "collapsible_panel":
		// A container with a title of its own; card 2.0 keeps it in the header.
		var out []CardElement
		if title := panelTitle(e.Header); title != "" {
			out = append(out, CardMarkdown{Content: title})
		}
		return append(out, decodeElements(e.Elements, depth+1)...)

	default:
		// A container this package has no case for — column_set, column, form,
		// interactive_container, and whatever comes next. Its children are still
		// content.
		return append(decodeElements(e.Elements, depth+1), decodeElements(e.Columns, depth+1)...)
	}
}

// decodeButton reads a button. A button that opens a link is rendered as a
// Markdown link, because the target is the part a reader cannot guess; a button
// that acts carries only its label, since its callback value is meaningless
// outside the card it belongs to.
func decodeButton(e rawElement) []CardElement {
	label := strings.TrimSpace(firstNonEmpty(textOf(e.Text), e.Content))
	if label == "" {
		return nil
	}
	if url := openURL(e.Behaviors); url != "" {
		return []CardElement{CardMarkdown{Content: fmt.Sprintf("[%s](%s)", label, url)}}
	}
	return []CardElement{CardActions{
		Buttons: []CardButton{{
			Text:  label,
			Type:  strings.TrimSpace(e.Type),
			Value: scalarString(e.Value),
		}},
		Layout: CardActionLayoutRow,
	}}
}

// openURL returns the first open_url target in a button's behaviours.
func openURL(behaviors []rawBehavior) string {
	for _, b := range behaviors {
		if b.Type == "open_url" && b.URL != "" {
			return b.URL
		}
	}
	return ""
}

// listElement reads a list: each item is a run of elements rendered on its own
// line, prefixed so the lines read as a list rather than as a paragraph.
func listElement(e rawElement, depth int) []CardElement {
	var lines []string
	for _, item := range e.Items {
		// An item is either an element of its own or a wrapper holding the
		// elements that make it up; both shapes are in the wild.
		elems := decodeElement(item, depth+1)
		if text := elementsText(elems); text != "" {
			lines = append(lines, "- "+text)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return []CardElement{CardMarkdown{Content: strings.Join(lines, "\n")}}
}

// tableElement reads a card table. The rows come in two shapes — a cell that is
// a plain value, and a cell that is an element to be read — and which one a row
// carries decides how the cell is rendered.
//
// A table with no usable column definition is refused by the platform as a
// component, but it can still arrive in a card someone else produced; its rows
// are then rendered as lines rather than dropped, because the values are the
// content and the columns were only the arrangement.
func tableElement(e rawElement, depth int) []CardElement {
	var rows []map[string]json.RawMessage
	if len(e.Rows) > 0 {
		_ = json.Unmarshal(e.Rows, &rows)
	}

	var columns []CardTableColumn
	for _, raw := range e.Columns {
		var c struct {
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
			DataType    string `json:"data_type"`
			Width       string `json:"width"`
			Align       string `json:"horizontal_align"`
		}
		if json.Unmarshal(raw, &c) != nil || c.Name == "" {
			continue
		}
		columns = append(columns, CardTableColumn{
			Name:            c.Name,
			DisplayName:     c.DisplayName,
			DataType:        c.DataType,
			Width:           c.Width,
			HorizontalAlign: c.Align,
		})
	}
	if len(columns) == 0 {
		return tableWithoutColumns(rows, depth)
	}
	if len(rows) == 0 {
		return nil
	}

	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		rendered := make(map[string]any, len(row))
		for name, cell := range row {
			rendered[name] = cellText(cell, depth+1)
		}
		out = append(out, rendered)
	}
	return []CardElement{CardTable{Columns: columns, Rows: out}}
}

// tableWithoutColumns renders a table whose column definitions are missing or
// unusable: one line per row, values in key order.
func tableWithoutColumns(rows []map[string]json.RawMessage, depth int) []CardElement {
	var lines []string
	for _, row := range rows {
		keys := make([]string, 0, len(row))
		for k := range row {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		cells := make([]string, 0, len(keys))
		for _, k := range keys {
			cells = append(cells, k+"="+cellText(row[k], depth+1))
		}
		line := strings.TrimSpace(strings.Join(cells, "  "))
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return []CardElement{CardMarkdown{Content: strings.Join(lines, "\n")}}
}

// cellText reads one table cell. A cell is a scalar in the common case, an
// element when it carries rich text, and an element wrapped in "data" in the
// read API's raw card shape — all three are in the wild, and the tag decides.
func cellText(raw json.RawMessage, depth int) string {
	var probe struct {
		Tag  string          `json:"tag"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil {
		if len(probe.Data) > 0 {
			if text := elementsText(decodeElement(probe.Data, depth)); text != "" {
				return text
			}
		}
		if probe.Tag != "" {
			if text := elementsText(decodeElement(raw, depth)); text != "" {
				return text
			}
		}
	}
	return scalarString(raw)
}

// codeBlockText reads a code block's source. Its lines are lists of tokens,
// each token carrying its own text; some producers instead write the whole block
// into a single content field.
func codeBlockText(e rawElement) string {
	if len(e.Contents) > 0 {
		var lines []struct {
			Contents []struct {
				Content string `json:"content"`
			} `json:"contents"`
		}
		if json.Unmarshal(e.Contents, &lines) == nil {
			rendered := make([]string, 0, len(lines))
			for _, line := range lines {
				var text strings.Builder
				for _, token := range line.Contents {
					text.WriteString(token.Content)
				}
				rendered = append(rendered, text.String())
			}
			if code := strings.TrimRight(strings.Join(rendered, "\n"), "\n"); strings.TrimSpace(code) != "" {
				return code
			}
		}
	}
	return strings.TrimSpace(e.Content)
}

// panelTitle reads a collapsible_panel's title bar.
func panelTitle(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var header struct {
		Title json.RawMessage `json:"title"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return ""
	}
	return textOf(header.Title)
}

// coalesceActionRows merges consecutive button rows into one.
//
// The flattening above walks a button row's column_set and answers each column's
// button on its own, which is right for reading order and wrong for rendering: a
// row of three buttons would come back as three rows and RenderText would lay
// them out on three lines. Merging them restores the row — and it is what makes
// the render/parse round trip exact for a card this package itself produced.
func coalesceActionRows(elems []CardElement) []CardElement {
	if len(elems) < 2 {
		return elems
	}
	out := make([]CardElement, 0, len(elems))
	for _, elem := range elems {
		row, isRow := elem.(CardActions)
		if !isRow || len(out) == 0 {
			out = append(out, elem)
			continue
		}
		prev, ok := out[len(out)-1].(CardActions)
		if !ok || prev.Layout != row.Layout {
			out = append(out, elem)
			continue
		}
		prev.Buttons = append(prev.Buttons, row.Buttons...)
		out[len(out)-1] = prev
	}
	return out
}

// elementsText renders a run of elements as text, by putting them on a card of
// their own and asking the one renderer. It is what keeps a table cell or a list
// item readable without a second text renderer to keep in step with the first.
func elementsText(elems []CardElement) string {
	if len(elems) == 0 {
		return ""
	}
	return strings.TrimSpace((&Card{Elements: elems}).RenderText())
}

// textOf reads a text object — {"tag":"plain_text","content":"…"} — or a bare
// string, which is how the platform's degraded rendering writes the same thing.
func textOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return strings.TrimSpace(obj.Content)
	}
	return ""
}

// scalarString renders a JSON scalar, or the compact JSON of anything else.
func scalarString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return variableValue(v)
}

// jsonString reads a bare JSON string, answering "" for anything else.
func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// firstNonEmpty returns the first argument that is not empty.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// cardNoiseNotice is what the platform's degraded rendering of a card says where
// the interactive components it could not draw would have been. It is the
// platform talking to whoever is looking at the card, not the card's producer
// talking to the reader, so it is dropped rather than handed to a model as if
// somebody had written it.
const cardNoiseNotice = "请升级至最新版本客户端"

// isCardNoise reports whether a text block is the platform's own notice.
func isCardNoise(text string) bool {
	return strings.Contains(text, cardNoiseNotice)
}
