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

// Package larkcard defines a platform-neutral rich-card model and its Lark
// Interactive Card renderer. The model lets callers build structured cards
// (markdown, dividers, button rows, list items, selects, notes) through a
// fluent builder instead of hand-assembling JSON maps; the renderer converts
// the model into Lark's Card JSON 2.0.
//
// It reads as well as writes: ParseCard (parse.go) turns a card body this
// process received — its own, or one somebody else produced, in any of the
// schemas Lark has shipped — back into the same model, so a card's text has a
// single definition and the surfaces that show it to a person and to a model
// cannot drift apart.
//
// The package has no dependency on the Lark SDK and keeps rendering and parsing
// as pure functions so they can be unit-tested without a live client. It is
// deliberately separate from the cardkit streaming-card path
// (biz/v1/bot/streaming_card.go), which drives the "Card 2.0" live-update
// protocol.
package larkcard

import (
	"fmt"
	"strings"
)

// Card JSON schema version. Card 2.0 is the only schema this package emits:
// its config uses width_mode, which is the 2.0 replacement for the 1.0-only
// wide_screen_mode (keeping the latter makes Feishu reject the card with 230099
// "parse card json err").
const CardSchemaV2 = "2.0"

// WidthModeFill makes a card span the full width of the chat window. Every card
// root in this repo uses it so widths cannot drift apart again; the alternative
// values are "default" (PC caps at 600px) and "compact" (400px).
const WidthModeFill = "fill"

// CardWidthConfig returns the config block shared by every card root: schema
// 2.0 with a full-width card.
func CardWidthConfig() map[string]any {
	return map[string]any{"width_mode": WidthModeFill}
}

// NewCardRoot builds the schema 2.0 root every card shares. Callers start from
// CardWidthConfig and add only the keys their card needs, so the root shape and
// the width cannot drift apart between surfaces.
func NewCardRoot(config map[string]any, elements []map[string]any) map[string]any {
	return map[string]any{
		"schema": CardSchemaV2,
		"config": config,
		"body":   map[string]any{"elements": elements},
	}
}

// Card represents a structured rich message that can be rendered as a Lark
// Interactive Card or degraded to plain text for surfaces without card support.
type Card struct {
	Header   *CardHeader
	Elements []CardElement
	// BodyPadding is the body's inner padding, in Lark's CSS-like shorthand
	// ("16px 12px 12px 12px": top right bottom left). An empty value leaves the
	// platform's own default.
	BodyPadding string
	// BodySpacing is the vertical gap between body elements ("8px", or the
	// presets small/medium/large/extra_large). An empty value leaves the
	// platform's own default.
	BodySpacing string
}

// CardHeader is the optional colored title bar of a card.
type CardHeader struct {
	Title string
	// Color is one of Lark's supported template colors: blue, green, red,
	// orange, purple, grey, turquoise, violet, indigo, wathet, yellow, carmine.
	Color string
}

// CardElement is the interface satisfied by all card content elements.
type CardElement interface {
	cardElement()
}

// CardMarkdown renders markdown-formatted text.
type CardMarkdown struct{ Content string }

// CardDivider renders a horizontal rule.
type CardDivider struct{}

// CardActions renders a row of clickable buttons.
type CardActions struct {
	Buttons []CardButton
	Layout  CardActionLayout
}

// CardNote renders small footnote text at the bottom.
// Tag is an optional machine-readable identifier (not displayed) used by the
// renderer to recognize and handle specific notes programmatically.
type CardNote struct {
	Text string
	Tag  string
}

// CardImage is a picture on a card.
//
// It is an inbound-only element: ParseCard produces it, and RenderCardMap does
// not render it. The component whitelist in card2schema.go holds only the shapes
// this repo has actually put in front of Feishu, and nothing here sends images
// in cards, so emitting one would be an unverified card shape. A caller that
// wants a card's pictures reads ImageKeys and handles them as message
// attachments, which is what the quoted-message path does.
type CardImage struct {
	Key string // img_key in card 2.0, image_key in the degraded rendering
	Alt string
}

// CardListItem renders a row with description text on the left and a button on
// the right.
type CardListItem struct {
	Text     string            // left-side description
	BtnText  string            // button label
	BtnType  string            // "primary", "default", "danger"
	BtnValue string            // callback data, e.g. "cmd:/new", "act:/switch 3"
	Extra    map[string]string // additional key-value pairs carried in the callback
}

// CardSelect renders a dropdown selector.
type CardSelect struct {
	Placeholder string
	Options     []CardSelectOption
	InitValue   string // pre-selected option value (empty = none)
}

// CardSelectOption is one item in a CardSelect dropdown.
type CardSelectOption struct {
	Text  string
	Value string
}

// CardTable renders a native Lark card 2.0 table: a header row plus one row per
// data row, paginated by the client. It is a root-level element — the platform
// refuses to nest it inside column_set, collapsible_panel or any other
// container — so a builder must append it to the card body directly.
type CardTable struct {
	Columns []CardTableColumn
	Rows    []map[string]any
	// PageSize is how many rows one page shows. Zero means the platform's
	// default (5); the accepted range is [1, 10].
	PageSize int
	// RowHeight is "" (platform default) or low/middle/high/auto/"40px".
	RowHeight string
	// FreezeFirstColumn keeps the key column visible while the value column
	// scrolls horizontally.
	FreezeFirstColumn bool
}

// CardTableColumn defines one column of a CardTable. Name is the key each row
// map is read by; DisplayName is what the header shows.
type CardTableColumn struct {
	Name        string
	DisplayName string
	// DataType is text (default), lark_md, number, options, persons, date or
	// markdown.
	DataType string
	// Width is "" (platform default), "auto", "120px" or "30%".
	Width string
	// HorizontalAlign is "" (platform default), left, center or right.
	HorizontalAlign string
}

func (CardMarkdown) cardElement() {}
func (CardDivider) cardElement()  {}
func (CardActions) cardElement()  {}
func (CardNote) cardElement()     {}
func (CardImage) cardElement()    {}
func (CardListItem) cardElement() {}
func (CardSelect) cardElement()   {}
func (CardTable) cardElement()    {}

// CardButton represents a clickable button inside a CardActions element.
type CardButton struct {
	Text  string            // display label
	Type  string            // "primary", "default", "danger"
	Value string            // callback data, e.g. "cmd:/new", "act:/switch 3"
	Extra map[string]string // additional key-value pairs carried in the callback
}

// CardActionLayout controls how a CardActions row is rendered.
type CardActionLayout string

const (
	// CardActionLayoutRow lays buttons out in a single action row.
	CardActionLayoutRow CardActionLayout = "row"
	// CardActionLayoutEqualColumns lays buttons out as equal-width columns.
	CardActionLayoutEqualColumns CardActionLayout = "equal_columns"
)

// Btn is a shorthand constructor for CardButton.
func Btn(text, typ, value string) CardButton {
	return CardButton{Text: text, Type: typ, Value: value}
}

// PrimaryBtn creates a primary-styled button.
func PrimaryBtn(text, value string) CardButton {
	return CardButton{Text: text, Type: "primary", Value: value}
}

// DefaultBtn creates a default-styled button.
func DefaultBtn(text, value string) CardButton {
	return CardButton{Text: text, Type: "default", Value: value}
}

// DangerBtn creates a danger-styled button.
func DangerBtn(text, value string) CardButton {
	return CardButton{Text: text, Type: "danger", Value: value}
}

// CardBuilder provides a fluent API for constructing Card instances.
type CardBuilder struct {
	card Card
}

// NewCard returns a new CardBuilder.
func NewCard() *CardBuilder {
	return &CardBuilder{}
}

// Title sets the card header with a title and color.
func (b *CardBuilder) Title(title, color string) *CardBuilder {
	b.card.Header = &CardHeader{Title: title, Color: color}
	return b
}

// BodyLayout sets the body's inner padding and the vertical gap between its
// elements, both in Lark's own units (see Card.BodyPadding / Card.BodySpacing).
// Either value may be empty to keep the platform's default.
//
// It exists for the card whose spacing is the reason it is rendered here at all:
// a card with no header starts at the body's top padding, so this is the air
// between the top of the card and its first line — the gap a quote reply needs
// between the quoted message and the card's title.
func (b *CardBuilder) BodyLayout(padding, spacing string) *CardBuilder {
	b.card.BodyPadding = padding
	b.card.BodySpacing = spacing
	return b
}

// Markdown appends a markdown text element.
func (b *CardBuilder) Markdown(content string) *CardBuilder {
	if content != "" {
		b.card.Elements = append(b.card.Elements, CardMarkdown{Content: content})
	}
	return b
}

// Markdownf appends a formatted markdown text element.
func (b *CardBuilder) Markdownf(format string, args ...any) *CardBuilder {
	return b.Markdown(fmt.Sprintf(format, args...))
}

// Divider appends a horizontal divider.
func (b *CardBuilder) Divider() *CardBuilder {
	b.card.Elements = append(b.card.Elements, CardDivider{})
	return b
}

// Buttons appends an action row with the given buttons.
func (b *CardBuilder) Buttons(buttons ...CardButton) *CardBuilder {
	if len(buttons) > 0 {
		b.card.Elements = append(b.card.Elements, CardActions{Buttons: buttons, Layout: CardActionLayoutRow})
	}
	return b
}

// ButtonsEqual appends an action row where each button takes equal width.
func (b *CardBuilder) ButtonsEqual(buttons ...CardButton) *CardBuilder {
	if len(buttons) > 0 {
		b.card.Elements = append(b.card.Elements, CardActions{Buttons: buttons, Layout: CardActionLayoutEqualColumns})
	}
	return b
}

// ListItem appends a list row: description on the left, button on the right.
func (b *CardBuilder) ListItem(desc, btnText, btnValue string) *CardBuilder {
	b.card.Elements = append(b.card.Elements, CardListItem{
		Text: desc, BtnText: btnText, BtnType: "default", BtnValue: btnValue,
	})
	return b
}

// ListItemBtn is like ListItem but allows specifying the button type.
func (b *CardBuilder) ListItemBtn(desc, btnText, btnType, btnValue string) *CardBuilder {
	b.card.Elements = append(b.card.Elements, CardListItem{
		Text: desc, BtnText: btnText, BtnType: btnType, BtnValue: btnValue,
	})
	return b
}

// ListItemBtnExtra is like ListItemBtn but with extra callback data.
func (b *CardBuilder) ListItemBtnExtra(desc, btnText, btnType, btnValue string, extra map[string]string) *CardBuilder {
	b.card.Elements = append(b.card.Elements, CardListItem{
		Text: desc, BtnText: btnText, BtnType: btnType, BtnValue: btnValue, Extra: extra,
	})
	return b
}

// Select appends a dropdown selector element.
func (b *CardBuilder) Select(placeholder string, options []CardSelectOption, initValue string) *CardBuilder {
	if len(options) > 0 {
		b.card.Elements = append(b.card.Elements, CardSelect{
			Placeholder: placeholder, Options: options, InitValue: initValue,
		})
	}
	return b
}

// Table appends a native table element. A table with no rows, or with no column
// that names one, is not appended: the platform rejects an empty one, and there
// is nothing to show.
func (b *CardBuilder) Table(table CardTable) *CardBuilder {
	if len(table.Rows) > 0 && hasNamedColumn(table.Columns) {
		b.card.Elements = append(b.card.Elements, table)
	}
	return b
}

// hasNamedColumn reports whether at least one column carries the name its rows
// are read by.
func hasNamedColumn(columns []CardTableColumn) bool {
	for _, c := range columns {
		if c.Name != "" {
			return true
		}
	}
	return false
}

// Note appends a footnote element.
func (b *CardBuilder) Note(text string) *CardBuilder {
	if text != "" {
		b.card.Elements = append(b.card.Elements, CardNote{Text: text})
	}
	return b
}

// TaggedNote appends a footnote element carrying a machine-readable tag.
func (b *CardBuilder) TaggedNote(tag, text string) *CardBuilder {
	if text != "" {
		b.card.Elements = append(b.card.Elements, CardNote{Text: text, Tag: tag})
	}
	return b
}

// Build returns the constructed Card.
func (b *CardBuilder) Build() *Card {
	c := b.card
	return &c
}

// RenderText converts the card to a plain-text representation for surfaces
// without rich-card support (used as a fallback, e.g. mirroring into the TUI).
func (c *Card) RenderText() string {
	var sb strings.Builder

	if c.Header != nil && c.Header.Title != "" {
		sb.WriteString("**")
		sb.WriteString(c.Header.Title)
		sb.WriteString("**\n\n")
	}

	for _, elem := range c.Elements {
		switch e := elem.(type) {
		case CardMarkdown:
			sb.WriteString(e.Content)
			sb.WriteString("\n\n")
		case CardDivider:
			sb.WriteString("---\n\n")
		case CardActions:
			for i, btn := range e.Buttons {
				if i > 0 {
					sb.WriteString("  ")
				}
				sb.WriteString("[")
				sb.WriteString(btn.Text)
				sb.WriteString("]")
			}
			sb.WriteString("\n\n")
		case CardListItem:
			sb.WriteString(e.Text)
			sb.WriteString("  [")
			sb.WriteString(e.BtnText)
			sb.WriteString("]\n")
		case CardSelect:
			sb.WriteString(e.Placeholder)
			sb.WriteString(": ")
			for i, opt := range e.Options {
				if i > 0 {
					sb.WriteString(" | ")
				}
				sb.WriteString(opt.Text)
			}
			sb.WriteString("\n\n")
		case CardTable:
			if len(e.Columns) == 0 || len(e.Rows) == 0 {
				break
			}
			for i, col := range e.Columns {
				if i > 0 {
					sb.WriteString(" | ")
				}
				sb.WriteString(col.DisplayName)
			}
			sb.WriteString("\n")
			for _, row := range e.Rows {
				for i, col := range e.Columns {
					if i > 0 {
						sb.WriteString(" | ")
					}
					fmt.Fprint(&sb, row[col.Name])
				}
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		case CardNote:
			sb.WriteString(e.Text)
			sb.WriteString("\n")
		case CardImage:
			sb.WriteString("[图片")
			if e.Alt != "" {
				sb.WriteString("：")
				sb.WriteString(e.Alt)
			}
			sb.WriteString("]\n\n")
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

// HasButtons reports whether the card contains any interactive elements.
func (c *Card) HasButtons() bool {
	for _, elem := range c.Elements {
		switch elem.(type) {
		case CardActions, CardListItem, CardSelect:
			return true
		}
	}
	return false
}

// ImageKeys returns the keys of every picture on the card, in the order they
// appear. Only a card read by ParseCard has any: the model's other producers
// never add a CardImage.
func (c *Card) ImageKeys() []string {
	if c == nil {
		return nil
	}
	var keys []string
	for _, elem := range c.Elements {
		if img, ok := elem.(CardImage); ok && img.Key != "" {
			keys = append(keys, img.Key)
		}
	}
	return keys
}
