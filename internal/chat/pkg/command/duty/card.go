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
	"strings"

	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
)

const (
	// dutyTablePageSize is how many roster rows the native table shows at a
	// time. The platform accepts 1–10 and pages the rest on the client, which
	// is why the card carries no page controls of its own.
	dutyTablePageSize = 10
	// dutyEmptyCell is what a roster column shows when there is nobody in it:
	// a dash rather than an empty cell, so a group with no backup reads as
	// "nobody" and not as a rendering gap.
	dutyEmptyCell = "—"

	// dutyCardBodyPadding and dutyCardBodySpacing set the spacing inside the
	// card. Both are spelled with the platform's own units (CSS-like shorthand
	// and a px length) rather than left to the platform's default, because this
	// spacing is the reason the card is built here: the extra 16px on top is
	// what separates the title from the "回复 <某人>: /duty" (reply <someone>: /duty) line Feishu draws
	// above a reply. Tune them here — nowhere else reads these numbers.
	dutyCardBodyPadding = "16px 12px 12px 12px"
	dutyCardBodySpacing = "8px"

	// The table's three headers.
	dutyModuleHeader  = "值班组"
	dutyPrimaryHeader = "主值班"
	dutyBackupHeader  = "备值班"

	// Column widths, as a share of the table canvas (the card width minus its
	// side padding). The three sum to 100% so the roster fills the card instead
	// of sitting in a fixed-pixel table in the middle of it: with width_mode
	// fill the canvas grows with the chat window, and only a percentage column
	// grows with it. Fixed px is the trap this card used to fall into — the
	// columns stayed 80–240px wide however wide the card got, so the labels
	// wrapped inside them.
	//
	// The roster columns take the larger share because they carry the longest
	// labels: each holds a handful of names joined into one cell, while the group
	// column carries only the group's name — the identifier every other /duty
	// spelling takes as an argument ("container-platform").
	dutyModuleColumnWidth = "30%"
	dutyOncallColumnWidth = "35%"

	// dutyTableRowHeight keeps every roster row one line tall. A cell that
	// cannot fit is ellipsized by the client, which shows the full text again
	// when the pointer hovers over it — the whole reason the row height is
	// pinned here. The platform's "auto" instead grows the row to fit, which
	// wraps the label onto a second line rather than ending it in an ellipsis.
	dutyTableRowHeight = "low"
)

// renderDutyCard draws the /duty answer as a card this repo renders itself.
//
// The answer used to be a Feishu-hosted template (DutyListCardTemplateID): only
// the template id travelled in the code, so nothing about the layout — the
// spacing the reader was looking at — could be changed from here. The card is
// built here instead, and the title is body content rather than a card header on
// purpose: a header bar is drawn flush against the card's top edge, while body
// content starts below the body's top padding (dutyCardBodyPadding). In a
// a quote reply that difference is the gap between the "回复 <某人>: /duty" (reply <someone>: /duty) line Feishu
// draws above the card and the card's first line. The test pins the shape, so a
// later refactor cannot quietly put the header back.
func renderDutyCard(groups []groupView, notes []string, weekRange, maintainer string) *larkcard.Card {
	cb := larkcard.NewCard().BodyLayout(dutyCardBodyPadding, dutyCardBodySpacing)

	title := "**📅 本周值班**"
	if weekRange != "" {
		title += "　" + weekRange
	}
	cb.Markdown(title)

	rows := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		rows = append(rows, map[string]any{
			"module":  g.code,
			"primary": joinWatchers(g.primary),
			"backup":  joinWatchers(g.backup),
		})
	}

	cb.Table(larkcard.CardTable{
		Columns: []larkcard.CardTableColumn{
			{Name: "module", DisplayName: dutyModuleHeader, DataType: "lark_md", Width: dutyModuleColumnWidth},
			{Name: "primary", DisplayName: dutyPrimaryHeader, DataType: "lark_md", Width: dutyOncallColumnWidth},
			{Name: "backup", DisplayName: dutyBackupHeader, DataType: "lark_md", Width: dutyOncallColumnWidth},
		},
		Rows:      rows,
		PageSize:  dutyTablePageSize,
		RowHeight: dutyTableRowHeight,
	})

	// A name that did not resolve is reported under the table rather than
	// dropped: the rows above are the answer to the part of the query that
	// worked, and saying nothing about the rest would read as if it had.
	for _, note := range notes {
		cb.Markdown("⚠️ " + note)
	}
	if maintainer != "" {
		cb.Note("无值班人时请联系：" + maintainer)
	}
	return cb.Build()
}

// joinWatchers renders one roster cell.
func joinWatchers(names []string) string {
	if len(names) == 0 {
		return dutyEmptyCell
	}
	return strings.Join(names, "、")
}
