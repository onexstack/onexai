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

package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/pkg/modeljson"
)

// ── chunking ────────────────────────────────────────────────────────────────

func TestSplitIntoChunks(t *testing.T) {
	t.Run("paragraphs are kept whole while they fit", func(t *testing.T) {
		text := "first paragraph\n\nsecond paragraph\n\nthird paragraph"
		chunks := splitIntoChunks(text, 40)
		if len(chunks) == 0 {
			t.Fatal("no chunks")
		}
		for _, c := range chunks {
			if len(c) > 40 {
				t.Errorf("chunk of %d chars exceeds the 40-char target: %q", len(c), c)
			}
		}
		// All the text must survive chunking.
		joined := strings.Join(chunks, " ")
		for _, want := range []string{"first", "second", "third"} {
			if !strings.Contains(joined, want) {
				t.Errorf("chunking lost %q", want)
			}
		}
	})

	t.Run("a paragraph larger than the limit is split by line", func(t *testing.T) {
		para := strings.Repeat("a line of text\n", 20)
		chunks := splitIntoChunks(para, 60)
		for _, c := range chunks {
			if len(c) > 60 {
				t.Errorf("chunk of %d chars exceeds the limit", len(c))
			}
		}
	})

	t.Run("a single line larger than the limit is split by character", func(t *testing.T) {
		chunks := splitIntoChunks(strings.Repeat("x", 250), 100)
		for _, c := range chunks {
			if len(c) > 100 {
				t.Errorf("chunk of %d chars exceeds the 100-char limit", len(c))
			}
		}
		if len(chunks) != 3 {
			t.Errorf("got %d chunks for 250 chars at 100, want 3", len(chunks))
		}
	})

	t.Run("empty input yields nothing", func(t *testing.T) {
		if chunks := splitIntoChunks("   \n\n  ", 100); len(chunks) != 0 {
			t.Errorf("got %d chunks from blank text", len(chunks))
		}
	})
}

func TestBestN(t *testing.T) {
	scored := []chunk{
		{text: "c", score: 0},
		{text: "a", score: 9},
		{text: "b", score: 3},
		{text: "d", score: 0},
	}
	got := bestN(scored, 2)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("bestN = %v, want [a b]", got)
	}

	// Everything scoring zero leaves nothing, which the caller handles by
	// falling back rather than by refusing to answer.
	if got := bestN(scored, 2); len(got) == 0 {
		t.Fatal("expected the two non-zero chunks")
	}
	if got := bestN([]chunk{{text: "x"}}, 2); len(got) != 0 {
		t.Errorf("all-zero input = %v, want nothing", got)
	}
}

func TestBestN_PrefersTheExcerptOverTheWholeChunk(t *testing.T) {
	got := bestN([]chunk{{text: "the whole chunk", score: 5, excerpt: "the relevant bit"}}, 1)
	if len(got) != 1 || got[0] != "the relevant bit" {
		t.Errorf("bestN = %v, want the excerpt", got)
	}
}

// ── fake model ──────────────────────────────────────────────────────────────

// fakeModel answers ranking prompts with a canned score and synthesis prompts
// with the excerpts it was given, so the pipeline can be exercised without a
// network.
type fakeModel struct {
	calls    atomic.Int64
	scoreFor func(text string) int
}

func (f *fakeModel) Generate(_ context.Context, msgs []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	f.calls.Add(1)
	prompt := msgs[0].Content

	// The two prompts are told apart by their opening line rather than by any
	// word inside them: the synthesis prompt quotes the ranking prompt's output,
	// so a keyword test would mistake one for the other.
	if !strings.HasPrefix(prompt, "判断下面这段文本") {
		return &schema.Message{Role: schema.Assistant, Content: "synthesized: " + prompt}, nil
	}

	score := 0
	if f.scoreFor != nil {
		score = f.scoreFor(prompt)
	}
	// The chunk is whatever sits between the "文本：" (Text:) marker and the instruction
	// that follows it, which is how the real model would see it too.
	excerpt := ""
	if i := strings.Index(prompt, "文本：\n"); i >= 0 {
		rest := prompt[i+len("文本：\n"):]
		if j := strings.Index(rest, "\n\n严格返回"); j >= 0 {
			excerpt = strings.TrimSpace(rest[:j])
		}
	}
	body, _ := json.Marshal(map[string]any{"score": score, "excerpt": excerpt})
	return &schema.Message{Role: schema.Assistant, Content: string(body)}, nil
}

func (f *fakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, fmt.Errorf("not used")
}

// ── tool ────────────────────────────────────────────────────────────────────

func writeDoc(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	return path
}

func TestNewTool_NoModelIsNil(t *testing.T) {
	if tool := NewTool(Config{}); tool != nil {
		t.Error("a tool with no model cannot rank or answer, so it must not be offered")
	}
}

func TestTool_AnswersFromTheRelevantChunk(t *testing.T) {
	doc := writeDoc(t, "irrelevant filler\n\n"+strings.Repeat("padding text here\n\n", 40)+
		"the payment service is restarted with kubectl rollout restart")
	// Score only the chunk that mentions the restart command. The keyword must
	// not appear in the question, or every chunk's prompt would match it — the
	// question is part of every ranking prompt.
	mdl := &fakeModel{scoreFor: func(prompt string) int {
		if strings.Contains(prompt, "kubectl rollout restart") {
			return 9
		}
		return 1
	}}

	tl := NewTool(Config{Model: mdl, ChunkSize: 200, TopK: 2}).(*answerFromDocumentTool)

	args, _ := json.Marshal(map[string]string{
		"file_path": doc,
		"question":  "how is the payment service restarted?",
	})
	out, err := tl.InvokableRun(context.Background(), string(args))
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(out, "synthesized") {
		t.Errorf("expected a synthesized answer, got %q", out)
	}
	// The excerpt is the head of the chunk that scored highest, so seeing this
	// sentence in the answer proves that chunk was the one selected.
	if !strings.Contains(out, "the payment service is restarted") {
		t.Errorf("the relevant passage did not reach the answer: %q", out)
	}
	if strings.Contains(out, "[1] irrelevant filler") {
		t.Errorf("the top-ranked passage is the irrelevant one: %q", out)
	}
}

func TestTool_InputValidation(t *testing.T) {
	tl := NewTool(Config{Model: &fakeModel{}}).(*answerFromDocumentTool)
	ctx := context.Background()

	cases := map[string]string{
		"not json":       "{",
		"no file path":   `{"question":"q"}`,
		"no question":    `{"file_path":"/tmp/x"}`,
		"blank path":     `{"file_path":"   ","question":"q"}`,
		"blank question": `{"file_path":"/tmp/x","question":"  "}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := tl.InvokableRun(ctx, args); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestTool_RefusesAMissingFile(t *testing.T) {
	tl := NewTool(Config{Model: &fakeModel{}}).(*answerFromDocumentTool)
	_, err := tl.InvokableRun(context.Background(),
		`{"file_path":"/nonexistent/does-not-exist.md","question":"q"}`)
	if err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Fatalf("expected a readable error, got: %v", err)
	}
}

func TestTool_RefusesADirectory(t *testing.T) {
	tl := NewTool(Config{Model: &fakeModel{}}).(*answerFromDocumentTool)
	_, err := tl.InvokableRun(context.Background(),
		fmt.Sprintf(`{"file_path":%q,"question":"q"}`, t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("expected a directory refusal, got: %v", err)
	}
}

// TestTool_FallsBackWhenNothingScores pins that a question about a document is
// still answered from it when ranking finds nothing relevant — "no answer"
// would be indistinguishable from "unreadable".
func TestTool_FallsBackWhenNothingScores(t *testing.T) {
	doc := writeDoc(t, "alpha content\n\nbeta content")
	mdl := &fakeModel{} // scores everything zero
	tl := NewTool(Config{Model: mdl, ChunkSize: 100, TopK: 2}).(*answerFromDocumentTool)

	out, err := tl.InvokableRun(context.Background(),
		fmt.Sprintf(`{"file_path":%q,"question":"anything"}`, doc))
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(out, "alpha content") {
		t.Errorf("the fallback did not use the document's start: %q", out)
	}
}

func TestTool_Info(t *testing.T) {
	info, err := NewTool(Config{Model: &fakeModel{}}).(*answerFromDocumentTool).Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolName {
		t.Errorf("tool name = %q, want %q", info.Name, ToolName)
	}
	if info.ParamsOneOf == nil {
		t.Fatal("the tool must advertise parameters")
	}
}

func TestStripFence(t *testing.T) {
	cases := map[string]string{
		"```json\n{\"a\":1}\n```": `{"a":1}`,
		"```\n{\"a\":1}\n```":     `{"a":1}`,
		`{"a":1}`:                 `{"a":1}`,
		"  {\"a\":1}  ":           `{"a":1}`,
	}
	for in, want := range cases {
		if got := modeljson.StripFence(in); got != want {
			t.Errorf("StripFence(%q) = %q, want %q", in, got, want)
		}
	}
}
