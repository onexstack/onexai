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
	"encoding/hex"
	"sort"
	"strings"
)

// This file implements the delete-mode multi-select form card: a Lark `checker`
// list wrapped in a `form` so a user can select multiple sessions to delete in a
// single submit. The design mirrors cc-connect's `/delete` card (see its
// 2026-03-11-feishu-delete-card design docs): the session id is the single
// source of truth, encoded into each checker's name, and the submit button
// carries the action prefix "act:/delete-mode form-submit".

const deleteModeCheckerNamePrefix = "delete_sel_"

// deleteModeCheckerName encodes a session id into a Lark checker element name.
func deleteModeCheckerName(sessionID string) string {
	return deleteModeCheckerNamePrefix + hex.EncodeToString([]byte(sessionID))
}

// parseDeleteModeCheckerName decodes a checker element name back into the
// session id. It returns ok=false when the name is not a delete-mode checker.
func parseDeleteModeCheckerName(name string) (string, bool) {
	if !strings.HasPrefix(name, deleteModeCheckerNamePrefix) {
		return "", false
	}
	raw := strings.TrimPrefix(name, deleteModeCheckerNamePrefix)
	if raw == "" {
		return "", false
	}
	b, err := hex.DecodeString(raw)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// CollectDeleteModeSelectedFromFormValue extracts the selected session ids from
// a submitted form's value map. It only keeps keys that are delete-mode checker
// names whose value is truthy, de-duplicates, and returns them sorted for stable
// ordering.
func CollectDeleteModeSelectedFromFormValue(formValue map[string]any) []string {
	if len(formValue) == 0 {
		return nil
	}
	ids := make([]string, 0, len(formValue))
	seen := make(map[string]struct{}, len(formValue))
	for key, val := range formValue {
		sessionID, ok := parseDeleteModeCheckerName(key)
		if !ok || !isTruthyFormValue(val) {
			continue
		}
		if _, exists := seen[sessionID]; exists {
			continue
		}
		seen[sessionID] = struct{}{}
		ids = append(ids, sessionID)
	}
	sort.Strings(ids)
	return ids
}

// isTruthyFormValue interprets a Lark form value as a boolean, tolerating the
// varying concrete types the SDK may unmarshal into (bool, string, float64,
// int, int64).
func isTruthyFormValue(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		return s == "true" || s == "1" || s == "yes" || s == "on"
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	default:
		return false
	}
}

// deleteModeCheckerRow is one selectable row in the delete-mode form.
type deleteModeCheckerRow struct {
	id      string
	text    string
	checked bool
}

// renderDeleteModeCheckerCard rewrites a Card into a delete-mode checker form
// when its elements follow the delete-mode convention. It returns ok=false when
// the card is not a delete-mode card, in which case the caller falls back to the
// regular renderer.
func renderDeleteModeCheckerCard(card *Card, base map[string]any) (map[string]any, bool) {
	formRowElements := make([]map[string]any, 0)
	notes := make([]CardNote, 0)
	navRows := make([]CardActions, 0)
	submitText := ""
	cancelText := ""

	for _, elem := range card.Elements {
		switch e := elem.(type) {
		case CardListItem:
			id, selectable, ok := parseDeleteModeListItemAction(e.BtnValue)
			if !ok {
				return nil, false
			}
			text := normalizeDeleteModeCheckerText(e.Text)
			if !selectable {
				formRowElements = append(formRowElements, map[string]any{
					"tag":     "markdown",
					"content": "▶ " + text,
				})
				continue
			}
			row := deleteModeCheckerRow{
				id:      id,
				text:    text,
				checked: strings.Contains(e.Text, "☑"),
			}
			formRowElements = append(formRowElements, map[string]any{
				"tag":     "checker",
				"name":    deleteModeCheckerName(row.id),
				"checked": row.checked,
				"text": map[string]any{
					"tag":     "lark_md",
					"content": row.text,
				},
			})
		case CardNote:
			notes = append(notes, e)
		case CardActions:
			remaining := make([]CardButton, 0, len(e.Buttons))
			for _, btn := range e.Buttons {
				switch btn.Value {
				case "act:/delete-mode confirm":
					submitText = btn.Text
				case "act:/delete-mode cancel":
					cancelText = btn.Text
				default:
					remaining = append(remaining, btn)
				}
			}
			navRows = append(navRows, CardActions{Buttons: remaining, Layout: e.Layout})
		case CardMarkdown, CardDivider, CardSelect:
			return nil, false
		}
	}

	if len(formRowElements) == 0 || submitText == "" {
		return nil, false
	}

	elements := make([]map[string]any, 0, len(notes)+1+len(navRows))
	for _, n := range notes {
		if n.Text == "" || n.Tag == "delete-mode-selected-count" {
			continue
		}
		elements = append(elements, noteDiv(n.Text))
	}
	formElements := append([]map[string]any{}, formRowElements...)

	buttonColumns := []map[string]any{
		{
			"tag":            "column",
			"width":          "auto",
			"vertical_align": "center",
			"elements": []map[string]any{
				{
					"tag":              "button",
					"text":             plainText(submitText),
					"type":             "danger",
					"name":             "delete_mode_submit",
					"form_action_type": "submit",
					"value":            map[string]string{"action": "act:/delete-mode form-submit"},
				},
			},
		},
	}
	if cancelText != "" {
		buttonColumns = append(buttonColumns, map[string]any{
			"tag":            "column",
			"width":          "auto",
			"vertical_align": "center",
			"elements": []map[string]any{
				{
					"tag":   "button",
					"text":  plainText(cancelText),
					"type":  "default",
					"name":  "delete_mode_cancel",
					"value": map[string]string{"action": "act:/delete-mode cancel"},
				},
			},
		})
	}
	formElements = append(formElements, map[string]any{
		"tag":              "column_set",
		"horizontal_align": "left",
		"columns":          buttonColumns,
	})

	elements = append(elements, map[string]any{
		"tag":      "form",
		"name":     "delete_mode_form",
		"elements": formElements,
	})

	for _, row := range navRows {
		actions := make([]map[string]any, 0, len(row.Buttons))
		for _, btn := range row.Buttons {
			btnType := btn.Type
			if btnType == "" {
				btnType = "default"
			}
			valMap := buttonValueMap(btn.Value, "", btn.Extra)
			action := map[string]any{
				"tag":   "button",
				"text":  plainText(btn.Text),
				"type":  btnType,
				"value": valMap,
			}
			if row.Layout == CardActionLayoutEqualColumns {
				action["width"] = "fill"
			}
			actions = append(actions, action)
		}
		if len(actions) > 0 {
			elements = append(elements, buttonRowColumnSet(actions, "left"))
		}
	}

	base["body"] = map[string]any{"elements": elements}
	return base, true
}

// normalizeDeleteModeCheckerText strips the leading status marker ("☑ ▶" etc.)
// from a checker row's display text.
func normalizeDeleteModeCheckerText(text string) string {
	trimmed := strings.TrimSpace(text)
	for _, prefix := range []string{"☑ ▶", "◻ ▶", "▶", "☑", "◻"} {
		if strings.HasPrefix(trimmed, prefix) {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			break
		}
	}
	return trimmed
}

// parseDeleteModeListItemAction parses a delete-mode list-item action value. It
// distinguishes selectable rows ("act:/delete-mode toggle <id>") from
// non-selectable ones ("act:/delete-mode noop <id>").
func parseDeleteModeListItemAction(action string) (id string, selectable bool, ok bool) {
	const (
		togglePrefix = "act:/delete-mode toggle "
		noopPrefix   = "act:/delete-mode noop "
	)
	switch {
	case strings.HasPrefix(action, togglePrefix):
		id = strings.TrimSpace(strings.TrimPrefix(action, togglePrefix))
		return id, true, id != ""
	case strings.HasPrefix(action, noopPrefix):
		id = strings.TrimSpace(strings.TrimPrefix(action, noopPrefix))
		return id, false, id != ""
	default:
		return "", false, false
	}
}
