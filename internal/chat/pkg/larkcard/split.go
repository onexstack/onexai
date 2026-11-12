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

import "strings"

// TextChunkBytes is the per-message budget for outbound plain text.
//
// Feishu rejects a text message past an upper bound, and a rejected send used
// to fail the whole reply — which is how a long answer disappeared without a
// trace. This sits well under the platform limit so the JSON envelope and any
// escaping fit alongside the content. It is one constant on purpose: the
// per-tenant ceiling is only knowable from a live tenant, so this is the knob
// to turn when one says so.
const TextChunkBytes = 12000

// SplitByBudget splits markdown into consecutive pieces of at most budget
// bytes each, for sending as separate messages.
//
// Two structures have to survive whole. A fenced code block cut in half loses
// its fence and the remainder renders as prose; a table cut in half renders as
// a wall of pipes. Both are worse outcomes than a shorter message, so fences
// and tables are atomic here: no piece boundary ever falls inside one. A
// single such block larger than the budget is emitted alone and over budget,
// because corrupting it to hit a number is not an improvement.
//
// Concatenating the result reproduces the input exactly — nothing is trimmed
// and no newline is dropped — so a caller that sends every piece in order has
// sent everything, and a caller that fails partway knows precisely what was
// not sent.
func SplitByBudget(md string, budget int) []string {
	if budget <= 0 || len(md) <= budget {
		return []string{md}
	}

	blocks := mdBlocks(md, budget)
	chunks := make([]string, 0, len(md)/budget+1)
	start := 0
	for i := 0; i < len(blocks); {
		end := blocks[i].end
		i++
		// Extend the piece block by block while the next one still fits. A
		// block that does not fit starts the next piece — it is never skipped,
		// so no input is lost even when a block is over budget on its own.
		for ; i < len(blocks); i++ {
			if blocks[i].end-start > budget {
				break
			}
			end = blocks[i].end
		}
		chunks = append(chunks, md[start:end])
		start = end
	}
	return chunks
}

// mdBlock is a byte range of the document that packing may not cut through.
type mdBlock struct {
	start, end int
}

// mdBlocks segments md into blocks, each starting and ending on a line
// boundary. Fences and tables are one block each; everything else is a run of
// ordinary lines, pre-split so that no non-atomic block exceeds budget.
func mdBlocks(md string, budget int) []mdBlock {
	lines := lineSpans(md)
	blocks := make([]mdBlock, 0, len(lines))
	runStart := -1

	// flushRun emits the accumulated run of ordinary lines, split to budget.
	flushRun := func(end int) {
		if runStart < 0 {
			return
		}
		blocks = append(blocks, splitRun(md, runStart, end, budget)...)
		runStart = -1
	}

	for i := 0; i < len(lines); {
		if a := findAtomicBlock(md, lines, i); a.ok {
			flushRun(a.start)
			blocks = append(blocks, mdBlock{a.start, a.end})
			i = a.next
			continue
		}
		if runStart < 0 {
			runStart = lines[i][0]
		}
		i++
	}
	flushRun(len(md))

	return blocks
}

// atomicBlock is a run of lines the splitter may not cut through, as a byte
// range plus the line index that follows it.
type atomicBlock struct {
	start, end int
	next       int
	ok         bool
}

// findAtomicBlock reports the atomic block beginning at line i, if there is
// one: a fenced code block or a table.
func findAtomicBlock(md string, lines [][2]int, i int) atomicBlock {
	if marker, ok := fenceOpen(md, lines[i]); ok {
		return fencedBlock(md, lines, i, marker)
	}
	if isTableRow(md, lines[i]) {
		return tableBlock(md, lines, i)
	}
	return atomicBlock{}
}

// fencedBlock spans a code fence from its opening line to its closing marker, or
// to the end of the document when the fence is never closed. Keeping an
// unterminated fence whole is the honest reading of a document that never
// closed it; guessing where it should have closed would move the corruption
// rather than avoid it.
func fencedBlock(md string, lines [][2]int, i int, marker string) atomicBlock {
	for j := i + 1; j < len(lines); j++ {
		if fenceClose(md, lines[j], marker) {
			return atomicBlock{start: lines[i][0], end: lines[j][1], next: j + 1, ok: true}
		}
	}
	return atomicBlock{start: lines[i][0], end: len(md), next: len(lines), ok: true}
}

// tableBlock spans a run of consecutive table rows.
func tableBlock(md string, lines [][2]int, i int) atomicBlock {
	end := lines[i][1]
	j := i + 1
	for ; j < len(lines) && isTableRow(md, lines[j]); j++ {
		end = lines[j][1]
	}
	return atomicBlock{start: lines[i][0], end: end, next: j, ok: true}
}

// splitRun cuts a run of ordinary lines into blocks of at most budget bytes.
func splitRun(md string, start, end, budget int) []mdBlock {
	var out []mdBlock
	for start < end {
		if end-start <= budget {
			out = append(out, mdBlock{start, end})
			break
		}
		cut := cutPoint(md, start, end, budget)
		out = append(out, mdBlock{start, cut})
		start = cut
	}
	return out
}

// cutPoint returns the latest offset at or before start+budget that still falls
// on a line boundary. When one line is longer than the whole budget there is no
// such boundary, so the hard limit is used and the line is cut mid-way — the
// only case where a piece boundary lands inside a line.
func cutPoint(md string, start, end, budget int) int {
	limit := min(start+budget, end)
	if nl := strings.LastIndexByte(md[start:limit], '\n'); nl >= 0 {
		if cut := start + nl + 1; cut > start {
			return cut
		}
	}
	return limit
}

// lineSpans returns the byte range of every line, newline included.
func lineSpans(md string) [][2]int {
	var out [][2]int
	for i := 0; i < len(md); {
		nl := strings.IndexByte(md[i:], '\n')
		if nl < 0 {
			out = append(out, [2]int{i, len(md)})
			break
		}
		out = append(out, [2]int{i, i + nl + 1})
		i += nl + 1
	}
	return out
}

// fenceMarkers are the fence characters CommonMark accepts.
var fenceMarkers = []string{"```", "~~~"}

// fenceOpen reports whether the line opens a fenced code block and returns the
// marker it opened with, so the matching close can be recognized rather than
// guessed at from a bare "```" appearing inside the block.
func fenceOpen(md string, line [2]int) (string, bool) {
	s := trimFenceLine(md[line[0]:line[1]])
	for _, m := range fenceMarkers {
		if strings.HasPrefix(s, m) {
			return m, true
		}
	}
	return "", false
}

// fenceClose reports whether the line closes a block opened with marker. An
// opening fence may carry an info string ("```go"); a closing one may not.
func fenceClose(md string, line [2]int, marker string) bool {
	s := trimFenceLine(md[line[0]:line[1]])
	return strings.HasPrefix(s, marker) && strings.TrimSpace(s[len(marker):]) == ""
}

func trimFenceLine(s string) string {
	return strings.TrimLeft(strings.TrimRight(s, "\r\n"), " \t")
}

// isTableRow reports whether the line is a table row. Requiring a closing pipe
// keeps a line that merely starts with "|" — which is ordinary prose in a
// surprising number of documents — out of the atomic set.
func isTableRow(md string, line [2]int) bool {
	s := strings.TrimSpace(md[line[0]:line[1]])
	return len(s) > 1 && strings.HasPrefix(s, "|") && strings.HasSuffix(s, "|")
}
