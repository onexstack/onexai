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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/onexstack/onexai/internal/agent/pkg/a2aevent"
	"github.com/onexstack/onexai/internal/agent/pkg/a2awire"
)

// Executor runs the configured CLI for one A2A task.
type Executor struct {
	Config *Config

	// sessions remembers each context's native conversation id. Nil when the
	// deployment did not configure one.
	sessions *sessionStore

	// approval is the permission-prompt broker, nil unless configured. One per
	// executor: every task of this runtime shares the same port.
	approval *ApprovalBroker

	mu    sync.Mutex
	procs map[a2atype.TaskID]*procEntry

	// contexts serialises turns that share one A2A context. Two tasks on the
	// same context would otherwise drive the same native conversation at once,
	// and the CLI's session file has one writer.
	contexts sync.Map // contextID -> *contextLock
}

// contextLock is one conversation's turn lock with a reference count, so the
// entry disappears when the conversation is idle instead of accumulating one
// per context the process has ever seen.
type contextLock struct {
	mu   sync.Mutex
	refs int
}

var contextLocksMu sync.Mutex

// procEntry is one running CLI. canceled is set by Cancel and read by Execute:
// the cancel path and the execution path race by design, and whichever finishes
// second must not overturn the other's verdict.
type procEntry struct {
	cmd      *exec.Cmd
	canceled bool
}

var _ a2asrv.AgentExecutor = (*Executor)(nil)

// NewExecutor builds an executor.
func NewExecutor(config *Config) *Executor {
	store, err := newSessionStore(config.SessionDir, config.Name)
	if err != nil {
		// Continuation is an optimization, not a requirement: a deployment with
		// an unwritable directory still answers, it just starts every turn
		// fresh. Saying so once is better than failing every task.
		slog.Warn("runtimecli: continuation is disabled", "error", err)
		store = nil
	}
	executor := &Executor{Config: config, sessions: store, procs: make(map[a2atype.TaskID]*procEntry)}
	if config.Approval.Enabled && config.Format == "claude" {
		broker, err := NewApprovalBroker()
		if err != nil {
			// Approval is a capability, not a requirement: a runtime that cannot
			// host the prompt tool still answers, it just never asks.
			slog.Warn("runtimecli: approval is disabled", "error", err)
		} else {
			executor.approval = broker
		}
	}
	return executor
}

// Execute implements a2asrv.AgentExecutor.
func (e *Executor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2atype.Event, error] {
	return func(yield func(a2atype.Event, error) bool) {
		if execCtx.StoredTask == nil {
			if !yield(a2atype.NewSubmittedTask(execCtx, execCtx.Message), nil) {
				return
			}
		}
		if !yield(a2aevent.Status(execCtx, a2atype.TaskStateWorking, ""), nil) {
			return
		}
		release, busy := e.beginTurn(execCtx.ContextID)
		if busy {
			yield(a2aevent.Status(execCtx, a2atype.TaskStateFailed,
				"another turn is already running in this conversation"), nil)
			return
		}
		defer release()

		prompt := a2aevent.MessageText(execCtx.Message)
		if strings.TrimSpace(prompt) == "" {
			yield(a2aevent.Status(execCtx, a2atype.TaskStateFailed, "the request carries no message text"), nil)
			return
		}

		resumeID, _ := e.sessions.load(execCtx.ContextID)
		// A resumed turn must not also re-send the prompt as the conversation
		// opener: the CLI already has it. Claude takes the prompt as the new
		// turn either way, so only the resume flag changes.
		args, cleanup := e.argumentsWithApproval(prompt, resumeID)
		defer cleanup()
		cmd := exec.CommandContext(ctx, e.Config.Command, args...) //nolint:gosec // configured
		cmd.Dir = e.Config.Workdir
		cmd.Env = e.Config.Environment()
		// Its own process group: a CLI starts children of its own, and only the
		// group can be stopped as a unit.
		configureProcessGroup(cmd)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			yield(a2aevent.Status(execCtx, a2atype.TaskStateFailed, "could not read the CLI output"), nil)
			return
		}
		stderr := newBoundedBuffer(DefaultStderrLimit)
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			slog.WarnContext(ctx, "runtimecli: start", "error", err)
			yield(a2aevent.Status(execCtx, a2atype.TaskStateFailed, "the CLI could not be started"), nil)
			return
		}
		e.track(execCtx.TaskID, cmd)
		defer e.untrack(execCtx.TaskID)

		state := e.consume(ctx, execCtx, stdout, yield)
		if state.sessionID != "" {
			if err := e.sessions.bind(execCtx.ContextID, state.sessionID); err != nil {
				slog.WarnContext(ctx, "runtimecli: could not remember the conversation", "error", err)
			}
		}
		waitErr := cmd.Wait()
		if state.stopped {
			return
		}
		// A resume that produced a different id did not resume: the CLI started
		// a fresh conversation and the next turn would silently continue the
		// wrong thread. Refusing is the only honest outcome.
		if resumeID != "" && state.sessionID != "" && state.sessionID != resumeID {
			slog.WarnContext(ctx, "runtimecli: the resumed conversation changed id",
				"expected", resumeID, "got", state.sessionID)
			yield(a2aevent.Status(execCtx, a2atype.TaskStateFailed,
				"the conversation could not be resumed"), nil)
			return
		}
		// Cancel won the race if it arrived first: reporting a failure here
		// would tell the caller their run broke when they stopped it.
		if e.wasCanceled(execCtx.TaskID) {
			yield(a2aevent.Status(execCtx, a2atype.TaskStateCanceled, ""), nil)
			return
		}
		switch {
		case state.failed:
			return
		case waitErr != nil:
			slog.WarnContext(ctx, "runtimecli: command failed", "error", waitErr, "stderr", stderr.String())
			yield(a2aevent.Status(execCtx, a2atype.TaskStateFailed, "the CLI exited with an error"), nil)
			return
		default:
			yield(a2aevent.StatusWithUsage(execCtx, a2atype.TaskStateCompleted, "", state.usage), nil)
		}
	}
}

// Cancel implements a2asrv.AgentExecutor.
//
// The stop is a signal to the process group with a grace period before the
// kill, not an immediate SIGKILL: a CLI that is told politely writes the
// session state the next turn resumes from.
func (e *Executor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2atype.Event, error] {
	return func(yield func(a2atype.Event, error) bool) {
		e.cancel(execCtx.TaskID)
		yield(a2aevent.Status(execCtx, a2atype.TaskStateCanceled, ""), nil)
	}
}

type consumeState struct {
	failed  bool
	stopped bool
	// usage is the run's token accounting, when the CLI reports one.
	usage *a2awire.Usage
	// sessionID is the CLI's own conversation id, when it reports one.
	sessionID string
}

func (e *Executor) consume(
	ctx context.Context, execCtx *a2asrv.ExecutorContext, stdout io.Reader,
	yield func(a2atype.Event, error) bool,
) consumeState {
	var artifactID a2atype.ArtifactID
	var state consumeState
	// The stream repeats itself in ways that are easy to mistake for content:
	// a tool appears once at content_block_start and again inside the complete
	// assistant message, and a full assistant message repeats what its deltas
	// already said. Both are deduped here rather than trusted upstream.
	var emitted strings.Builder
	seenTool := make(map[string]bool)
	seenResult := make(map[string]bool)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if e.Config.Format == "text" {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if !e.emitText(execCtx, &artifactID, line+"\n", &emitted, yield) {
				state.stopped = true
				return state
			}
			continue
		}
		parsed := parseClaudeLineFull(line)
		events, resultText, usage, failed, stop := parsed.events, parsed.resultText, parsed.usage, parsed.failed, parsed.stop
		if usage != nil {
			state.usage = usage
		}
		if parsed.sessionID != "" {
			state.sessionID = parsed.sessionID
		}
		for _, event := range events {
			switch event.kind {
			case "text_delta":
				if !e.emitText(execCtx, &artifactID, event.text, &emitted, yield) {
					state.stopped = true
					return state
				}
			case "assistant_text":
				// --output-format stream-json without --include-partial-messages
				// reports the complete assistant message instead of deltas.
				//
				// Skipping it whenever *any* delta was seen dropped whole later
				// messages: a run that streamed one reply and then produced a
				// second, complete one lost the second. The question is whether
				// this text was already said, not whether something was.
				if strings.HasSuffix(emitted.String(), event.text) {
					continue
				}
				if !e.emitText(execCtx, &artifactID, event.text, &emitted, yield) {
					state.stopped = true
					return state
				}
			case "tool":
				if !e.emitTool(execCtx, event.step, seenTool, seenResult, yield) {
					state.stopped = true
					return state
				}
			}
		}
		if stop && resultText != "" && emitted.Len() == 0 {
			if !e.emitText(execCtx, &artifactID, resultText, &emitted, yield) {
				state.stopped = true
				return state
			}
		}
		if failed {
			state.failed = true
			yield(a2aevent.Status(execCtx, a2atype.TaskStateFailed, "the CLI reported an error"), nil)
		}
		if stop {
			return state
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "runtimecli: read output", "error", err)
		state.failed = true
		yield(a2aevent.Status(execCtx, a2atype.TaskStateFailed, "the CLI output could not be read"), nil)
	}
	return state
}

func (e *Executor) emitText(
	execCtx *a2asrv.ExecutorContext, artifactID *a2atype.ArtifactID, text string,
	emitted *strings.Builder, yield func(a2atype.Event, error) bool,
) bool {
	if text == "" {
		return true
	}
	emitted.WriteString(text)
	if *artifactID == "" {
		first := a2atype.NewArtifactEvent(execCtx, a2atype.NewTextPart(text))
		*artifactID = first.Artifact.ID
		return yield(first, nil)
	}
	return yield(a2atype.NewArtifactUpdateEvent(execCtx, *artifactID, a2atype.NewTextPart(text)), nil)
}

// emitTool publishes a tool start or result exactly once per id.
//
// The same call is reported twice by the stream — once when the block opens and
// once in the completed message — and a retried call can report its result
// twice. A renderer showing the same step twice looks like the tool ran twice.
func (e *Executor) emitTool(
	execCtx *a2asrv.ExecutorContext, step map[string]any,
	seenTool, seenResult map[string]bool, yield func(a2atype.Event, error) bool,
) bool {
	id, _ := step["id"].(string)
	if id != "" {
		status, _ := step["status"].(string)
		if _, isResult := step["result"]; isResult || (status != "" && status != "pending") {
			if seenResult[id] {
				return true
			}
			seenResult[id] = true
		} else {
			if seenTool[id] {
				return true
			}
			seenTool[id] = true
		}
	}
	return yield(toolEvent(execCtx, step), nil)
}

func (e *Executor) track(taskID a2atype.TaskID, cmd *exec.Cmd) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.procs[taskID] = &procEntry{cmd: cmd}
}

func (e *Executor) untrack(taskID a2atype.TaskID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.procs, taskID)
}

// beginTurn claims the context's turn slot, or reports that it is taken.
func (e *Executor) beginTurn(contextID string) (release func(), busy bool) {
	if contextID == "" {
		return func() {}, false
	}
	contextLocksMu.Lock()
	entry, _ := e.contexts.Load(contextID)
	lock, _ := entry.(*contextLock)
	if lock == nil {
		lock = &contextLock{}
		e.contexts.Store(contextID, lock)
	}
	lock.refs++
	contextLocksMu.Unlock()

	if !lock.mu.TryLock() {
		contextLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			e.contexts.Delete(contextID)
		}
		contextLocksMu.Unlock()
		return nil, true
	}
	return func() {
		lock.mu.Unlock()
		contextLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			e.contexts.Delete(contextID)
		}
		contextLocksMu.Unlock()
	}, false
}

func (e *Executor) cancel(taskID a2atype.TaskID) {
	e.mu.Lock()
	entry := e.procs[taskID]
	if entry != nil {
		entry.canceled = true
	}
	e.mu.Unlock()
	if entry != nil {
		stopProcess(entry.cmd, DefaultStopGrace)
	}
}

// wasCanceled reports whether Cancel claimed this task before the run finished.
func (e *Executor) wasCanceled(taskID a2atype.TaskID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry := e.procs[taskID]
	return entry != nil && entry.canceled
}

type parsedEvent struct {
	kind string // text | tool
	text string
	step map[string]any
}

type claudeEnvelope struct {
	Type    string          `json:"type"`
	Event   json.RawMessage `json:"event"`
	Message json.RawMessage `json:"message"`
	Result  string          `json:"result"`
	IsError bool            `json:"is_error"`
	Usage   claudeUsage     `json:"usage"`
	// SessionID is the CLI's own conversation id. `system/init` reports it when
	// the process starts and `result` repeats it, so a run that ends without a
	// result still leaves a handle for the next turn.
	SessionID string `json:"session_id"`
}

// claudeUsage is the token accounting Claude Code reports on its result
// envelope. The cached counts are reported separately and are part of the
// prompt, so they are added into the input total here: a caller that read only
// input_tokens would under-report every cached turn.
type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// report converts the CLI's counters to the wire payload.
func (u claudeUsage) report() a2awire.Usage {
	input := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	return a2awire.Usage{
		InputTokens:       input,
		OutputTokens:      u.OutputTokens,
		CachedInputTokens: u.CacheReadInputTokens,
		TotalTokens:       input + u.OutputTokens,
	}
}

type claudeInner struct {
	Type  string `json:"type"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
	ContentBlock struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`
}

// parseClaudeLine understands the subset of Claude Code stream-json this
// runtime publishes: text deltas, tool starts/results, and the terminal result.
func parseClaudeLine(line string) (events []parsedEvent, resultText string, failed, stop bool) {
	parsed := parseClaudeLineFull(line)
	return parsed.events, parsed.resultText, parsed.failed, parsed.stop
}

// parsedLine is one decoded JSONL line.
type parsedLine struct {
	events     []parsedEvent
	resultText string
	usage      *a2awire.Usage
	// sessionID is the CLI's conversation id. It is reported on the init and
	// result envelopes, and it is the only handle a later turn has for
	// continuing this conversation.
	sessionID string
	failed    bool
	stop      bool
}

// parseClaudeLineWithUsage is parseClaudeLine plus the run's token accounting,
// which only the terminal result carries.
func parseClaudeLineFull(line string) parsedLine {
	var envelope claudeEnvelope
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		return parsedLine{}
	}
	switch envelope.Type {
	case "stream_event":
		var inner claudeInner
		if err := json.Unmarshal(envelope.Event, &inner); err != nil {
			return parsedLine{}
		}
		switch inner.Type {
		case "content_block_delta":
			if inner.Delta.Type == "text_delta" && inner.Delta.Text != "" {
				return parsedLine{events: []parsedEvent{{kind: "text_delta", text: inner.Delta.Text}}}
			}
		case "content_block_start":
			if inner.ContentBlock.Type == "tool_use" {
				return parsedLine{events: []parsedEvent{{kind: "tool", step: map[string]any{
					"id": inner.ContentBlock.ID, "name": inner.ContentBlock.Name, "status": "pending",
				}}}}
			}
		}
	case "assistant":
		var message struct {
			Content []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		}
		if err := json.Unmarshal(envelope.Message, &message); err != nil {
			return parsedLine{}
		}
		var events []parsedEvent
		for _, block := range message.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					events = append(events, parsedEvent{kind: "assistant_text", text: block.Text})
				}
			case "tool_use":
				events = append(events, parsedEvent{kind: "tool", step: map[string]any{
					"id": block.ID, "name": block.Name, "status": "pending",
				}})
			}
		}
		return parsedLine{events: events}
	case "user":
		var message struct {
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
				Content   any    `json:"content"`
				IsError   bool   `json:"is_error"`
			} `json:"content"`
		}
		if err := json.Unmarshal(envelope.Message, &message); err != nil {
			return parsedLine{}
		}
		var results []parsedEvent
		for _, block := range message.Content {
			if block.Type != "tool_result" {
				continue
			}
			status := "ok"
			if block.IsError {
				status = "fail"
			}
			results = append(results, parsedEvent{kind: "tool", step: map[string]any{
				"id": block.ToolUseID, "status": status, "result": textOf(block.Content),
			}})
		}
		return parsedLine{events: results}
	case "system":
		// The init envelope is where a conversation's id first appears. It is
		// recorded even when the run later fails, so the next turn can resume
		// what this one started.
		return parsedLine{sessionID: envelope.SessionID}
	case "result":
		return parsedLine{resultText: envelope.Result, usage: usageOrNil(envelope.Usage), sessionID: envelope.SessionID, failed: envelope.IsError, stop: true}
	}
	return parsedLine{}
}

// usageOrNil reports a usage payload only when the CLI actually counted
// something. A result with no counters is not "zero tokens", it is a CLI that
// does not report them, and publishing zeros would claim a measurement.
func usageOrNil(u claudeUsage) *a2awire.Usage {
	report := u.report()
	if report.InputTokens == 0 && report.OutputTokens == 0 {
		return nil
	}
	return &report
}

func textOf(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					b.WriteString(text)
				}
			}
		}
		return b.String()
	default:
		encoded, _ := json.Marshal(v)
		return string(encoded)
	}
}

func toolEvent(execCtx *a2asrv.ExecutorContext, step map[string]any) *a2atype.TaskArtifactUpdateEvent {
	part := a2atype.NewDataPart(step)
	part.Metadata = map[string]any{a2awire.PartTypeMetadataKey: "tool"}
	return a2atype.NewArtifactEvent(execCtx, part)
}

func (e *Executor) String() string { return fmt.Sprintf("runtimecli(%s)", e.Config.Command) }

// argumentsWithApproval builds the CLI arguments and, when approval is on, the
// MCP config file that points the CLI at this process's approval broker.
//
// The file is written per turn rather than once at start-up because it carries
// the broker's address, which is only known once the listener exists, and
// because a deployment can move the runtime without rewriting anything.
func (e *Executor) argumentsWithApproval(prompt, resumeID string) ([]string, func()) {
	args := e.Config.Arguments(prompt, resumeID)
	if e.approval == nil {
		return args, func() {}
	}
	file, err := os.CreateTemp("", "onexai-approval-*.json")
	if err != nil {
		slog.Warn("runtimecli: approval config could not be written; the turn runs without it", "error", err)
		return args, func() {}
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	config := map[string]any{
		"mcpServers": map[string]any{
			"approvals": map[string]any{
				"type":    "http",
				"url":     e.approval.URL(),
				"headers": map[string]string{"Authorization": "Bearer " + e.approval.Token()},
			},
		},
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		_ = file.Close()
		cleanup()
		return args, func() {}
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		cleanup()
		return args, func() {}
	}
	_ = file.Close()
	// The settings file is what makes the CLI ask at all: the permission-prompt
	// tool is only consulted for calls the CLI would otherwise allow, so a
	// deployment that wants approval has to say which tools those are.
	settings, err := os.CreateTemp("", "onexai-settings-*.json")
	if err != nil {
		slog.Warn("runtimecli: approval settings could not be written; the turn runs without approval", "error", err)
		_ = file.Close()
		cleanup()
		return args, func() {}
	}
	ask := e.Config.Approval.Tools
	if len(ask) == 0 {
		ask = []string{"*"}
	}
	encodedSettings, err := json.Marshal(map[string]any{
		"permissions": map[string]any{"ask": ask},
	})
	if err == nil {
		_, err = settings.Write(encodedSettings)
	}
	_ = settings.Close()
	if err != nil {
		slog.Warn("runtimecli: approval settings could not be written; the turn runs without approval", "error", err)
		_ = os.Remove(settings.Name())
		_ = file.Close()
		cleanup()
		return args, func() {}
	}
	removeSettings := func() { _ = os.Remove(settings.Name()) }
	args = append(args,
		"--mcp-config", file.Name(),
		// An isolated settings file: the user's own permissions must not decide
		// which of this deployment's tool calls reach a human.
		"--setting-sources", "",
		"--settings", settings.Name(),
		// The tool name is what Claude calls when a call would otherwise
		// prompt; its answer is the human's decision.
		"--permission-prompt-tool", "mcp__approvals__"+ApprovalToolName,
	)
	return args, func() {
		cleanup()
		removeSettings()
	}
}
