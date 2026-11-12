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

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/pkg/agentcli"
)

// eventBuffer is how many events the run goroutine may get ahead of the caller
// before it blocks. It is a buffer, not a bound on the turn: a caller that stops
// reading stops the agent, which is the failure mode to prefer over losing
// events.
const eventBuffer = 64

// maxQuestionBytes bounds the tool arguments shown in an approval card. A card is
// a preview of what is being asked, and a tool input carrying a whole file would
// push the actual question out of it.
const maxQuestionBytes = 1500

// session is one CLI conversation. It owns the process of the turn in flight,
// the permission request that turn is blocked on, and the agent-side session id
// that makes the next turn a continuation rather than a new conversation.
type session struct {
	agent *Agent
	key   string

	// turnMu serialises turns. The caller is documented as serialising its own
	// calls, but this backend holds a process per conversation, so a second turn
	// arriving concurrently is refused here rather than trusted not to arrive.
	turnMu sync.Mutex
	// runner is the process of the turn in flight, runID the caller's name for it
	// and cancel its off switch. All three are cleared when the turn ends.
	runner *agentcli.Runner
	runID  string
	cancel context.CancelFunc

	mu sync.Mutex
	// pending is the permission request the agent is stopped on, if any. It is
	// what a later RespondPermission answers.
	pending *agentcli.PermissionRequest
	// backendID is the agent-side session id, taken from the init event and
	// handed back as --resume. It is what makes the second turn remember the
	// first, and it is why State.BackendSessionID has something to record.
	backendID string
	// allowAll is the set of tools the user has said "总是允许" (always allow) to. The CLI's
	// permission protocol has no such answer — the host is expected to remember —
	// so this is where that promise is kept.
	allowAll map[string]bool
	closed   bool
}

var _ agent.Session = (*session)(nil)

func newSession(a *Agent, key string) *session {
	return &session{agent: a, key: key, allowAll: make(map[string]bool)}
}

// ID implements agent.Session: the agent's own session id, which the next turn
// resumes.
func (s *session) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backendID
}

// workDir is the directory this turn runs in: the one the conversation asked
// for, or its own workspace when it asked for nothing.
//
// A requested directory is resolved against this backend's allowed roots even
// though the caller that accepted it checked it too. The check here is the one
// that counts — it is the last one before a process is started — and the value it
// checks may have been read from a snapshot written before the configuration
// changed, or handed in by a path that never validated anything.
func (s *session) workDir(requested string) (string, error) {
	if strings.TrimSpace(requested) == "" {
		return agentcli.SessionWorkspace(s.agent.opts.WorkspaceRoot, s.key)
	}
	return agentcli.ResolveDir(s.agent.AllowedRoots(), requested)
}

// AskStream implements agent.Session: one turn of this conversation, streamed.
func (s *session) AskStream(ctx context.Context, req agent.AskRequest, onEvent func(agent.Event) error) error {
	if onEvent == nil {
		return errors.New("cli agent: AskStream needs an event handler")
	}

	workdir, err := s.workDir(req.WorkDir)
	if err != nil {
		return err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("cli agent: this conversation is closed")
	}
	resume := s.backendID
	s.mu.Unlock()
	if req.Resume != "" {
		// The conversation's snapshot wins over what this process remembers: it
		// is the only copy that survives a restart or an idle reap, and the
		// in-memory id is just what this process has seen since it started.
		resume = req.Resume
	}

	runner, err := agentcli.New(agentcli.Options{
		Bin:             s.agent.opts.Bin,
		WorkDir:         workdir,
		Resume:          resume,
		PermissionMode:  s.agent.opts.PermissionMode,
		AllowedTools:    s.agent.opts.AllowedTools,
		DisallowedTools: s.agent.opts.DisallowedTools,
		Model:           s.agent.opts.Model,
		Timeout:         s.agent.opts.TurnTimeout,
		MaxLineBytes:    s.agent.opts.MaxLineBytes,
	})
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The context is handed over with the process, so a caller that stops this
	// run — /stop, an idle reap, a shutdown — can end it the way the turn reads
	// its ending from, rather than killing a process and reporting a crash.
	if err := s.begin(runner, req.RunID, cancel); err != nil {
		return err
	}
	defer s.finish(runner)

	ch := make(chan agentcli.Event, eventBuffer)
	// The run's outcome comes back on a channel rather than through variables the
	// two goroutines share: Run closes ch just before it returns, so reading
	// those variables after the loop would be a read of something still being
	// written.
	done := make(chan runOutcome, 1)
	go func() {
		res, err := runner.Run(runCtx, req.Question, ch)
		done <- runOutcome{res: res, err: err}
	}()

	t := &turn{
		ctx:       ctx,
		sess:      s,
		runner:    runner,
		tools:     make(map[string]string),
		reasoning: req.Reasoning,
	}
	var abort error
	for ev := range ch {
		if abort != nil {
			// The caller has stopped listening. The run is being cancelled; keep
			// draining so the process is not left blocked writing to a channel
			// nobody reads.
			continue
		}
		if err := t.handle(ev, onEvent); err != nil {
			abort = err
			cancel()
		}
	}

	// A run that ended without a result event is still over, and the caller has
	// to be told how: an exit status, a cancelled context, or a stream that was
	// abandoned mid-line.
	if abort != nil {
		return abort
	}
	return t.terminate(runCtx, <-done, onEvent)
}

// runOutcome is what Run reports once the process is gone.
type runOutcome struct {
	res agentcli.Result
	err error
}

// begin claims the conversation for one run, refusing a second one that would
// otherwise start a second process on the same workspace.
func (s *session) begin(runner *agentcli.Runner, runID string, cancel context.CancelFunc) error {
	s.turnMu.Lock()
	if s.runner != nil {
		s.turnMu.Unlock()
		return fmt.Errorf("%w (run %s)", agent.ErrBusy, s.runID)
	}
	s.runner, s.runID, s.cancel = runner, runID, cancel
	s.turnMu.Unlock()
	return nil
}

// inFlight returns the run this conversation is on: its process, the caller's
// name for it, and the function that ends it. They are read together because
// they are written together, and a caller that had one without the others could
// answer a request of the wrong run.
func (s *session) inFlight() (*agentcli.Runner, string, context.CancelFunc) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	return s.runner, s.runID, s.cancel
}

// finish releases the conversation and clears whatever the turn left behind:
// a permission request nobody answered must not be answerable after the turn it
// belonged to has ended, or a stale card button would unblock the next one.
func (s *session) finish(runner *agentcli.Runner) {
	s.turnMu.Lock()
	if s.runner == runner {
		s.runner, s.runID, s.cancel = nil, "", nil
	}
	s.turnMu.Unlock()

	s.mu.Lock()
	s.pending = nil
	s.mu.Unlock()
}

// AnswerPermission implements agent.Session.
//
// req.RunID is the caller's own name for the run, which is the one it put in the
// card's buttons and the one it recorded with the pending question.
// req.RequestID is deliberately unused: it is the answer to a question of *which
// pause*, and this backend has at most one — the CLI's own request id never
// leaves this package, since it identifies a control request rather than a
// conversation, and a caller that echoed it back would be trusting it to still
// be the request it meant.
//
// The continuation needs no stream of its own here. This turn's process is still
// running and its event loop is still reading it, so unblocking the request is
// all it takes for the rest of the run to reach the caller's callback — which is
// why the sink stays attached across a pause for this backend without anything
// having to retain it.
func (s *session) AnswerPermission(ctx context.Context, req agent.AnswerRequest) error {
	runner, current, _ := s.inFlight()

	if runner == nil {
		return errors.New("cli agent: no run is in flight for this conversation")
	}
	if req.RunID != "" && req.RunID != current {
		return fmt.Errorf("cli agent: run %q is not the one in flight (it is %q)", req.RunID, current)
	}

	s.mu.Lock()
	pending := s.pending
	if pending != nil {
		s.pending = nil
	}
	if req.Decision == agent.DecisionAllowAll && pending != nil {
		// Recorded before the answer is sent: if the write fails the run is
		// blocked anyway, and a promise that is kept twice is better than one
		// silently dropped.
		s.allowAll[pending.ToolName] = true
	}
	s.mu.Unlock()

	if pending == nil {
		return errors.New("cli agent: no permission request is waiting for an answer")
	}
	return runner.RespondPermission(ctx, pending.RequestID,
		permissionResult(req.Decision, req.AnswerText()))
}

// Stop implements agent.Session: it terminates the run's process group, which is
// what stops the tools the agent started along with it.
func (s *session) Stop(_ context.Context, runID string) error {
	runner, current, cancel := s.inFlight()

	if runner == nil {
		return errors.New("cli agent: no run is in flight for this conversation")
	}
	if runID != "" && runID != current {
		return fmt.Errorf("cli agent: run %q is not the one in flight (it is %q)", runID, current)
	}
	// Cancelling before killing is what makes this a stop rather than a crash.
	// The run's context is where the turn reads its ending from, so a process
	// killed outright would come back as a failure and turn the card red; ending
	// the context first is what lets it say "stopped".
	if cancel != nil {
		cancel()
	}
	return runner.Close()
}

// Close implements agent.Session. It is safe to call twice, and safe to call
// while a turn is running: the process is terminated either way.
func (s *session) Close() error {
	s.mu.Lock()
	already := s.closed
	s.closed = true
	s.mu.Unlock()
	if already {
		return nil
	}

	runner, _, cancel := s.inFlight()

	s.agent.forget(s)
	if cancel != nil {
		// Same reasoning as Stop: a conversation that was reaped or a backend that
		// is shutting down ends its run, and the turn should say so rather than
		// report the process it lost as a failure.
		cancel()
	}
	if runner == nil {
		return nil
	}
	return runner.Close()
}

// turn is the per-run state the translation needs: which tool each result
// belongs to, whether the turn has already been reported over, and how to answer.
type turn struct {
	ctx    context.Context
	sess   *session
	runner *agentcli.Runner
	// tools maps a tool_use block's id to its tool name, so the result that
	// comes back later can be reported as that tool finishing.
	tools map[string]string
	// reasoning reports whether the conversation wants the model's thinking
	// surfaced. When it does not, no reasoning event is emitted: the card the
	// caller built has no element to render it into, and an update aimed at a
	// missing element fails the call that carries the answer too.
	reasoning bool
	// terminal records that a done/failed/stopped event has been emitted, so the
	// end of the turn does not report a second one.
	terminal bool
}

// handle translates one line of the agent's output, and answers it if the
// conversation has already agreed to auto-approve that tool.
func (t *turn) handle(ev agentcli.Event, onEvent func(agent.Event) error) error {
	switch ev.Type {
	case agentcli.EventSystem:
		if ev.Subtype == agentcli.SubtypeInit && ev.SessionID != "" {
			// The id to resume with. Recorded on every turn that reports one, so a
			// conversation that was compacted or forked follows the agent's own
			// answer rather than the id it was started with.
			t.sess.setBackendID(ev.SessionID)
		}
		return nil

	case agentcli.EventAssistant:
		return t.handleBlocks(ev, onEvent)

	case agentcli.EventUser:
		// A user event carries the tool results, which is where a tool call becomes
		// a finished step. Its text blocks are the agent's own tool-result echo and
		// are not part of the answer.
		return t.handleResults(ev, onEvent)

	case agentcli.EventControlRequest:
		return t.handlePermission(ev, onEvent)

	case agentcli.EventResult:
		t.terminal = true
		if ev.IsError {
			return onEvent(agent.Event{Type: agent.EventFailed, Err: resultError(ev)})
		}
		return onEvent(agent.Event{Type: agent.EventDone, Final: ev.Result})
	}
	// An unknown line type is not an error: the CLI grows new ones, and a turn
	// that renders everything it understands is not a failed turn.
	return nil
}

// handleBlocks reports the assistant's own output: text becomes answer text, a
// thinking block becomes reasoning, and a tool call is remembered so its result
// can be reported as a step.
//
// Thinking and text are kept apart the whole way: the CLI sends the model's
// chain-of-thought as its own block kind, and it must not join the answer.
func (t *turn) handleBlocks(ev agentcli.Event, onEvent func(agent.Event) error) error {
	for _, b := range ev.Blocks() {
		switch b.Type {
		case "text":
			if b.Text == "" {
				continue
			}
			if err := onEvent(agent.Event{Type: agent.EventAnswer, Delta: b.Text}); err != nil {
				return err
			}
		case "thinking":
			if !t.reasoning || b.Thinking == "" {
				continue
			}
			if err := onEvent(agent.Event{Type: agent.EventReasoning, Delta: b.Thinking}); err != nil {
				return err
			}
		case "tool_use":
			t.tools[b.ToolUseID] = b.Name
		}
	}
	return nil
}

// handleResults turns the tool results in a user event into tool steps. Only
// completed tools are reported, matching the llm backend: a step that appears
// when the tool starts and again when it ends would be two steps in a card that
// appends them.
func (t *turn) handleResults(ev agentcli.Event, onEvent func(agent.Event) error) error {
	for _, b := range ev.Blocks() {
		if b.Type != "tool_result" {
			continue
		}
		name := t.tools[b.ToolUseID]
		if name == "" {
			// A result whose call this turn never saw — a resumed session's
			// replay, or a line the codec does not model. Naming it after the
			// tool that ran is not possible, and inventing a name would put a
			// step in the card that never happened.
			continue
		}
		status := "ok"
		if b.IsError {
			status = "fail"
		}
		if err := onEvent(agent.Event{
			Type:  agent.EventToolStep,
			Steps: []agent.ToolStep{{Name: name, Status: status}},
		}); err != nil {
			return err
		}
	}
	return nil
}

// handlePermission deals with a tool the agent has stopped to ask about. If the
// user has already said "总是允许" (always allow) to that tool the answer never reaches them and
// is sent here; otherwise it becomes an interrupt the caller turns into a card.
func (t *turn) handlePermission(ev agentcli.Event, onEvent func(agent.Event) error) error {
	req := ev.Control
	if req == nil {
		return nil
	}

	if t.sess.autoApproved(req.ToolName) {
		return t.runner.RespondPermission(t.ctx, req.RequestID,
			agentcli.PermissionResult{Allow: true})
	}

	t.sess.setPending(req)
	return onEvent(agent.Event{
		Type: agent.EventInterrupted,
		Interrupt: &agent.Interrupt{
			Requests: []agent.InterruptRequest{{
				// No address and no request id: this backend's pause is answered
				// through the process it holds, not by naming a point in a stored
				// run, and its own request id never leaves this package.
				//
				// The CLI has one kind of pause: it is about to run a tool and it
				// is asking whether it may. A question is the llm backend's other
				// shape, and nothing here produces one.
				Kind:     agent.InterruptKindPermission,
				ToolName: req.ToolName,
				Question: describeInput(req.Input),
			}},
		},
	})
}

// terminate reports how a run ended, for the turns that ended without a result
// event to report it. The answer depends on things only known once the process
// is gone, which is why it is separate from handle.
func (t *turn) terminate(runCtx context.Context, out runOutcome, onEvent func(agent.Event) error) error {
	if t.terminal {
		// The result event already said how this turn ended, and said it with the
		// agent's own words.
		return nil
	}
	switch {
	case out.err == nil:
		// Clean exit with no result event: the agent stopped after its last
		// message. The caller still has to be told the turn is over, and the text
		// it produced is what it ended with.
		return onEvent(agent.Event{Type: agent.EventDone, Final: out.res.Answer})
	case runCtx.Err() != nil:
		// Stopped by the caller: /stop, the turn timeout, or a shutdown. Not a
		// failure — the card says stopped, not broken.
		return onEvent(agent.Event{Type: agent.EventStopped})
	default:
		return onEvent(agent.Event{Type: agent.EventFailed, Err: out.err})
	}
}

// setBackendID records the agent-side session id to resume with.
func (s *session) setBackendID(id string) {
	s.mu.Lock()
	s.backendID = id
	s.mu.Unlock()
}

// setPending records the permission request the agent is stopped on.
func (s *session) setPending(req *agentcli.PermissionRequest) {
	s.mu.Lock()
	s.pending = req
	s.mu.Unlock()
}

// autoApproved reports whether the user has already said yes to this tool for
// this conversation.
func (s *session) autoApproved(tool string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allowAll[tool]
}

// permissionResult maps the port's decisions onto the wire shapes the CLI
// validates. There is no "always allow" on the wire — remembering is the host's
// job — so it is answered as a plain allow and recorded by the caller.
func permissionResult(decision, resumeData string) agentcli.PermissionResult {
	switch decision {
	case agent.DecisionDeny:
		// The CLI requires a denial to say why. resumeData is where a caller's
		// explanation travels, and an empty one is filled in by PermissionResult.
		return agentcli.PermissionResult{Allow: false, Message: resumeData}
	default:
		return agentcli.PermissionResult{Allow: true}
	}
}

// describeInput renders a tool call's arguments for an approval card: the input
// is the question the user is being asked, and an approval card that showed only
// the tool's name would be asking them to approve something they cannot see.
func describeInput(input json.RawMessage) string {
	text := strings.TrimSpace(string(input))
	if text == "" || text == "null" {
		return ""
	}
	// Re-encoded rather than passed through, so a pretty-printed input does not
	// spend the card's bytes on indentation.
	var compact any
	if err := json.Unmarshal(input, &compact); err == nil {
		if b, err := json.Marshal(compact); err == nil {
			text = string(b)
		}
	}
	if r := []rune(text); len(r) > maxQuestionBytes {
		return string(r[:maxQuestionBytes]) + " …"
	}
	return text
}

// resultError turns an error result event into the error a caller reports. The
// agent's own message is preferred, because "the agent said no" without saying
// why is a report nobody can act on.
func resultError(ev agentcli.Event) error {
	if message := strings.TrimSpace(ev.Result); message != "" {
		return errors.New(message)
	}
	if ev.Subtype != "" {
		return fmt.Errorf("agent run ended with %s", ev.Subtype)
	}
	return errors.New("agent run failed")
}
