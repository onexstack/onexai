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
	"regexp"
	"sort"
)

// Card JSON 2.0 validates component properties strictly, and every card in this
// repo is built as raw maps: nothing in the type system keeps a property off the
// component it does not belong to. Feishu's answer to a stray one is to reject the
// whole card —
//
//	200621: parse card json err, unknown property, property: text_color,
//	path: ROOT -> body -> elements -> [4](tag: div)
//
// — which is how a footnote carrying text_color on its div kept every note-bearing
// card (the question card, the approval card, /help) from being delivered at all.
// 1.0 ignored a property it did not know; 2.0 does not, so a shape that used to be
// harmless now fails the card.
//
// Card2Violations is the offline half of that check: it walks a rendered card and
// reports each property that is not in the whitelist for the component it sits on,
// phrased the way the API phrases it, so a failing test reads like the error it
// exists to prevent.
//
// The tables below are whitelists, not a schema. An entry means "this repo puts this
// property here, and Feishu has been seen to accept it" — confirmed either against
// the card 2.0 reference or in a card delivered live. A property that is missing is
// not automatically invalid; it is unaccounted for, which is exactly the state the
// text_color bug shipped in. Add it only after the shape has been put in front of
// Feishu and accepted — a cardkit-created card proves the creation path, not the
// update path.
type card2PropSet map[string]bool

func card2Props(names ...string) card2PropSet {
	set := make(card2PropSet, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

// card2ComponentProps is the whitelist for every component this repo emits, keyed
// by its `tag`.
var card2ComponentProps = map[string]card2PropSet{
	// div's inner text carries the size and colour; the div itself does not take
	// them (this is the 200621 above).
	"div":           card2Props("tag", "text", "icon"),
	"markdown":      card2Props("tag", "content", "element_id", "text_size"),
	"plain_text":    card2Props("tag", "content", "text_size", "text_color"),
	"lark_md":       card2Props("tag", "content"),
	"button":        card2Props("tag", "text", "type", "value", "name", "form_action_type", "width", "size", "element_id", "behaviors"),
	"column_set":    card2Props("tag", "columns", "flex_mode", "horizontal_align", "element_id"),
	"column":        card2Props("tag", "width", "weight", "vertical_align", "horizontal_align", "elements"),
	"select_static": card2Props("tag", "placeholder", "options", "value", "initial_option"),
	"table": card2Props("tag", "columns", "rows", "page_size", "row_height", "row_max_height",
		"freeze_first_column", "header_style", "margin"),
	"checker": card2Props("tag", "name", "checked", "text"),
	"form":    card2Props("tag", "name", "elements"),

	"collapsible_panel": card2Props("tag", "header", "elements", "element_id", "expanded",
		"background_color", "border", "vertical_spacing", "padding"),
	"hr":            card2Props("tag"),
	"standard_icon": card2Props("tag", "token", "color"),
}

// card2ContainerProps is the whitelist for the maps that carry no tag of their own:
// the card root and the fixed-shape objects hanging off it.
var card2ContainerProps = map[string]card2PropSet{
	"root":             card2Props("schema", "config", "header", "body"),
	"config":           card2Props("width_mode", "streaming_mode", "streaming_config", "summary", "enable_forward", "enable_forward_interaction", "update_multi"),
	"streaming_config": card2Props("print_frequency_ms", "print_step", "print_strategy"),
	"print_hint":       card2Props("default", "pc", "mobile"),
	"card_header":      card2Props("title", "template"),
	"panel_header":     card2Props("title"),
	"body":             card2Props("elements", "padding", "vertical_spacing"),
	"border":           card2Props("color"),
	"option":           card2Props("text", "value"),
	// A table's column objects carry no tag, so they cannot be checked as
	// components; this set is what they are checked against instead.
	"table_column": card2Props("name", "display_name", "data_type", "width",
		"horizontal_align", "vertical_align", "format", "date_format"),
	"behavior": card2Props("type", "value"),
}

// Card2Violations returns one message per property that is not whitelisted for the
// component or container it sits on, and per element_id the platform would refuse.
// An empty result means every property and every id in the card is one this repo
// has already put in front of Feishu.
func Card2Violations(cardJSON []byte) []string {
	var root any
	if err := json.Unmarshal(cardJSON, &root); err != nil {
		return []string{"card JSON does not parse: " + err.Error()}
	}

	var violations []string
	walkCard2(root, "ROOT", "root", &violations)
	return violations
}

// card2ElementID is what cardkit accepts as an element id, in the platform's own
// terms: letters, digits and underscores, starting with a letter, at most 20
// characters.
var card2ElementID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,19}$`)

// checkCard2ElementID reports an id the platform would refuse.
//
// The message keeps the platform's phrasing, as the property checks do, so a
// failing test reads like the error it exists to prevent. This check earns its
// place: an id built at runtime — decision_grant_1 was the one — never appears in
// a golden, and the id being four characters too long turned every approval into
// two cards instead of one, because the component could not be added to the card
// the turn was already streaming into.
func checkCard2ElementID(id, path string, violations *[]string) {
	if card2ElementID.MatchString(id) {
		return
	}
	*violations = append(*violations, fmt.Sprintf(
		"%s: elementID format error: only alphabets, numbers, and underscores are allowed; "+
			"it must start with an alphabet and not exceed 20 characters. ElementID:%s", path, id))
}

// walkCard2 checks one node and then descends into the fields that hold further
// components. Only those fields are followed: callback data (value, initial_option)
// and scalars are left alone, so an action's own keys are never mistaken for card
// properties.
func walkCard2(node any, path, container string, violations *[]string) {
	switch v := node.(type) {
	case []any:
		for i, item := range v {
			walkCard2(item, fmt.Sprintf("%s -> [%d]", path, i), container, violations)
		}
	case map[string]any:
		// An element id is checked wherever it appears, whatever component carries
		// it, because cardkit checks it too — and it does so at the one moment a
		// card cannot be rebuilt around the mistake: when a component is added to a
		// card that is already on screen.
		if id, ok := v["element_id"].(string); ok {
			checkCard2ElementID(id, path, violations)
		}

		tag, _ := v["tag"].(string)
		switch {
		case tag != "":
			allowed, known := card2ComponentProps[tag]
			if !known {
				*violations = append(*violations, path+": unknown component tag "+tag)
				return
			}
			checkCard2Props(v, path, tag, allowed, violations)
		case container == "component":
			*violations = append(*violations, path+": nested component has no tag")
			return
		default:
			checkCard2Props(v, path, "", card2ContainerProps[container], violations)
		}

		for _, key := range card2SortedKeys(v) {
			child := v[key]
			switch key {
			case "elements":
				walkCard2(child, path+" -> elements", "component", violations)
			case "columns":
				// column_set's columns are tagged components; a table's are
				// plain objects with a different, smaller property set.
				container := "component"
				if tag == "table" {
					container = "table_column"
				}
				walkCard2(child, path+" -> columns", container, violations)
			case "options":
				walkCard2(child, path+" -> options", "option", violations)
			case "behaviors":
				walkCard2(child, path+" -> behaviors", "behavior", violations)
			case "text", "icon", "title":
				walkCard2(child, path+" -> "+key, "component", violations)
			case "header":
				// The two headers have different fields: a card header takes a
				// template, a panel header does not.
				container := "card_header"
				if tag != "" {
					container = "panel_header"
				}
				walkCard2(child, path+" -> header", container, violations)
			case "border":
				walkCard2(child, path+" -> border", "border", violations)
			case "body":
				walkCard2(child, path+" -> body", "body", violations)
			case "config":
				walkCard2(child, path+" -> config", "config", violations)
			case "streaming_config":
				walkCard2(child, path+" -> streaming_config", "streaming_config", violations)
			case "print_frequency_ms", "print_step":
				walkCard2(child, path+" -> "+key, "print_hint", violations)
			}
		}
	}
}

func checkCard2Props(m map[string]any, path, tag string, allowed card2PropSet, violations *[]string) {
	where := path
	if tag != "" {
		where += "(tag: " + tag + ")"
	}
	for _, key := range card2SortedKeys(m) {
		if !allowed[key] {
			*violations = append(*violations, where+": unknown property "+key)
		}
	}
}

func card2SortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
