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

package duty

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// TestDutyCardKeepsTitleInTheBody pins the shape the /duty card is rendered with
// here rather than by a Feishu template.
//
// The title is body content, not a card header, and that is the whole point: a
// header bar is drawn flush against the card's top edge, which in a quote reply puts
// it right under the "回复 <某人>: /duty" (reply <someone>: /duty) line Feishu draws above the card. Body
// content starts below the body's own top padding, which is the spacing this
// card wants — so a later refactor that "restores" a header has to argue with
// this test first.
func TestDutyCardKeepsTitleInTheBody(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewDutyCmd(env.sr, out) })
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if res.Interactive == nil {
		t.Fatal("no card was built for /duty")
	}

	cardJSON := larkcard.RenderCard(res.Interactive, "oc_1")
	if violations := larkcard.Card2Violations([]byte(cardJSON)); len(violations) > 0 {
		t.Fatalf("card 2.0 would reject the card: %v", violations)
	}

	root := decodeCard(t, cardJSON)
	if _, ok := root["header"]; ok {
		t.Errorf("the card grew a header; a header bar sits flush with the card's top edge, which is the cramped layout this shape avoids")
	}

	body, _ := root["body"].(map[string]any)
	if body["padding"] != dutyCardBodyPadding || body["vertical_spacing"] != dutyCardBodySpacing {
		t.Errorf("body spacing = padding %v / spacing %v, want %q / %q",
			body["padding"], body["vertical_spacing"], dutyCardBodyPadding, dutyCardBodySpacing)
	}
	elements, _ := body["elements"].([]any)
	if len(elements) < 2 {
		t.Fatalf("card has %d elements, want a title and a roster table", len(elements))
	}
	first, _ := elements[0].(map[string]any)
	title, _ := first["content"].(string)
	if first["tag"] != "markdown" || !strings.Contains(title, "本周值班") {
		t.Errorf("first element = %+v, want the title markdown", first)
	}
	if !strings.Contains(title, getThisWeekRange()) {
		t.Errorf("title = %q, want it to carry the week range", title)
	}

	table := cardTable(t, elements)
	if got := table["rows"]; !strings.Contains(fmt.Sprint(got), "Lingfei Kong") {
		t.Errorf("table rows = %v, want the on-call watcher", got)
	}
	// The duty group (值班组) column carries the group's name and nothing else — not the
	// display name the store also holds ("EKS"), and not a description.
	if got := fmt.Sprint(table["rows"]); !strings.Contains(got, "eks") || strings.Contains(got, "EKS") {
		t.Errorf("table rows = %v, want the group's name only", got)
	}
}

// TestDutyCardFillsItsWidthOnSingleLineRows pins the roster table's layout: the
// three columns take percentage widths that sum to the whole canvas, and every
// row is one line tall so a cell the column cannot fit is ellipsized (with the
// full text on hover) instead of wrapping onto a second line.
//
// Both halves are the fix for a table whose columns were fixed px: whatever the
// card's width, the columns stayed 80–240px and the labels wrapped inside them.
func TestDutyCardFillsItsWidthOnSingleLineRows(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command { return NewDutyCmd(env.sr, out) })
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if res.Interactive == nil {
		t.Fatal("no card was built for /duty")
	}

	body := decodeCard(t, larkcard.RenderCard(res.Interactive, "oc_1"))["body"].(map[string]any)
	table := cardTable(t, body["elements"].([]any))
	cols := table["columns"].([]any)
	if len(cols) != 3 {
		t.Fatalf("columns = %v, want the group and the two roster columns", cols)
	}
	module, primary, backup := cols[0].(map[string]any), cols[1].(map[string]any), cols[2].(map[string]any)

	// Every column is a percentage, and the three add up to the canvas: fixed px
	// would leave the table behind however wide the card grows.
	wantWidths := []string{dutyModuleColumnWidth, dutyOncallColumnWidth, dutyOncallColumnWidth}
	total := 0.0
	for i, col := range []map[string]any{module, primary, backup} {
		width, _ := col["width"].(string)
		if width != wantWidths[i] {
			t.Errorf("column %d width = %q, want %q", i, width, wantWidths[i])
		}
		pct, ok := percentWidth(width)
		if !ok {
			t.Errorf("column %d width = %q, want a percentage", i, width)
			continue
		}
		total += pct
	}
	if total != 100 {
		t.Errorf("column widths add up to %v%%, want 100%% so the table fills the card", total)
	}

	// A fixed row height is what turns overflow into an ellipsis; "auto" would
	// grow the row and wrap the cell instead.
	if got := table["row_height"]; got != dutyTableRowHeight {
		t.Errorf("row_height = %v, want %q", got, dutyTableRowHeight)
	}
}

// percentWidth parses a card 2.0 percentage column width ("40%") back to 40.
func percentWidth(width string) (float64, bool) {
	trimmed, ok := strings.CutSuffix(width, "%")
	if !ok {
		return 0, false
	}
	pct, err := strconv.ParseFloat(trimmed, 64)
	return pct, err == nil
}

// decodeCard unmarshals a rendered card.
func decodeCard(t *testing.T, cardJSON string) map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal([]byte(cardJSON), &root); err != nil {
		t.Fatalf("card json does not parse: %v", err)
	}
	return root
}

// cardTable returns the roster table out of a card's decoded body elements.
func cardTable(t *testing.T, elements []any) map[string]any {
	t.Helper()
	for _, elem := range elements {
		e, _ := elem.(map[string]any)
		if e["tag"] == "table" {
			return e
		}
	}
	t.Fatal("card carries no roster table")
	return nil
}
