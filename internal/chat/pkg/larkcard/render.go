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
	"log/slog"
)

// plainText is a helper building a Lark plain_text element.
func plainText(content string) map[string]any {
	return map[string]any{"tag": "plain_text", "content": content}
}

// noteDiv renders a footnote element: card 2.0 dropped the v1 `note` component,
// and notation-sized grey text on a div is the documented replacement.
//
// The two style fields belong to the inner plain_text, not to the div. Card 2.0
// validates component properties strictly — 1.0 ignored an unknown one, 2.0
// rejects the whole card — so a div carrying text_color is refused with 200621
// "parse card json err, unknown property: text_color" and the card never arrives.
func noteDiv(content string) map[string]any {
	return map[string]any{
		"tag": "div",
		"text": map[string]any{
			"tag":        "plain_text",
			"content":    content,
			"text_size":  "notation",
			"text_color": "grey",
		},
	}
}

// RenderCard converts a Card into Lark Interactive Card JSON. The sessionKey is
// injected into every button/list-item/select callback value so card actions can
// be routed back to the originating conversation.
func RenderCard(card *Card, sessionKey string) string {
	b, err := json.Marshal(RenderCardMap(card, sessionKey))
	if err != nil {
		slog.Error("larkcard: render card marshal failed", "error", err)
		return `{"schema":"2.0","config":{"width_mode":"fill"},"body":{"elements":[]}}`
	}
	return string(b)
}

// RenderCardMap converts a Card into a Lark Card 2.0 map. It is the shared
// rendering core used both for the message API (via RenderCard) and for
// card-action callback responses (CardActionTriggerResponse). Feishu requires a
// callback's response card to use the same schema as the card being acted on,
// so both paths have to stay on 2.0 together.
func RenderCardMap(card *Card, sessionKey string) map[string]any {
	result := map[string]any{
		"schema": CardSchemaV2,
		"config": CardWidthConfig(),
	}
	if card == nil {
		return result
	}

	if card.Header != nil && card.Header.Title != "" {
		color := card.Header.Color
		if color == "" {
			color = "blue"
		}
		result["header"] = map[string]any{
			"title":    plainText(card.Header.Title),
			"template": color,
		}
	}
	if transformed, ok := renderDeleteModeCheckerCard(card, result); ok {
		return transformed
	}

	var elements []map[string]any
	for _, elem := range card.Elements {
		switch e := elem.(type) {
		case CardMarkdown:
			elements = append(elements, map[string]any{
				"tag":     "markdown",
				"content": e.Content,
			})
		case CardDivider:
			elements = append(elements, map[string]any{
				"tag": "hr",
			})
		case CardActions:
			var actions []map[string]any
			for _, btn := range e.Buttons {
				action := buttonElement(btn, sessionKey)
				if e.Layout == CardActionLayoutEqualColumns {
					action["width"] = "fill"
				}
				actions = append(actions, action)
			}
			if len(actions) > 0 {
				if e.Layout == CardActionLayoutEqualColumns {
					columns := make([]map[string]any, 0, len(actions))
					for _, action := range actions {
						columns = append(columns, map[string]any{
							"tag":              "column",
							"width":            "weighted",
							"weight":           1,
							"vertical_align":   "center",
							"horizontal_align": "center",
							"elements":         []map[string]any{action},
						})
					}
					columnSet := map[string]any{
						"tag":     "column_set",
						"columns": columns,
					}
					if len(actions) == 2 {
						columnSet["flex_mode"] = "bisect"
					}
					elements = append(elements, columnSet)
				} else {
					// Card 2.0 removed the v1 `action` container; a row of
					// buttons has to be laid out through a column_set.
					elements = append(elements, buttonRowColumnSet(actions, "left"))
				}
			}
		case CardListItem:
			btnType := e.BtnType
			if btnType == "" {
				btnType = "default"
			}
			valMap := buttonValueMap(e.BtnValue, sessionKey, e.Extra)
			elements = append(elements, map[string]any{
				"tag":       "column_set",
				"flex_mode": "none",
				"columns": []map[string]any{
					{
						"tag":            "column",
						"width":          "weighted",
						"weight":         5,
						"vertical_align": "center",
						"elements": []map[string]any{
							{
								"tag":     "markdown",
								"content": e.Text,
							},
						},
					},
					{
						"tag":            "column",
						"width":          "auto",
						"vertical_align": "center",
						"elements": []map[string]any{
							{
								"tag":   "button",
								"text":  plainText(e.BtnText),
								"type":  btnType,
								"value": valMap,
							},
						},
					},
				},
			})
		case CardSelect:
			var options []map[string]any
			for _, opt := range e.Options {
				options = append(options, map[string]any{
					"text":  plainText(opt.Text),
					"value": opt.Value,
				})
			}
			selectElem := map[string]any{
				"tag":         "select_static",
				"placeholder": plainText(e.Placeholder),
				"options":     options,
			}
			if sessionKey != "" {
				selectElem["value"] = map[string]string{"session_key": sessionKey}
			}
			if e.InitValue != "" {
				selectElem["initial_option"] = e.InitValue
			}
			elements = append(elements, selectElem)
		case CardTable:
			if table, ok := cardTableElement(e); ok {
				elements = append(elements, table)
			}
		case CardNote:
			elements = append(elements, noteDiv(e.Text))
		}
	}

	if len(elements) == 0 {
		elements = []map[string]any{{"tag": "markdown", "content": " "}}
	}

	body := map[string]any{"elements": elements}
	if card.BodyPadding != "" {
		body["padding"] = card.BodyPadding
	}
	if card.BodySpacing != "" {
		body["vertical_spacing"] = card.BodySpacing
	}
	result["body"] = body
	return result
}

// cardTableElement renders a CardTable as a card 2.0 `table` component. The
// second return is false for a table the platform would refuse — no columns or
// no rows — which the caller drops rather than sends.
//
// The header style is fixed here rather than exposed on the model: every table
// this repo sends is a key/value read-out, and the grey header is what makes the
// key column read as a label instead of another row.
func cardTableElement(t CardTable) (map[string]any, bool) {
	if len(t.Columns) == 0 || len(t.Rows) == 0 {
		return nil, false
	}

	columns := make([]map[string]any, 0, len(t.Columns))
	for _, c := range t.Columns {
		if c.Name == "" {
			continue
		}
		col := map[string]any{"name": c.Name, "data_type": stringOr(c.DataType, "text")}
		if c.DisplayName != "" {
			col["display_name"] = c.DisplayName
		}
		if c.Width != "" {
			col["width"] = c.Width
		}
		if c.HorizontalAlign != "" {
			col["horizontal_align"] = c.HorizontalAlign
		}
		columns = append(columns, col)
	}
	if len(columns) == 0 {
		return nil, false
	}

	table := map[string]any{
		"tag":          "table",
		"columns":      columns,
		"rows":         t.Rows,
		"row_height":   stringOr(t.RowHeight, "auto"),
		"header_style": map[string]any{"background_style": "grey", "bold": true},
	}
	if t.PageSize > 0 {
		table["page_size"] = t.PageSize
	}
	if t.FreezeFirstColumn {
		table["freeze_first_column"] = true
	}
	return table, true
}

// stringOr returns v, or fallback when v is empty.
func stringOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// buttonRowColumnSet lays a row of buttons out horizontally. Card 2.0 removed
// the v1 `action` container (using it makes Feishu reject the card with 200861
// "cards of schema V2 no longer support this capability"), so buttons have to
// live inside a column_set. Auto-width columns keep each button at its natural
// size rather than stretching it across the row.
func buttonRowColumnSet(actions []map[string]any, horizontalAlign string) map[string]any {
	columns := make([]map[string]any, 0, len(actions))
	for _, action := range actions {
		columns = append(columns, map[string]any{
			"tag":              "column",
			"width":            "auto",
			"vertical_align":   "center",
			"horizontal_align": "center",
			"elements":         []map[string]any{action},
		})
	}
	return map[string]any{
		"tag":              "column_set",
		"horizontal_align": horizontalAlign,
		"columns":          columns,
	}
}

// ElementMarkdown builds one card 2.0 markdown component carrying an element id.
//
// It exists for the caller that must address a component of a card it did not
// build: the live card's decision block is written into a cardkit card that is
// already on screen, so it needs the component itself rather than a render of a
// whole card. Building it here keeps the shape — and the property names card 2.0
// validates — in the package that owns every other card shape.
func ElementMarkdown(id, content string) map[string]any {
	elem := map[string]any{"tag": "markdown", "content": content}
	if id != "" {
		elem["element_id"] = id
	}
	return elem
}

// ElementButtonRow builds a row of buttons as a card 2.0 column_set, carrying the
// same callback values RenderCardMap would give them (action, session key, extras)
// so a button added to a live card behaves exactly like one the card was built
// with.
func ElementButtonRow(id string, buttons []CardButton, sessionKey string, layout CardActionLayout) map[string]any {
	actions := make([]map[string]any, 0, len(buttons))
	for _, btn := range buttons {
		action := buttonElement(btn, sessionKey)
		if layout == CardActionLayoutEqualColumns {
			action["width"] = "fill"
		}
		actions = append(actions, action)
	}

	var row map[string]any
	if layout == CardActionLayoutEqualColumns {
		columns := make([]map[string]any, 0, len(actions))
		for _, action := range actions {
			columns = append(columns, map[string]any{
				"tag":              "column",
				"width":            "weighted",
				"weight":           1,
				"vertical_align":   "center",
				"horizontal_align": "center",
				"elements":         []map[string]any{action},
			})
		}
		row = map[string]any{"tag": "column_set", "columns": columns}
		if len(actions) == 2 {
			row["flex_mode"] = "bisect"
		}
	} else {
		row = buttonRowColumnSet(actions, "left")
	}
	if id != "" {
		row["element_id"] = id
	}
	return row
}

// buttonElement renders one CardButton as a card 2.0 button component: the single
// place a button's callback value is built, shared by the in-place card renderer
// and by ElementButtonRow.
func buttonElement(btn CardButton, sessionKey string) map[string]any {
	btnType := btn.Type
	if btnType == "" {
		btnType = "default"
	}
	return map[string]any{
		"tag":   "button",
		"text":  plainText(btn.Text),
		"type":  btnType,
		"value": buttonValueMap(btn.Value, sessionKey, btn.Extra),
	}
}

// buttonValueMap builds the callback value map for a button or list item,
// always carrying the logical action and (when present) the session key plus
// any extra key-value pairs.
func buttonValueMap(action, sessionKey string, extra map[string]string) map[string]string {
	valMap := map[string]string{"action": action}
	if sessionKey != "" {
		valMap["session_key"] = sessionKey
	}
	for k, v := range extra {
		valMap[k] = v
	}
	return valMap
}
