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

package chat

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCanonicalSummaryFoldsTheModelsPunctuation is the fix for a real divergence.
//
// The query excludes the marker with an exact string comparison, because SQL has no
// portable way to strip a trailing punctuation set — and the model sometimes ends its
// one-sentence answer with one. While the Go side tolerated that and the SQL side did
// not, a ticket the model had declined was published anyway: my own fixture wrote
// 「记录简略，无需复盘。」 and the candidate list returned it. The tolerance now lives
// at the single point where an answer becomes a stored value, so both readers can
// compare exactly.
func TestCanonicalSummaryFoldsTheModelsPunctuation(t *testing.T) {
	for _, in := range []string{
		SummaryBrief,
		SummaryBrief + "。",
		SummaryBrief + "!!",
		"  " + SummaryBrief + "  ",
	} {
		assert.Equal(t, SummaryBrief, CanonicalSummary(in), "input %q", in)
	}

	// A write-up is stored as written, byte for byte: this function's whole job is to
	// absorb one specific imprecision, and a version that tidied anything else would
	// be editing somebody's answer.
	writeUp := "## 问题现象\n网关 502\n"
	assert.Equal(t, writeUp, CanonicalSummary(writeUp))
	assert.Equal(t, "", CanonicalSummary(""))
}

// TestIsBriefSummaryRecognisesTheMarker is the check the canonicaliser uses.
//
// It has to be tolerant in one direction and strict in the other, and both are
// load-bearing: a marker the model ended with a full stop still means "do not
// publish this", while an answer that merely *mentions* the marker is a write-up
// and must go out.
func TestIsBriefSummaryRecognisesTheMarker(t *testing.T) {
	for _, tc := range []struct {
		why, in string
		want    bool
	}{
		{"the marker itself", SummaryBrief, true},
		{"with a full stop", SummaryBrief + "。", true},
		{"with an ascii full stop", SummaryBrief + ".", true},
		{"with exclamation", SummaryBrief + "！", true},
		{"with surrounding blanks", "  " + SummaryBrief + "\n", true},

		{"a write-up", "## 问题现象\n网关 502", false},
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"the marker with something after it", SummaryBrief + "\n但这次值得记一笔", false},
		{"something before the marker", "先说结论：" + SummaryBrief, false},
		{"a different sentence of the same length", "记录简略但仍需复盘", false},
	} {
		t.Run(tc.why, func(t *testing.T) {
			assert.Equal(t, tc.want, IsBriefSummary(tc.in))
		})
	}
}
