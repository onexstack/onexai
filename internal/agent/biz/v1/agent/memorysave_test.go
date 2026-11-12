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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

func TestMemoryPlanNeedsAllTheParts(t *testing.T) {
	spec := RunSpec{MemoryName: "team", MemoryEnabled: true, MemoryAutoSaveEvery: 3}

	assert.Nil(t, memoryPlanFor(RunSpec{MemoryName: "team", MemoryAutoSaveEvery: 3},
		"a", "team-a", "u1", "", "", 3), "memory must be enabled")
	assert.Nil(t, memoryPlanFor(RunSpec{MemoryName: "team", MemoryEnabled: true},
		"a", "team-a", "u1", "", "", 3), "an interval is required")
	assert.Nil(t, memoryPlanFor(spec, "a", "team-a", "", "", "", 3), "a subject is required")
	assert.Nil(t, memoryPlanFor(spec, "", "team-a", "u1", "", "", 3), "an agent is required")

	plan := memoryPlanFor(spec, "a", "team-a", "u1", "session-1", "run-1", 3)
	assert.NotNil(t, plan)
	assert.Equal(t, "a", plan.Agent)
	assert.Equal(t, "team-a", plan.AgentNamespace)
	assert.Equal(t, "u1", plan.Subject)
	assert.Equal(t, "session-1", plan.SessionID)
	assert.Equal(t, "run-1", plan.RunID)
}

func TestParseExtractedFacts(t *testing.T) {
	raw := "```json\n" + `[
		{"content":"  prefers PostgreSQL  ","type":"profile","importance":0.9,"confidence":0.8,"tags":["db"]},
		{"content":"","type":"semantic"},
		{"content":"duplicate","type":"weird","importance":2,"confidence":-1},
		{"content":"duplicate","type":"semantic"}
	]` + "\n```"

	facts := parseExtractedFacts(raw, 10)
	assert.Len(t, facts, 2, "empty and duplicate content is dropped")
	assert.Equal(t, "prefers PostgreSQL", facts[0].Content)
	assert.Equal(t, "profile", facts[0].Type)
	assert.Equal(t, float32(0.9), facts[0].Importance)
	assert.Equal(t, "semantic", facts[1].Type, "an unknown type falls back to semantic")
	assert.Equal(t, float32(1), facts[1].Importance)
	assert.Equal(t, float32(0.6), facts[1].Confidence)
}

func TestParseExtractedFactsRespectsTheCap(t *testing.T) {
	raw := `[{"content":"a"},{"content":"b"},{"content":"c"}]`
	assert.Len(t, parseExtractedFacts(raw, 2), 2)
	assert.Empty(t, parseExtractedFacts("not json", 10))
}

func TestMemoryPlanIsDueOnItsInterval(t *testing.T) {
	plan := &memoryPlan{Every: 3}
	assert.False(t, plan.due(), "a nil turn never saves")
	for turn, want := range map[int]bool{1: false, 2: false, 3: true, 6: true, 7: false} {
		plan.Turn = turn
		assert.Equal(t, want, plan.due(), "turn %d", turn)
	}
	assert.False(t, (&memoryPlan{}).due(), "no interval means no auto-save")
}

func TestUserTurnsCountsOnlyUserMessages(t *testing.T) {
	history := []agentapi.Message{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "two"},
		{Role: "tool", Content: "step"},
	}
	assert.Equal(t, 2, userTurns(history))
	assert.Equal(t, 0, userTurns(nil))
}

func TestTruncateMemoryKeepsValidText(t *testing.T) {
	assert.Equal(t, "short", truncateMemory("short"))

	long := strings.Repeat("记", maxAutoMemoryChars+10)
	got := truncateMemory(long)
	assert.Equal(t, maxAutoMemoryChars+1, len([]rune(got)), "the ellipsis replaces the tail, it does not exceed the bound")
	assert.True(t, strings.HasSuffix(got, "…"))
}
