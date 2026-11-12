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
	"strings"
	"testing"
)

// TestFormatTokens pins every unit boundary, including the two the abbreviation
// must not get wrong: the last value below a unit, and the first value that
// rounds up into the next one.
func TestFormatTokens(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{-5, "0"},
		{1, "1"},
		{402, "402"},
		{820, "820"},
		{999, "999"},
		{1000, "1k"},
		{1340, "1.3k"},
		{2000, "2k"},
		{2100, "2.1k"},
		{21500, "21.5k"},
		{999499, "999.5k"},
		// Rounds to 1000k, which would read as a unit error.
		{999999, "1m"},
		{1000000, "1m"},
		{1240000, "1.2m"},
		{21500000, "21.5m"},
	}

	for _, tc := range cases {
		if got := FormatTokens(tc.in); got != tc.want {
			t.Errorf("FormatTokens(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFormatDurationMs pins the boundaries of the three shapes: sub-second,
// sub-minute with its mandatory decimal, and minutes and up.
func TestFormatDurationMs(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0ms"},
		{-1, "0ms"},
		{1, "1ms"},
		{89, "89ms"},
		{234, "234ms"},
		{999, "999ms"},
		// Keeps the ".0": a duration is a measurement, not a magnitude.
		{1030, "1.0s"},
		{1500, "1.5s"},
		{59999, "60.0s"},
		{60000, "1m0s"},
		{74000, "1m14s"},
		{3600000, "1h0m0s"},
	}

	for _, tc := range cases {
		if got := FormatDurationMs(tc.in); got != tc.want {
			t.Errorf("FormatDurationMs(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestToolStepFiguresShowsDurationAndTokens covers the figures the row puts in
// parentheses: the exit code first, then " | "-joined duration and tokens, and the
// whole line omitted when the backend reported neither — an unreported figure is
// not a zero.
func TestToolStepFiguresShowsDurationAndTokens(t *testing.T) {
	exit := 2
	cases := []struct {
		name string
		step RichStep
		want string
	}{
		{
			name: "both figures",
			step: RichStep{
				Kind: RichStepKindTool, Name: "exec", Summary: "ls -la",
				Status: "ok", DurationMs: 234, Tokens: 820,
			},
			want: "⚡ 234ms | ✨ 820",
		},
		{
			name: "neither reported",
			step: RichStep{
				Kind: RichStepKindTool, Name: "exec", Summary: "ls -la", Status: "ok",
			},
			want: "",
		},
		{
			name: "still pending, so no tokens yet",
			step: RichStep{
				Kind: RichStepKindTool, Name: "exec", Summary: "ls -la",
				Status: "pending", DurationMs: 1200,
			},
			want: "⚡ 1.2s",
		},
		{
			name: "exit code comes before the new figures",
			step: RichStep{
				Kind: RichStepKindTool, Name: "exec", Summary: "ls -la",
				Status: "fail", ExitCode: &exit, DurationMs: 89, Tokens: 402,
			},
			want: "退出码：2 | ⚡ 89ms | ✨ 402",
		},
		{
			name: "a thinking step carries no metadata line",
			step: RichStep{
				Kind: RichStepKindThinking, Summary: "weighing options",
				DurationMs: 500, Tokens: 100,
			},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The steps under test carry their duration in the field, which is
			// what the final card passes; the live panel's waiting time is covered
			// by the bot package's renderToolsProgress test.
			if got := ToolStepFigures(tc.step, tc.step.DurationMs); got != tc.want {
				t.Errorf("ToolStepFigures() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRichStepBadge pins the status vocabulary. The card shows the state as a
// coloured icon and the text paths as a glyph, so a status that resolved to the
// wrong badge would report a failure as a success — or, worse, an unrecognised
// status as one the user should trust.
func TestRichStepBadge(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name    string
		status  string
		success *bool
		want    statusBadge
	}{
		{name: "ok", status: "ok", want: badgeDone},
		{name: "fail", status: "fail", want: badgeFailed},
		{name: "pending", status: "pending", want: badgeRunning},
		{name: "success flag", success: &yes, want: badgeDone},
		{name: "failure flag", success: &no, want: badgeFailed},
		{name: "nothing reported", want: badgeUnknown},
		{name: "padded status", status: " ok ", want: badgeDone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := richStepBadge(RichStep{Status: tc.status, Success: tc.success}); got != tc.want {
				t.Errorf("richStepBadge(%q, %v) = %+v, want %+v", tc.status, tc.success, got, tc.want)
			}
		})
	}

	// An unrecognised status is a contract change upstream, not a state to guess
	// at: the row prints the raw value so it cannot pass for a success, and does so
	// without a colour tag — that text is upstream's, and a tag is the one thing it
	// could break out of.
	if got := richStepBadge(RichStep{Status: "who-knows"}); got.glyph != "who-knows" || got.markup != "" {
		t.Errorf("richStepBadge(unknown) = %+v, want an uncoloured badge carrying the raw value", got)
	}
}

// TestStepStatusMarkup pins the colour vocabulary of the glyph each state opens a
// row with. It is not the standard_icon vocabulary: the rich-text tag for the
// neutral colour is "neutral", and an unrecognised name inside a tag is rendered as
// literal text rather than ignored, so passing "grey" through would put the name on
// the card.
func TestStepStatusMarkup(t *testing.T) {
	cases := []struct {
		name   string
		status string
		want   string
	}{
		{name: "success is green", status: "ok", want: "<font color='green'>✓</font>"},
		{name: "failure is red", status: "fail", want: "<font color='red'>✗</font>"},
		{name: "running is blue", status: "pending", want: "<font color='blue'>⟳</font>"},
		{name: "nothing reported is neutral", want: "<font color='neutral'>○</font>"},
		{name: "an unknown status carries no tag", status: "who-knows", want: "who-knows"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StepStatusMarkup(RichStep{Status: tc.status}); got != tc.want {
				t.Errorf("StepStatusMarkup(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// TestRichStepRowsCarryNoStatusWord guards the point of the two helpers above: the
// row shows the state as a glyph or an icon, never as the backend's raw vocabulary
// ("ok", "pending"), which is not written for users.
func TestRichStepRowsCarryNoStatusWord(t *testing.T) {
	step := RichStep{
		Kind: RichStepKindTool, Name: "exec", Summary: "ls -la",
		Status: "ok", DurationMs: 234, Tokens: 820,
	}

	got := richStepRowText(step)
	if !strings.Contains(got, badgeDone.glyph) {
		t.Errorf("richStepRowText() = %q, want the status glyph %q", got, badgeDone.glyph)
	}
	for _, unwanted := range []string{"状态", "ok ", "状态：ok"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("richStepRowText() = %q, must not carry the status word %q", got, unwanted)
		}
	}
}

// TestCompactRichStepKeepsMetadata guards the card-size ladder: compaction
// rewrites the two long text fields, and a step that lost its duration and
// tokens on the way down would silently degrade the whole panel on exactly the
// runs that have the most tools.
func TestCompactRichStepKeepsMetadata(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	step := RichStep{
		Kind: RichStepKindTool, ID: "call_a", Name: "exec",
		Summary: long, Result: long, Status: "ok", DurationMs: 234, Tokens: 820,
	}

	got := compactRichStepText(step, 10)
	if got.DurationMs != 234 || got.Tokens != 820 || got.ID != "call_a" {
		t.Errorf("compaction dropped metadata: %+v", got)
	}
	if len([]rune(got.Summary)) > 13 || len([]rune(got.Result)) > 13 {
		t.Errorf("compaction did not shorten the text: %+v", got)
	}
}
