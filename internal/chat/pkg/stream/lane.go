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

package stream

import (
	"strings"
	"unicode/utf8"
)

// laneID names one of the three content channels a streaming card carries.
type laneID int

const (
	// laneAnswer is the answer the user is waiting for.
	laneAnswer laneID = iota
	// laneReasoning is the model's reasoning, shown separately so it never
	// contaminates the answer.
	laneReasoning
	// laneProgress is the tool-step panel.
	laneProgress
)

// laneOrder fixes the order lanes are published in. The order is arbitrary —
// Sequence guarantees each publish outranks the previous one regardless — but it
// is fixed so a run is reproducible.
var laneOrder = []laneID{laneAnswer, laneReasoning, laneProgress}

// lane is one content channel: everything accumulated for it so far, and what the
// sink was last shown.
//
// What the sink holds is remembered as the text itself rather than as a length.
// A count cannot tell a rewritten lane from an unchanged one — the progress panel
// is re-rendered wholesale, and a step flipping from "⟳ 读取文件" (reading file) to
// "✓ 读取文件" (file read) changes what the reader must see without changing how many
// characters it takes to say it. Lengths are still what the threshold is measured
// in, because that is what the configuration is expressed in, and they are
// counted in runes: bytes would make a character-threshold thirty times looser
// for CJK text than for Latin, so one configuration would stream smoothly in
// English and in visible bursts in Chinese.
type lane struct {
	buf strings.Builder
	// grows reports whether content is appended to this lane or replaces it.
	// A growing lane has a meaningful delta to accumulate, so its publish can
	// wait for one; a replaced lane is always wholly new, and waiting for a
	// character delta would mean a step flipping from pending to done — the one
	// update the reader is watching for — stays invisible until the text also
	// happens to get longer.
	grows bool
	// n is the rune count of buf.
	n int
	// sent is the text the sink was last shown, and has reports whether it has
	// been shown anything at all — an empty lane that was never published is not
	// the same as one published with empty content, because only the first is
	// worth skipping.
	sent string
	has  bool
}

// append adds a delta to the lane.
func (l *lane) append(delta string) {
	l.buf.WriteString(delta)
	l.n += utf8.RuneCountInString(delta)
}

// set replaces the lane's content.
func (l *lane) set(text string) {
	l.buf.Reset()
	l.buf.WriteString(text)
	l.n = utf8.RuneCountInString(text)
}

// text returns the lane's full current content.
func (l *lane) text() string { return l.buf.String() }

// pending returns how far behind the sink the given outbound text is, in runes.
// A replaced lane can be shorter than what was published, so the difference is
// absolute: shrinking is a change worth publishing too.
func (l *lane) pending(text string) int {
	d := utf8.RuneCountInString(text) - utf8.RuneCountInString(l.sent)
	if d < 0 {
		return -d
	}
	return d
}

// stale reports whether text is worth sending: non-empty, and different from
// what the sink already holds.
func (l *lane) stale(text string) bool {
	if text == "" {
		return false
	}
	return !l.has || l.sent != text
}

// markSent records that the sink now holds text.
func (l *lane) markSent(text string) {
	l.sent = text
	l.has = true
}

// truncateRunes cuts text to at most max runes, returning it unchanged when it is
// already short enough.
func truncateRunes(text string, max int) string {
	if max <= 0 || utf8.RuneCountInString(text) <= max {
		return text
	}
	i := 0
	for pos := range text {
		if i == max {
			return text[:pos]
		}
		i++
	}
	return text
}
