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

package kbtool

import (
	"context"
	"strings"
	"testing"
)

// stubGrader returns a canned verdict, so the tool's handling of a verdict can
// be tested without a model.
type stubGrader struct {
	docs     []Document
	coverage Coverage
	calls    int
}

func (g *stubGrader) Grade(_ context.Context, _ string, _ []Document) ([]Document, Coverage) {
	g.calls++
	return g.docs, g.coverage
}

func TestParseGrades(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		n       int
		want    map[int]int
		wantErr bool
	}{
		{
			name: "a fenced reply is unwrapped",
			raw:  "```json\n{\"scores\":[{\"index\":0,\"score\":9},{\"index\":1,\"score\":0}]}\n```",
			n:    2,
			want: map[int]int{0: 9, 1: 0},
		},
		{
			name: "an index the model invented is dropped",
			raw:  `{"scores":[{"index":0,"score":5},{"index":7,"score":9}]}`,
			n:    2,
			want: map[int]int{0: 5},
		},
		{
			// One passage missing a score is not a reason to discard the
			// others: they are the model's own numbering, and the caller keeps
			// the unscored passage rather than losing the ranking.
			name: "a missing index leaves the rest usable",
			raw:  `{"scores":[{"index":1,"score":4}]}`,
			n:    3,
			want: map[int]int{1: 4},
		},
		{
			name:    "a reply naming nothing is an error",
			raw:     `{"scores":[]}`,
			n:       2,
			wantErr: true,
		},
		{
			name:    "a reply whose every index is invented is an error",
			raw:     `{"scores":[{"index":9,"score":1}]}`,
			n:       2,
			wantErr: true,
		},
		{
			name:    "prose is an error",
			raw:     "资料 0 很相关",
			n:       2,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGrades(tt.raw, tt.n)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseGrades(%q) = %v, want an error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGrades(%q): %v", tt.raw, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseGrades(%q) = %v, want %v", tt.raw, got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("score[%d] = %d, want %d", k, got[k], v)
				}
			}
		})
	}
}

func TestReorderByGrade(t *testing.T) {
	docs := []Document{
		{Content: "a", Score: 0.9},
		{Content: "b", Score: 0.8},
		{Content: "c", Score: 0.7},
		{Content: "d", Score: 0.6},
	}
	// c answers, a and b are on topic but weaker, d was never scored.
	got := reorderByGrade(docs, map[int]int{0: 4, 1: 4, 2: 9})

	if len(got) != len(docs) {
		t.Fatalf("reorder changed the number of passages: %d, want %d", len(got), len(docs))
	}
	if got[0].Content != "c" {
		t.Errorf("first passage = %q, want the one the grader rated 9", got[0].Content)
	}
	// The tie keeps the platforms' order, so the same inputs always produce the
	// same context.
	if got[1].Content != "a" || got[2].Content != "b" {
		t.Errorf("tie order = %q,%q, want the platforms' a,b", got[1].Content, got[2].Content)
	}
	// Unscored last: it cannot outrank a passage the grader read, and it cannot
	// be dropped either, because the grader never said it was irrelevant.
	if got[3].Content != "d" {
		t.Errorf("last passage = %q, want the unscored one", got[3].Content)
	}
}

func TestCoverageOf(t *testing.T) {
	docs := []Document{{Content: "x"}}
	tests := []struct {
		name   string
		docs   []Document
		scores map[int]int
		want   Coverage
	}{
		{name: "nothing retrieved is none", docs: nil, want: CoverageNone},
		{name: "no scores falls back to partial", docs: docs, want: CoveragePartial},
		{name: "an answering passage is sufficient", docs: docs, scores: map[int]int{0: gradeSufficient}, want: CoverageSufficient},
		{name: "an on-topic passage is partial", docs: docs, scores: map[int]int{0: gradeSufficient - 1}, want: CoveragePartial},
		{name: "every passage irrelevant is none", docs: docs, scores: map[int]int{0: 0}, want: CoverageNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := coverageOf(tt.docs, tt.scores); got != tt.want {
				t.Errorf("coverageOf = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCoverageNote_NoVerdictIsSilent pins the difference between "no grader ran"
// and "the grader found nothing".
//
// They are different claims — the first is the absence of a judgement, the
// second is a judgement about the material — and only the second is worth
// telling the model. Collapsing them would make turning grading off change the
// answer, which is the one thing an off-by-default feature must not do.
func TestCoverageNote_NoVerdictIsSilent(t *testing.T) {
	if got := coverageNote(""); got != "" {
		t.Errorf("no verdict produced %q, want silence", got)
	}
	if got := coverageNote(CoverageSufficient); got != "" {
		t.Errorf("sufficient produced %q, want silence", got)
	}
	if got := coverageNote(CoverageNone); !strings.Contains(got, "No relevant information") {
		t.Errorf("none produced %q, want the wording grading replaced nothing of", got)
	}
	if got := coverageNote(CoveragePartial); !strings.Contains(got, "do not fully answer") {
		t.Errorf("partial produced %q, want the coverage caveat", got)
	}
}

// TestInvokableRun_WithoutAGraderRendersExactlyWhatItUsedTo is the guard on the
// feature being opt-in.
func TestInvokableRun_WithoutAGraderRendersExactlyWhatItUsedTo(t *testing.T) {
	docs := []Document{{Source: "a", Content: "A", Score: 0.5}}
	tool := New(Config{
		Retrievers: []Retriever{&fakeRetriever{name: "kb", docs: docs}},
	}).(*retrievalTool)

	got, err := tool.InvokableRun(context.Background(), `{"query":"q"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if strings.Contains(got, "Note:") {
		t.Errorf("an ungraded run produced a coverage note: %q", got)
	}
	if !strings.Contains(got, "A") {
		t.Errorf("an ungraded run did not render the passage: %q", got)
	}
}

// TestInvokableRun_GradedCoverageNoneWithholdsThePassages pins the one case
// where the tool answers with no material: the grader read the passages and
// rated none of them on topic.
//
// Showing them anyway is the failure this prevents — material rated irrelevant
// is what a model reaches for when it has nothing better, and an answer built
// from it looks exactly like an answer built from the corpus.
func TestInvokableRun_GradedCoverageNoneWithholdsThePassages(t *testing.T) {
	docs := []Document{{Source: "a", Content: "UNRELATED-BODY", Score: 0.5}}
	grader := &stubGrader{docs: docs, coverage: CoverageNone}
	tool := New(Config{
		Retrievers: []Retriever{&fakeRetriever{name: "kb", docs: docs}},
		Grader:     grader,
	}).(*retrievalTool)

	got, err := tool.InvokableRun(context.Background(), `{"query":"q"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if grader.calls != 1 {
		t.Errorf("grader called %d times, want once per retrieval", grader.calls)
	}
	if strings.Contains(got, "UNRELATED-BODY") {
		t.Errorf("passages the grader rated irrelevant were still shown: %q", got)
	}
	if !strings.Contains(got, "No relevant information") {
		t.Errorf("withheld passages did not say why: %q", got)
	}
}

// TestNewGrader_RequiresAModel pins that grading is off unless a model is named,
// which is what makes the whole feature opt-in.
func TestNewGrader_RequiresAModel(t *testing.T) {
	if g := NewGrader(GraderConfig{}); g != nil {
		t.Error("a grader with no model must be nil, not a grader that cannot run")
	}
}
