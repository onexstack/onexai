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

package runtimecli

import (
	"context"
	"strings"
	"testing"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/pkg/a2awire"
)

func TestArguments(t *testing.T) {
	config := &Config{Args: []string{"-p", "{{prompt}}", "--verbose"}}
	assert.Equal(t, []string{"-p", "hello", "--verbose"}, config.Arguments("hello", ""))

	config = &Config{Args: []string{"-p"}}
	assert.Equal(t, []string{"-p", "hello"}, config.Arguments("hello", ""))
}

func TestParseClaudeTextAndTool(t *testing.T) {
	events, _, failed, stop := parseClaudeLine(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}}`)
	require.False(t, failed)
	require.False(t, stop)
	require.Len(t, events, 1)
	assert.Equal(t, "text_delta", events[0].kind)
	assert.Equal(t, "hi", events[0].text)

	events, _, _, _ = parseClaudeLine(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"t1","name":"Read"}}}`)
	require.Len(t, events, 1)
	assert.Equal(t, "tool", events[0].kind)
	assert.Equal(t, "Read", events[0].step["name"])

	_, _, failed, stop = parseClaudeLine(`{"type":"result","is_error":true}`)
	assert.True(t, failed)
	assert.True(t, stop)
}

func TestParseClaudeAssistantAndResult(t *testing.T) {
	events, _, failed, stop := parseClaudeLine(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`)
	require.False(t, failed)
	require.False(t, stop)
	require.Len(t, events, 1)
	assert.Equal(t, "assistant_text", events[0].kind)
	assert.Equal(t, "hello", events[0].text)

	_, result, failed, stop := parseClaudeLine(`{"type":"result","result":"hello","is_error":false}`)
	assert.Equal(t, "hello", result)
	assert.False(t, failed)
	assert.True(t, stop)
}

func TestParseClaudeUsage(t *testing.T) {
	line := `{"type":"result","result":"hi","usage":{"input_tokens":10,"output_tokens":5,` +
		`"cache_read_input_tokens":100,"cache_creation_input_tokens":2}}`
	parsed := parseClaudeLineFull(line)
	assert.Equal(t, "hi", parsed.resultText)
	assert.False(t, parsed.failed)
	assert.True(t, parsed.stop)
	require.NotNil(t, parsed.usage)
	usage := parsed.usage
	// The cached counts are part of the prompt: reporting only input_tokens
	// would under-report every cached turn.
	assert.Equal(t, 112, usage.InputTokens)
	assert.Equal(t, 5, usage.OutputTokens)
	assert.Equal(t, 100, usage.CachedInputTokens)
	assert.Equal(t, 117, usage.TotalTokens)
}

func TestParseClaudeWithoutUsageReportsNothing(t *testing.T) {
	parsed := parseClaudeLineFull(`{"type":"result","result":"hi"}`)
	assert.Nil(t, parsed.usage, "a CLI that reports no counters must not claim zero tokens")
}

// TestExecutorPublishesUsage pins the terminal status a peer reads the run's
// cost from.
func TestExecutorPublishesUsage(t *testing.T) {
	payload := `{"type":"result","result":"done","usage":{"input_tokens":7,"output_tokens":3}}`
	executor := NewExecutor(&Config{
		Command: "/bin/sh",
		Args:    []string{"-c", "echo '" + payload + "'"},
		Format:  "claude",
	})
	execCtx := &a2asrv.ExecutorContext{
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Message:   a2atype.NewMessage(a2atype.MessageRoleUser, a2atype.NewTextPart("hello")),
	}
	var last *a2atype.TaskStatusUpdateEvent
	for event, err := range executor.Execute(context.Background(), execCtx) {
		require.NoError(t, err)
		if status, ok := event.(*a2atype.TaskStatusUpdateEvent); ok {
			last = status
		}
	}
	require.NotNil(t, last)
	assert.Equal(t, a2atype.TaskStateCompleted, last.Status.State)

	raw, ok := last.Metadata[a2awire.UsageExtensionURI]
	require.True(t, ok, "the terminal status should carry usage metadata")
	usage, ok := raw.(map[string]any)
	require.True(t, ok, "metadata values are limited to JSON-shaped types")
	assert.Equal(t, 7, usage["inputTokens"])
	assert.Equal(t, 3, usage["outputTokens"])
	assert.Equal(t, 10, usage["totalTokens"])
}

func TestExecutorRunsACommand(t *testing.T) {
	executor := NewExecutor(&Config{Command: "/bin/echo", Args: []string{"{{prompt}}"}, Format: "text"})
	execCtx := &a2asrv.ExecutorContext{
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Message:   a2atype.NewMessage(a2atype.MessageRoleUser, a2atype.NewTextPart("hello")),
	}
	var events []a2atype.Event
	for event, err := range executor.Execute(context.Background(), execCtx) {
		require.NoError(t, err)
		events = append(events, event)
	}
	require.NotEmpty(t, events)
	last := events[len(events)-1].(*a2atype.TaskStatusUpdateEvent)
	assert.Equal(t, a2atype.TaskStateCompleted, last.Status.State)

	var text string
	for _, event := range events {
		if artifact, ok := event.(*a2atype.TaskArtifactUpdateEvent); ok {
			for _, part := range artifact.Artifact.Parts {
				if value, ok := part.Content.(a2atype.Text); ok {
					text += string(value)
				}
			}
		}
	}
	assert.Contains(t, text, "hello")
}

// TestArgumentsResumeOnlyForClaude pins that the resume flag is added for the
// runtime that has one, and only when a conversation was bound.
func TestArgumentsResumeOnlyForClaude(t *testing.T) {
	claude := &Config{Format: "claude", Args: []string{"-p", "{{prompt}}"}}
	assert.Equal(t, []string{"-p", "hi"}, claude.Arguments("hi", ""))
	assert.Equal(t, []string{"-p", "hi", "--resume", "s-1"}, claude.Arguments("hi", "s-1"))

	text := &Config{Format: "text", Args: []string{"{{prompt}}"}}
	assert.Equal(t, []string{"hi"}, text.Arguments("hi", "s-1"),
		"a runtime without a resume flag must not be handed one")
}

// TestSessionStoreRoundTrip pins the continuation contract: one identity per
// context, reloadable, and bound exactly once.
func TestSessionStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := newSessionStore(dir, "claude")
	require.NoError(t, err)

	_, ok := store.load("ctx-1")
	assert.False(t, ok, "a fresh context has no conversation")

	require.NoError(t, store.bind("ctx-1", "s-1"))
	id, ok := store.load("ctx-1")
	require.True(t, ok)
	assert.Equal(t, "s-1", id)

	// Rebinding the same id is a no-op; a different one is refused, because a
	// context is one conversation.
	require.NoError(t, store.bind("ctx-1", "s-1"))
	require.Error(t, store.bind("ctx-1", "s-2"))

	// A second process must see what the first persisted.
	reopened, err := newSessionStore(dir, "claude")
	require.NoError(t, err)
	id, ok = reopened.load("ctx-1")
	require.True(t, ok)
	assert.Equal(t, "s-1", id)
}

// TestSessionStoreIgnoresAnotherRuntimesState pins that a directory shared
// between two harnesses cannot hand one of them the other's conversation.
func TestSessionStoreIgnoresAnotherRuntimesState(t *testing.T) {
	dir := t.TempDir()
	claude, err := newSessionStore(dir, "claude")
	require.NoError(t, err)
	require.NoError(t, claude.bind("ctx-1", "s-1"))

	codex, err := newSessionStore(dir, "codex")
	require.NoError(t, err)
	_, ok := codex.load("ctx-1")
	assert.False(t, ok)
}

// TestSessionStoreDisabledWithoutADirectory pins that continuation is opt-in.
func TestSessionStoreDisabledWithoutADirectory(t *testing.T) {
	store, err := newSessionStore("", "claude")
	require.NoError(t, err)
	assert.Nil(t, store)
	require.NoError(t, store.bind("ctx", "s"))
	_, ok := store.load("ctx")
	assert.False(t, ok)
}

// TestParseClaudeCapturesTheSessionID pins that the id is remembered from both
// envelopes: init announces it, result repeats it, and a run that fails after
// init must still leave a handle behind.
func TestParseClaudeCapturesTheSessionID(t *testing.T) {
	init := parseClaudeLineFull(`{"type":"system","subtype":"init","session_id":"s-9"}`)
	assert.Equal(t, "s-9", init.sessionID)
	assert.False(t, init.stop)

	result := parseClaudeLineFull(`{"type":"result","result":"ok","session_id":"s-9"}`)
	assert.Equal(t, "s-9", result.sessionID)
	assert.True(t, result.stop)
}

// runLines feeds JSONL lines through the executor and returns the events.
func runLines(t *testing.T, lines ...string) []a2atype.Event {
	t.Helper()
	payload := strings.Join(lines, "\n") + "\n"
	executor := NewExecutor(&Config{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf '%s' '" + strings.ReplaceAll(payload, "'", "'\\''") + "'"},
		Format:  "claude",
	})
	execCtx := &a2asrv.ExecutorContext{
		TaskID:    "task-dup",
		ContextID: "ctx-dup",
		Message:   a2atype.NewMessage(a2atype.MessageRoleUser, a2atype.NewTextPart("hi")),
	}
	var events []a2atype.Event
	for event, err := range executor.Execute(context.Background(), execCtx) {
		require.NoError(t, err)
		events = append(events, event)
	}
	return events
}

// toolEvents counts the tool artifacts in a stream.
func toolEvents(events []a2atype.Event) int {
	count := 0
	for _, event := range events {
		artifact, ok := event.(*a2atype.TaskArtifactUpdateEvent)
		if !ok || artifact.Artifact == nil || len(artifact.Artifact.Parts) == 0 {
			continue
		}
		if kind, _ := artifact.Artifact.Parts[0].Metadata["onexai.dev/a2a/part-type"].(string); kind == "tool" {
			count++
		}
	}
	return count
}

// TestToolEventIsNotDuplicated pins that the same call reported twice — once
// when the block opens and once inside the completed assistant message —
// produces one artifact. Two artifacts read as "the tool ran twice".
func TestToolEventIsNotDuplicated(t *testing.T) {
	events := runLines(t,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"t1","name":"Read"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]}}`,
		`{"type":"result","result":"done"}`,
	)
	assert.Equal(t, 1, toolEvents(events))
}

// TestLaterAssistantMessageSurvivesStreaming pins the second parser bug: a run
// that streamed one reply and then produced a second, complete one lost the
// second because a single global "we already streamed something" flag suppressed
// every later message.
func TestLaterAssistantMessageSurvivesStreaming(t *testing.T) {
	events := runLines(t,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"first"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}`,
		`{"type":"result","result":"firstsecond"}`,
	)

	var text string
	for _, event := range events {
		artifact, ok := event.(*a2atype.TaskArtifactUpdateEvent)
		if !ok || artifact.Artifact == nil {
			continue
		}
		for _, part := range artifact.Artifact.Parts {
			if value, ok := part.Content.(a2atype.Text); ok {
				text += string(value)
			}
		}
	}
	assert.Contains(t, text, "first")
	assert.Contains(t, text, "second", "a later complete message must not be dropped")
}

// TestResumeThatChangesIDFails pins the anti-fork rule: a `--resume` that comes
// back with a different session id did not resume, and continuing would silently
// switch the conversation the user thinks they are in.
func TestResumeThatChangesIDFails(t *testing.T) {
	dir := t.TempDir()
	store, err := newSessionStore(dir, "claude")
	require.NoError(t, err)
	require.NoError(t, store.bind("ctx-fork", "wanted"))

	executor := NewExecutor(&Config{
		Name:    "claude",
		Command: "/bin/sh",
		Args: []string{"-c",
			`printf '{"type":"result","result":"ok","session_id":"different"}\n'`},
		Format:     "claude",
		SessionDir: dir,
	})
	execCtx := &a2asrv.ExecutorContext{
		TaskID:    "task-fork",
		ContextID: "ctx-fork",
		Message:   a2atype.NewMessage(a2atype.MessageRoleUser, a2atype.NewTextPart("hi")),
	}

	last := lastStatus(t, collectEvents(t, executor, execCtx))
	assert.Equal(t, a2atype.TaskStateFailed, last.Status.State)
}

// TestOneTurnPerContext pins that two tasks cannot drive one native
// conversation at the same time.
func TestOneTurnPerContext(t *testing.T) {
	executor := NewExecutor(&Config{
		Command: "/bin/sh",
		Args:    []string{"-c", "sleep 2"},
		Format:  "text",
	})
	release, busy := executor.beginTurn("ctx-locked")
	require.False(t, busy)

	_, busy = executor.beginTurn("ctx-locked")
	assert.True(t, busy, "the second turn must be refused while the first holds the context")

	release()
	_, busy = executor.beginTurn("ctx-locked")
	assert.False(t, busy, "the context is free again once the turn releases it")
}

// collectEvents drains one execution.
func collectEvents(t *testing.T, executor *Executor, execCtx *a2asrv.ExecutorContext) []a2atype.Event {
	t.Helper()
	var events []a2atype.Event
	for event, err := range executor.Execute(context.Background(), execCtx) {
		require.NoError(t, err)
		events = append(events, event)
	}
	return events
}

// lastStatus is the state the task ends in.
func lastStatus(t *testing.T, events []a2atype.Event) *a2atype.TaskStatusUpdateEvent {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if status, ok := events[i].(*a2atype.TaskStatusUpdateEvent); ok {
			return status
		}
	}
	require.FailNow(t, "the stream carried no status event")
	return nil
}
