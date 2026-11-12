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

func joinChunks(chunks []string) string { return strings.Join(chunks, "") }

// boundariesInside reports whether any chunk boundary falls strictly inside
// the half-open range [start, end). A boundary landing there is one structure
// cut in half; a boundary exactly at start or end is the good case.
func boundariesInside(chunks []string, start, end int) bool {
	off := 0
	for _, c := range chunks[:max(len(chunks)-1, 0)] {
		off += len(c)
		if off > start && off < end {
			return true
		}
	}
	return false
}

func TestSplitByBudget(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		budget int
		want   int
	}{
		{name: "short input stays whole", in: "hello", budget: 100, want: 1},
		{name: "exactly at budget stays whole", in: strings.Repeat("a", 100), budget: 100, want: 1},
		{name: "one byte over splits", in: strings.Repeat("a\n", 51), budget: 100, want: 2},
		{name: "zero budget disables splitting", in: strings.Repeat("a", 500), budget: 0, want: 1},
		{name: "negative budget disables splitting", in: strings.Repeat("a", 500), budget: -1, want: 1},
		{name: "empty input is one empty chunk", in: "", budget: 100, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitByBudget(tt.in, tt.budget)
			if len(got) != tt.want {
				t.Fatalf("got %d chunks, want %d", len(got), tt.want)
			}
			if joinChunks(got) != tt.in {
				t.Errorf("content changed: got %q, want %q", joinChunks(got), tt.in)
			}
		})
	}
}

// TestSplitByBudgetRespectsBudgetOnProse pins the ordinary case: with no
// oversized atomic block in the way, every chunk is within budget. Boundaries
// land on newlines, so a chunk never ends mid-word.
func TestSplitByBudgetRespectsBudgetOnProse(t *testing.T) {
	md := strings.Repeat("the quick brown fox jumps over the lazy dog\n", 200)
	chunks := SplitByBudget(md, 512)

	if len(chunks) < 2 {
		t.Fatalf("got %d chunks for %d bytes at budget 512, want several", len(chunks), len(md))
	}
	for i, c := range chunks {
		if len(c) > 512 {
			t.Errorf("chunk %d is %d bytes, over the 512 budget", i, len(c))
		}
		if i < len(chunks)-1 && !strings.HasSuffix(c, "\n") {
			t.Errorf("chunk %d does not end on a line boundary: %q", i, c[max(0, len(c)-20):])
		}
	}
}

// TestSplitByBudgetKeepsCodeFence is the case that motivates the atomicity: a
// fence cut in half turns the remainder into prose.
func TestSplitByBudgetKeepsCodeFence(t *testing.T) {
	code := strings.Repeat("fmt.Println(\"hello world\")\n", 60)
	md := "intro\n\n```go\n" + code + "```\n\noutro\n"
	chunks := SplitByBudget(md, 256)

	if joinChunks(chunks) != md {
		t.Fatalf("content changed")
	}
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want the document split", len(chunks))
	}

	start := strings.Index(md, "```go")
	end := start + len("```go\n") + len(code) + len("```\n")
	if boundariesInside(chunks, start, end) {
		t.Errorf("a chunk boundary falls inside the code fence (offsets %d..%d)", start, end)
	}

	// An odd fence count in any chunk is the same defect seen from the other
	// side: an opener without its closer.
	for i, c := range chunks {
		if n := strings.Count(c, "```"); n%2 != 0 {
			t.Errorf("chunk %d has %d fence markers, so a fence was split:\n%s", i, n, c)
		}
	}
}

// TestSplitByBudgetKeepsTable is the other atomic case: a table cut in half
// renders as a wall of pipes.
func TestSplitByBudgetKeepsTable(t *testing.T) {
	table := "| a | b | c |\n| - | - | - |\n" +
		strings.Repeat("| column one | column two | column three |\n", 40)
	md := "intro\n\n" + table + "\noutro\n"
	chunks := SplitByBudget(md, 256)

	if joinChunks(chunks) != md {
		t.Fatalf("content changed")
	}
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want the document split", len(chunks))
	}

	start := strings.Index(md, table)
	if boundariesInside(chunks, start, start+len(table)) {
		t.Errorf("a chunk boundary falls inside the table (offsets %d..%d)", start, start+len(table))
	}

	whole := 0
	for _, c := range chunks {
		if strings.Contains(c, table) {
			whole++
		}
	}
	if whole != 1 {
		t.Errorf("table appears whole in %d chunks, want exactly 1", whole)
	}
}

// TestSplitByBudgetNoLoss is the invariant everything else rests on: joining
// the pieces reproduces the input byte for byte, across doc shapes and budgets
// including degenerate ones.
func TestSplitByBudgetNoLoss(t *testing.T) {
	inputs := map[string]string{
		"empty":        "",
		"one line":     "one line",
		"crlf":         "a\r\nb\r\nc\r\n",
		"prose":        strings.Repeat("plain text line\n", 400),
		"fence":        "before\n```go\n" + strings.Repeat("x := 1\n", 500) + "```\nafter\n",
		"tilde fence":  "before\n~~~\n" + strings.Repeat("y := 2\n", 300) + "~~~\nafter\n",
		"open fence":   "unterminated\n```\n" + strings.Repeat("still inside\n", 300),
		"table":        "| a | b |\n| - | - |\n" + strings.Repeat("| 1 | 2 |\n", 300),
		"one long run": strings.Repeat("a", 30000),
		"mixed": "mixed\n\n```\ncode\n```\n\n" + strings.Repeat("prose\n", 200) +
			"\n| x |\n| - |\n| 1 |\n",
		"fence then prose": "```\nfenced\n```\n" + strings.Repeat("body\n", 250),
	}
	for name, in := range inputs {
		for _, budget := range []int{1, 7, 50, 512, TextChunkBytes} {
			chunks := SplitByBudget(in, budget)
			if got := joinChunks(chunks); got != in {
				t.Errorf("%s at budget %d: content changed (%d bytes in, %d out)",
					name, budget, len(in), len(got))
			}
			if len(in) > 0 && len(chunks) == 0 {
				t.Errorf("%s at budget %d: dropped all content", name, budget)
			}
		}
	}
}

// TestSplitByBudgetOversizedFenceEmittedWhole documents the deliberate limit:
// a single atomic block bigger than the budget cannot be split without
// corrupting it, so it is sent over budget rather than broken.
func TestSplitByBudgetOversizedFenceEmittedWhole(t *testing.T) {
	code := strings.Repeat("x\n", 500)
	md := "```\n" + code + "```\n"
	chunks := SplitByBudget(md, 64)

	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want the oversized fence emitted as one", len(chunks))
	}
	if chunks[0] != md {
		t.Errorf("the oversized fence was altered:\ngot  %q\nwant %q", chunks[0], md)
	}
}
