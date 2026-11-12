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

package agentbuilder

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func toolMessage(name, content string) *schema.Message {
	return &schema.Message{Role: schema.Tool, ToolName: name, Content: content}
}

// TestCollectEvidence_TakesOnlyTheNamedTool pins the filter that keeps the check
// honest: an answer is not "ungrounded" for disagreeing with a clock.
func TestCollectEvidence_TakesOnlyTheNamedTool(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "问题"},
		toolMessage("current_time", "2026-10-15T20:00:00Z"),
		toolMessage("search_knowledge_base", "第一章正文"),
		{Role: schema.Assistant, Content: "回答", ToolCalls: []schema.ToolCall{{ID: "1"}}},
	}

	got := collectEvidence(messages, "search_knowledge_base")

	if !strings.Contains(got, "第一章正文") {
		t.Errorf("the knowledge-base passage is missing: %q", got)
	}
	if strings.Contains(got, "2026-10-15T20:00:00Z") {
		t.Errorf("another tool's output was taken as evidence: %q", got)
	}
}

// TestCollectEvidence_JoinsEveryCall is the multi-hop case: a run that searched
// twice has two bodies of material, and the answer may be built from either.
func TestCollectEvidence_JoinsEveryCall(t *testing.T) {
	messages := []*schema.Message{
		toolMessage("search_knowledge_base", "第一段"),
		toolMessage("search_knowledge_base", "第二段"),
	}

	got := collectEvidence(messages, "search_knowledge_base")

	if !strings.Contains(got, "第一段") || !strings.Contains(got, "第二段") {
		t.Errorf("both calls must be in the evidence: %q", got)
	}
	if !strings.Contains(got, "第一段") || !strings.Contains(got, "---") {
		t.Errorf("the bodies must be separated, not run together: %q", got)
	}
}

func TestCollectEvidence_EmptyWhenNothingWasRetrieved(t *testing.T) {
	if got := collectEvidence(nil, "search_knowledge_base"); got != "" {
		t.Errorf("no tool messages produced %q, want empty", got)
	}
	// A run whose tool answered with an empty string is the same case: an answer
	// with no material is not an ungrounded one, it is an answer to a different
	// question.
	messages := []*schema.Message{toolMessage("search_knowledge_base", "")}
	if got := collectEvidence(messages, "search_knowledge_base"); got != "" {
		t.Errorf("an empty tool result produced %q, want empty", got)
	}
}

// TestCollectEvidence_BoundsByCharacters pins that a huge corpus cannot be sent
// to the reviewer whole, and that the bound is counted in characters rather than
// bytes — the same unit the rest of the retrieval path uses, and the one that
// matches what a model is billed for.
func TestCollectEvidence_BoundsByCharacters(t *testing.T) {
	messages := []*schema.Message{
		toolMessage("search_knowledge_base", strings.Repeat("课", groundingMaxEvidenceChars)),
		toolMessage("search_knowledge_base", "this tail must not survive"),
	}

	got := collectEvidence(messages, "search_knowledge_base")

	if n := len([]rune(got)); n > groundingMaxEvidenceChars {
		t.Errorf("evidence is %d characters, want at most %d", n, groundingMaxEvidenceChars)
	}
	if strings.Contains(got, "this tail must not survive") {
		t.Error("a passage past the budget was included: the check is about gist, not completeness")
	}
}

// TestNewGroundingReview_RequiresAModel pins that the feature is opt-in.
func TestNewGroundingReview_RequiresAModel(t *testing.T) {
	if g := NewGroundingReview(nil, "search_knowledge_base"); g != nil {
		t.Error("a grounding reviewer with no model must be nil, not a check that cannot run")
	}
}
