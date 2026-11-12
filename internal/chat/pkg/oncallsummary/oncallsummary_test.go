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

package oncallsummary

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	chatknown "github.com/onexstack/onexai/pkg/known/chat"
)

// TestPromptAsksForTheFiveSections pins the shape the answer must take.
//
// It is a request, not a guarantee — a model may ignore it — but a prompt that
// stopped asking for one of the headings would produce summaries whose readers
// cannot find the fix, and nothing downstream would notice.
func TestPromptAsksForTheFiveSections(t *testing.T) {
	got := Material{Title: "网关 502", Notes: "重启后恢复"}.Prompt()

	for _, want := range []string{
		sectionSymptom, sectionAttempts, sectionCause, sectionFix, sectionPrevent,
	} {
		assert.Contains(t, got, want, "the prompt must ask for %s", want)
	}
	assert.Contains(t, got, UnknownMarker, "and must say what to write where the material is silent")
	assert.Contains(t, got, "IP 地址", "and must forbid reproducing the things that make an answer unsafe to publish")
	// The order the headings carry matters as much as their presence: a reader
	// learns where the fix is, and an answer that reordered them would move it.
	assert.Less(t, strings.Index(got, sectionSymptom), strings.Index(got, sectionFix),
		"the symptom is asked for before the fix")
}

// TestPromptAsksForTheBriefMarkerInTheSameWordsTheQueryExcludes is the one
// cross-package agreement this feature has, and it is worth a test of its own.
//
// The prompt asks the model to answer with one sentence when a ticket is not worth
// a write-up, and the ticket query excludes rows whose summary *is* that sentence.
// Two spellings would mean the model declining a ticket in wording the query does
// not recognise — which publishes exactly the ticket the model said to skip, and
// publishes it quietly.
func TestPromptAsksForTheBriefMarkerInTheSameWordsTheQueryExcludes(t *testing.T) {
	got := Material{Title: "你好"}.Prompt()

	assert.Contains(t, got, chatknown.SummaryBrief,
		"the prompt must ask for the marker by the name the query knows it by")
	assert.True(t, chatknown.IsBriefSummary(chatknown.SummaryBrief),
		"and the marker the prompt asks for must be one the query recognises")
}

// TestPromptLabelsEveryPartIncludingTheEmptyOnes pins that absence is stated.
//
// An omitted part is invisible to the model: it reads a prompt that simply has no
// handling log and has no way to tell that one existed. Saying 「（空）」 (empty) is a fact
// it can work from, and it is what keeps the 「（群聊未明确）」 (the group chat does not say) answers honest.
func TestPromptLabelsEveryPartIncludingTheEmptyOnes(t *testing.T) {
	got := Material{Title: "网关 502"}.Prompt()

	assert.Contains(t, got, "工单标题")
	assert.Contains(t, got, "处理记录")
	assert.Contains(t, got, "工单群聊天记录")
	assert.Contains(t, got, "（空）", "a part that is absent has to say so rather than be missing")
	// The title is present, so the prompt must not claim everything is empty.
	assert.Equal(t, 3, strings.Count(got, "（空）"), "one line per absent part, and only for those")
}

// TestPromptCarriesWhatTheCallerCouldNotGet pins the missing-material line.
func TestPromptCarriesWhatTheCallerCouldNotGet(t *testing.T) {
	got := Material{
		Title:   "网关 502",
		Missing: []string{"工单群的聊天记录", "较早的聊天记录（已省略 3 条）"},
	}.Prompt()

	assert.Contains(t, got, "工单群的聊天记录")
	assert.Contains(t, got, "已省略 3 条")
}

// TestGenerateReturnsTheModelsText pins the happy path.
func TestGenerateReturnsTheModelsText(t *testing.T) {
	c := &fakeCompleter{text: "## 问题现象\n网关 502\n"}

	got, err := Generate(context.Background(), c, "oncall-summary:tkt-1", "材料")
	require.NoError(t, err)
	assert.Equal(t, "## 问题现象\n网关 502", got, "the text is trimmed of the model's trailing blank line")

	// The request must be one user message with the prompt in it. The far side
	// reads only the last user message, so a system+user split would drop the
	// instruction silently — see Generate's comment.
	require.Len(t, c.requests, 1)
	req := c.requests[0]
	require.Len(t, req.GetMessages(), 1, "one message, or the instruction is dropped")
	assert.Equal(t, "user", req.GetMessages()[0].GetRole())
	assert.Equal(t, "材料", req.GetMessages()[0].GetContent())
	assert.Equal(t, "oncall-summary:tkt-1", req.GetSessionID())
	assert.False(t, req.GetStream(), "the summary is not streamed")
	// The far side requires an agent. The sentinel is the builtin assistant —
	// no instruction, no tools — which is what keeps this a one-shot completion
	// shaped only by the prompt.
	assert.Equal(t, agentv1.NoAgent, req.GetAgent())
}

// TestGenerateRefusesAnAnswerWithNoText pins that a 200 is not a summary.
func TestGenerateRefusesAnAnswerWithNoText(t *testing.T) {
	for _, tc := range []struct {
		why  string
		resp *agentv1.ChatCompletionResponse
	}{
		{"no choices", &agentv1.ChatCompletionResponse{}},
		{"a choice with no message", &agentv1.ChatCompletionResponse{
			Choices: []*agentv1.ChatCompletionChoice{{}},
		}},
		{"a message with no content", &agentv1.ChatCompletionResponse{
			Choices: []*agentv1.ChatCompletionChoice{{Message: &agentv1.ChatCompletionMessage{}}},
		}},
		{"a message with only whitespace", &agentv1.ChatCompletionResponse{
			Choices: []*agentv1.ChatCompletionChoice{{Message: &agentv1.ChatCompletionMessage{Content: " \n "}}},
		}},
	} {
		t.Run(tc.why, func(t *testing.T) {
			_, err := Generate(context.Background(), &fakeCompleter{resp: tc.resp}, "s", "材料")
			require.Error(t, err)
		})
	}
}

// TestGenerateSurfacesTheCallersFailures pins that a failure is not turned into an
// empty summary.
//
// The difference matters downstream: an error leaves the ticket's summary empty and
// logs, while a stored empty string would look like a generation that succeeded
// with nothing to say.
func TestGenerateSurfacesTheCallersFailures(t *testing.T) {
	sentinel := errors.New("the agent service refused")
	_, err := Generate(context.Background(), &fakeCompleter{err: sentinel}, "s", "材料")
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel, "the cause must survive, or an operator cannot tell a timeout from a refusal")
}

// TestGenerateRefusesAnEmptyCall pins the two inputs that would otherwise ask the
// model to write about nothing.
func TestGenerateRefusesAnEmptyCall(t *testing.T) {
	_, err := Generate(context.Background(), nil, "s", "材料")
	require.Error(t, err, "no client")

	_, err = Generate(context.Background(), &fakeCompleter{}, "s", "   ")
	require.Error(t, err, "no prompt")
}

// fakeCompleter records what was asked and answers with what the test set.
type fakeCompleter struct {
	text     string
	resp     *agentv1.ChatCompletionResponse
	err      error
	requests []*agentv1.ChatCompletionRequest
}

func (f *fakeCompleter) ChatCompletions(_ context.Context, req *agentv1.ChatCompletionRequest) (*agentv1.ChatCompletionResponse, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.resp != nil {
		return f.resp, nil
	}
	return &agentv1.ChatCompletionResponse{
		Choices: []*agentv1.ChatCompletionChoice{
			{Message: &agentv1.ChatCompletionMessage{Content: f.text}},
		},
	}, nil
}
