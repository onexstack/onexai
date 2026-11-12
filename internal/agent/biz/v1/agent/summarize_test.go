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
	"context"
	"strings"
	"testing"

	adksummarization "github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/pkg/tokenizer"
	"github.com/onexstack/onexai/pkg/media"
)

// TestSummarizationLimits pins the split between the two summarization layers.
//
// An end-to-end run against a live model showed why this matters: with the
// in-run trigger set to the budget itself, the cross-turn compactor reduced
// history to just under the budget, the run added this turn's question and the
// tool schemas, and the very first model call crossed the trigger. The in-run
// layer then summarized on every turn, redoing the compactor's work, and it
// trimmed the current question to fit its derived allowance — head-first, which
// cut off an instruction sitting at the end of a long pasted question.
func TestSummarizationLimits(t *testing.T) {
	const budget = 500000
	trigger, preserved := summarizationLimits(budget)

	// The trigger must clear what the compactor leaves, or it fires on the
	// first iteration of every over-budget turn.
	if trigger <= budget {
		t.Fatalf("in-run trigger %d does not clear the cross-turn budget %d", trigger, budget)
	}

	// But it must still leave room under the model window for the rescue call,
	// which this middleware makes in one unchunked piece. A 1M-token window is
	// the model the original failure was reported against.
	const window = 1048576
	if trigger >= window {
		t.Fatalf("in-run trigger %d leaves no headroom under a %d-token window", trigger, window)
	}

	// The allowance is ours, not the library's trigger/3 default, and must be
	// large enough that no realistic question gets trimmed.
	if libraryDefault := trigger / 3; preserved <= libraryDefault {
		t.Fatalf("preserved allowance %d does not exceed the library default %d", preserved, libraryDefault)
	}
	if preserved >= trigger {
		t.Fatalf("preserved allowance %d leaves no room for the summary under a %d trigger", preserved, trigger)
	}
}

// TestDescribeImages pins what a summary is told about the pictures in a turn.
//
// It is deliberately a description and not the pictures: inlining one would put
// a base64 body into the summarizer's own prompt, which is the cost the whole
// reference design exists to avoid — and the summarizer is the one caller that
// would then be billed to read it.
func TestDescribeImages(t *testing.T) {
	tests := []struct {
		name string
		refs []media.Ref
		want string
	}{
		{
			name: "one image with its shape",
			refs: []media.Ref{{MimeType: "image/png", Width: 800, Height: 600}},
			want: "1 张图片（image/png 800x600）",
		},
		{
			name: "several of the same shape",
			refs: []media.Ref{
				{MimeType: "image/png", Width: 10, Height: 10},
				{MimeType: "image/png", Width: 10, Height: 10},
			},
			want: "2 张图片（image/png 10x10）",
		},
		{
			// Sorted, so the same history renders the same way twice.
			name: "mixed shapes",
			refs: []media.Ref{
				{MimeType: "image/jpeg", Width: 1, Height: 1},
				{MimeType: "image/png", Width: 2, Height: 2},
			},
			want: "2 张图片（image/jpeg 1x1、image/png 2x2）",
		},
		{
			name: "a reference with nothing recorded",
			refs: []media.Ref{{}},
			want: "1 张图片（图片）",
		},
		{
			// A type without dimensions still says more than a bare count.
			name: "type without dimensions",
			refs: []media.Ref{{MimeType: "image/webp"}},
			want: "1 张图片（image/webp）",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describeImages(tt.refs); got != tt.want {
				t.Errorf("describeImages() = %q, want %q", got, tt.want)
			}
		})
	}

	// Above all: nothing that looks like bytes reaches the text.
	long := []media.Ref{{URL: "data:image/png;base64," + strings.Repeat("A", 4096), MimeType: "image/png"}}
	if strings.Contains(describeImages(long), "AAAA") {
		t.Error("the description carries the image's bytes")
	}
}

// TestRenderPreservedUserMessages pins the block's selection rule.
//
// Budgets here are chosen far away from any message's real token count, so the
// test pins the rule rather than a tokenizer's arithmetic; only the "one
// message fits" case needs a count, and it asks the tokenizer for it.
func TestRenderPreservedUserMessages(t *testing.T) {
	msgs := []*schema.Message{
		schema.SystemMessage("系统提示"),
		schema.UserMessage("第一个问题"),
		schema.AssistantMessage("回答", nil),
		schema.UserMessage("第二个问题"),
		schema.UserMessage("第三个问题"),
	}

	t.Run("everything fits, and non-user messages are left out", func(t *testing.T) {
		got, err := renderPreservedUserMessages(msgs, 1_000_000)
		if err != nil {
			t.Fatalf("renderPreservedUserMessages() error = %v", err)
		}
		want := "<all_user_messages>\n" +
			"    - 第一个问题\n" +
			"    - 第二个问题\n" +
			"    - 第三个问题\n" +
			"</all_user_messages>"
		if got != want {
			t.Errorf("renderPreservedUserMessages() = %q, want %q", got, want)
		}
	})

	t.Run("a message that overruns is dropped whole, not trimmed", func(t *testing.T) {
		newest, err := tokenizer.CountSchemaMessages([]*schema.Message{msgs[len(msgs)-1]}, nil)
		if err != nil {
			t.Fatalf("counting the newest message: %v", err)
		}

		// Exactly the newest message's worth of room: the one before it cannot
		// fit, and is dropped rather than cut down to nothing.
		got, err := renderPreservedUserMessages(msgs, newest)
		if err != nil {
			t.Fatalf("renderPreservedUserMessages() error = %v", err)
		}
		want := "<all_user_messages>\n" +
			userMessagesOmittedNote + "\n" +
			"    - 第三个问题\n" +
			"</all_user_messages>"
		if got != want {
			t.Errorf("renderPreservedUserMessages() = %q, want %q", got, want)
		}
	})

	t.Run("nothing fits", func(t *testing.T) {
		got, err := renderPreservedUserMessages(msgs, 0)
		if err != nil {
			t.Fatalf("renderPreservedUserMessages() error = %v", err)
		}
		// Empty, so the caller leaves the library's own block standing rather
		// than emptying the section the summarizer was asked to fill.
		if got != "" {
			t.Errorf("renderPreservedUserMessages() = %q, want it empty", got)
		}
	})

	t.Run("no user messages at all", func(t *testing.T) {
		got, err := renderPreservedUserMessages([]*schema.Message{schema.SystemMessage("系统提示")}, 1000)
		if err != nil {
			t.Fatalf("renderPreservedUserMessages() error = %v", err)
		}
		if got != "" {
			t.Errorf("renderPreservedUserMessages() = %q, want it empty", got)
		}
	})
}

// TestUserMessageFinalizeKeepsALongQuestionWhole is the behaviour the local
// finalize exists for.
//
// eino v0.9.21 sizes the preserved block from a fixed 30k-token estimate, where
// this deployment's 500k budget asks for 0.75 of it, and it estimates a message
// at len(bytes)/4. A question past that estimate is truncated to its first and
// last 1000 runes, so what a long paste loses is its middle — usually the
// detail being asked about.
//
// The history needs a second user message for that to show: the library takes a
// lone user message whole, whatever its size, and only runs the budget loop when
// there is one to choose between.
func TestUserMessageFinalizeKeepsALongQuestionWhole(t *testing.T) {
	const (
		middle = "【这里是提问的关键细节】"
		tail   = "以上都做完后，把结果发到运维群里。"
	)
	// 12000 x 15 / 4 = 45k estimated tokens: past the library's 30k allowance,
	// and well inside the 375k this deployment reserves. Deliberately not near
	// the boundary — at exactly 30k the library keeps the message whole and
	// there is nothing left to demonstrate.
	question := strings.Repeat("补充说明。", 6000) + middle + strings.Repeat("补充说明。", 6000) + tail

	original := []*schema.Message{
		schema.SystemMessage(summarizerSystemPrompt),
		schema.UserMessage("先看下这个日志。"),
		schema.AssistantMessage("好的。", nil),
		schema.UserMessage(question),
	}
	summary := schema.AssistantMessage(
		"<summary>历史摘要</summary>\n<all_user_messages>\n    - 占位\n</all_user_messages>", nil)
	ctx := context.Background()

	// First establish that the allowance is load-bearing: the library's own
	// finalize cuts the middle of the question out.
	libraryOut, err := adksummarization.DefaultFinalize(ctx, original, summary)
	if err != nil {
		t.Fatalf("DefaultFinalize() error = %v", err)
	}
	if strings.Contains(libraryOut[len(libraryOut)-1].Content, middle) {
		t.Fatal("the library kept the middle; this test no longer exercises the truncation it guards")
	}

	_, preserved := summarizationLimits(500000)
	out, err := newUserMessageFinalize(preserved)(ctx, original, summary)
	if err != nil {
		t.Fatalf("newUserMessageFinalize() error = %v", err)
	}
	if got := out[len(out)-1].Content; !strings.Contains(got, middle) {
		t.Errorf("the middle of the question was dropped; block ends with:\n%s", tailOf(got, 200))
	}
}

// tailOf returns the last n bytes of s, for failure messages that would
// otherwise print an 8k-repeat question.
func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
