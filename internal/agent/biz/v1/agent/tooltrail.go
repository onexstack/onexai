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

package agent

import (
	"strings"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// toolTrailMarker heads the record appended to a conversation after a run.
//
// It is marked as system-generated for the same reason the history summary is
// (see summaryMarker): the record is the one part of the exchange the run did
// not author, and a reader — human or model — has to be able to tell it apart
// from something a participant said.
const toolTrailMarker = "【本轮工具调用记录（系统生成）】\n"

// toolTrail is the record of a run's tool calls, reduced to the two facts a
// later turn needs in order to know what actually happened: which tools ran, and
// whether each succeeded.
//
// It exists because the conversation's only memory of a completed run used to be
// the assistant's own prose. That made a claim unfalsifiable: a run that said it
// had triggered a release without calling anything left the claim behind as a
// fact, and the next turn read it back as its own past — which is how one
// conversation comes to insist it built something it never touched.
//
// It is written on every turn, including the turns that called nothing.
// "No calls" has to be as visible as the calls themselves, because a record
// written only when there was something to report would leave its absence
// ambiguous, and the empty case is exactly the one worth catching.
type toolTrail struct {
	// order holds the call ids in first-seen order. The record reads in the
	// order the tools ran, which mapping iteration would not give.
	order []string
	// rows maps a call id to its rendered row, so the later reports of one
	// invocation replace it rather than adding another.
	rows map[string]string
	// anonymous holds the steps a backend reported with no call id. There is
	// nothing to match a later report on, so they can only be appended.
	anonymous []string
}

// newToolTrail starts a record, continuing one carried across an approval pause.
//
// A resumed run is rebuilt from its stored metadata and replays none of the
// tools that ran before it paused, so the record taken from the resume alone
// would describe half a turn and present it as the whole of one.
func newToolTrail(seed []string) *toolTrail {
	t := &toolTrail{rows: make(map[string]string, len(seed))}
	// The seed is already rendered, so the call it came from is not recoverable
	// — and does not need to be: nothing in the resumed run can supersede a call
	// that has already finished.
	t.anonymous = append(t.anonymous, seed...)
	return t
}

// observe folds one report of tool steps into the record.
//
// The backend reports an invocation when it starts and again when it ends, under
// the same call id, so a row is replaced rather than repeated. The replacing row
// is the later one by construction: the terminal report is what carries the
// status the call actually ended in.
func (t *toolTrail) observe(steps []agentapi.ToolStep) {
	for _, st := range steps {
		row := st.Name + "(" + st.Status + ")"
		if st.ID == "" {
			t.anonymous = append(t.anonymous, row)
			continue
		}
		if _, seen := t.rows[st.ID]; !seen {
			t.order = append(t.order, st.ID)
		}
		t.rows[st.ID] = row
	}
}

// record returns the rows in the order the calls were first seen.
func (t *toolTrail) record() []string {
	out := make([]string, 0, len(t.anonymous)+len(t.order))
	out = append(out, t.anonymous...)
	for _, id := range t.order {
		out = append(out, t.rows[id])
	}
	return out
}

// renderToolTrail renders the record as the annotation appended to the
// conversation. An empty record is rendered rather than omitted; see toolTrail.
func renderToolTrail(record []string) string {
	if len(record) == 0 {
		return toolTrailMarker + "- 无（本次运行没有调用任何工具）"
	}

	var b strings.Builder
	b.WriteString(toolTrailMarker)
	for _, row := range record {
		b.WriteString("- ")
		b.WriteString(row)
		b.WriteString("\n")
	}
	// The marker already ends in a newline and each row adds its own, so the
	// trailing one is dropped: the annotation sits in a conversation whose next
	// message is a user turn, and a stray blank line there is the sort of thing
	// that gets copied into an answer.
	return strings.TrimSuffix(b.String(), "\n")
}
