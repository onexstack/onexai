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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/pkg/agentcli"
)

// TestCLISessionTranslatesEvents pins the whole mapping in one run: what the
// agent says becomes the answer, what it runs becomes tool steps, and how it
// ends becomes the terminal event.
//
// The three details worth keeping are the ones a rewrite is most likely to
// lose: a tool result whose call this turn never saw is dropped rather than
// invented, a failed tool result is reported as a failure and not as a step that
// succeeded, and the init event's session id is the conversation's own ID —
// which is what the caller has to persist to resume it.
func TestCLISessionTranslatesEvents(t *testing.T) {
	skipWithoutShell(t)
	bin := writeScript(t, `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-cli-1"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"reading it"},{"type":"tool_use","id":"tu-1","name":"Read","input":{"file_path":"/tmp/a"}}]}}'
printf '%s\n' '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu-1","content":"hello"}]}}'
printf '%s\n' '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu-gone","content":"a replay of a call this turn never saw"}]}}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu-2","name":"Write","input":{"file_path":"/tmp/a"}}]}}'
printf '%s\n' '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu-2","is_error":true,"content":"permission denied"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","result":"the file says hello"}'
`)

	a := newTestAgent(t, Options{Bin: bin, WorkspaceRoot: t.TempDir(), MaxSessions: 2})
	conv := sessionFor(t, a, "conv-translate")

	turn := runTurn(t, conv, agent.AskRequest{Question: "read it", RunID: "run-1"})
	if err := turn.wait(t); err != nil {
		t.Fatalf("AskStream: %v", err)
	}

	got := turn.rest()
	want := []agent.Event{
		{Type: agent.EventAnswer, Delta: "reading it"},
		{Type: agent.EventToolStep, Steps: []agent.ToolStep{{Name: "Read", Status: "ok"}}},
		{Type: agent.EventToolStep, Steps: []agent.ToolStep{{Name: "Write", Status: "fail"}}},
		{Type: agent.EventDone, Final: "the file says hello"},
	}
	if len(got) != len(want) {
		t.Fatalf("the turn reported %d events (%+v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i].Type != want[i].Type {
			t.Fatalf("event %d is %d, want %d (%+v)", i, got[i].Type, want[i].Type, got)
		}
		switch want[i].Type {
		case agent.EventAnswer:
			if got[i].Delta != want[i].Delta {
				t.Errorf("answer delta = %q, want %q", got[i].Delta, want[i].Delta)
			}
		case agent.EventToolStep:
			if len(got[i].Steps) != 1 || got[i].Steps[0] != want[i].Steps[0] {
				t.Errorf("step %d = %+v, want %+v", i, got[i].Steps, want[i].Steps)
			}
		case agent.EventDone:
			if got[i].Final != want[i].Final {
				t.Errorf("final answer = %q, want %q", got[i].Final, want[i].Final)
			}
		}
	}

	if got := conv.ID(); got != "sess-cli-1" {
		t.Errorf("ID() = %q, want the id the init event handed out", got)
	}
}

// TestCLISessionReportsThinkingOnlyWhenRequested pins the CLI backend's half of
// the reasoning lane: a "thinking" block becomes a reasoning event, it never
// joins the answer, and a conversation that turned reasoning off gets none.
//
// The gate matters beyond tidiness — a caller that switched reasoning off built
// a card with no reasoning element, so an update aimed at it would fail the call
// that carries the answer text too.
func TestCLISessionReportsThinkingOnlyWhenRequested(t *testing.T) {
	skipWithoutShell(t)

	for _, tc := range []struct {
		name      string
		reasoning bool
		want      []agent.Event
	}{
		{
			name:      "on",
			reasoning: true,
			want: []agent.Event{
				{Type: agent.EventReasoning, Delta: "先看十分位"},
				{Type: agent.EventAnswer, Delta: "9.9 更大"},
				{Type: agent.EventDone, Final: "9.9 更大"},
			},
		},
		{
			name:      "off",
			reasoning: false,
			want: []agent.Event{
				{Type: agent.EventAnswer, Delta: "9.9 更大"},
				{Type: agent.EventDone, Final: "9.9 更大"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := writeScript(t, `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-cli-think"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"先看十分位","signature":"sig"},{"type":"text","text":"9.9 更大"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","result":"9.9 更大"}'
`)

			a := newTestAgent(t, Options{Bin: bin, WorkspaceRoot: t.TempDir(), MaxSessions: 2})
			conv := sessionFor(t, a, "conv-thinking-"+tc.name)

			turn := runTurn(t, conv, agent.AskRequest{
				Question: "9.11 和 9.9 哪个大", RunID: "run-think-" + tc.name, Reasoning: tc.reasoning,
			})
			if err := turn.wait(t); err != nil {
				t.Fatalf("AskStream: %v", err)
			}

			got := turn.rest()
			if len(got) != len(tc.want) {
				t.Fatalf("the turn reported %d events (%+v), want %d", len(got), got, len(tc.want))
			}
			for i := range tc.want {
				if got[i].Type != tc.want[i].Type || got[i].Delta != tc.want[i].Delta ||
					got[i].Final != tc.want[i].Final {
					t.Fatalf("event %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestCLISessionRespondPermissionRoundTrip drives the handshake the way it
// actually happens: the turn runs on one request, and the answer arrives on
// another — here, as a second Session lookup for the same key, which is exactly
// what the card callback does. A session that answered from inside its own turn
// would prove nothing about that.
//
// The bytes the agent reads are asserted, not just the fact that it continued:
// a wrong envelope or a decision that did not travel would unblock the run just
// as well, and would run the tool the user refused.
func TestCLISessionRespondPermissionRoundTrip(t *testing.T) {
	skipWithoutShell(t)

	for _, tc := range []struct {
		name         string
		decision     string
		resume       string
		wantBehavior string
		wantMessage  string
	}{
		{name: "allow", decision: agent.DecisionAllow, wantBehavior: "allow"},
		{name: "deny", decision: agent.DecisionDeny, resume: "这条命令不许执行",
			wantBehavior: "deny", wantMessage: "这条命令不许执行"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := writeScript(t, `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-cli-2"}'
printf '%s\n' '{"type":"control_request","request_id":"req-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -rf /tmp/one"},"tool_use_id":"tu-1"}}'
IFS= read -r answer
printf '%s\n' "$answer" > "$PWD/answer.json"
printf '%s\n' '{"type":"result","subtype":"success","result":"ran it"}'
`)

			root := t.TempDir()
			a := newTestAgent(t, Options{Bin: bin, WorkspaceRoot: root, MaxSessions: 2})
			key := "conv-permission"
			workdir, err := agentcli.SessionWorkspace(root, key)
			if err != nil {
				t.Fatalf("SessionWorkspace: %v", err)
			}
			conv := sessionFor(t, a, key)

			turn := runTurn(t, conv, agent.AskRequest{Question: "clean it up", RunID: "run-7"})
			ev := turn.next(t)
			if ev.Type != agent.EventInterrupted {
				t.Fatalf("event = %d, want an interrupt (%+v)", ev.Type, ev)
			}
			if ev.Interrupt == nil {
				t.Fatal("the interrupt arrived with nothing in it")
			}
			reqs := ev.Interrupt.Permissions()
			if len(reqs) != 1 {
				t.Fatalf("the interrupt carries %d approval requests, want one", len(reqs))
			}
			if reqs[0].Kind != agent.InterruptKindPermission {
				t.Errorf("kind = %q, want %q", reqs[0].Kind, agent.InterruptKindPermission)
			}
			if reqs[0].ToolName != "Bash" {
				t.Errorf("tool = %q, want Bash", reqs[0].ToolName)
			}
			// This backend's pause carries no address: its request ids identify a
			// control request rather than anything the caller could name back, so
			// the answer travels as text and an empty address is the contract
			// rather than a gap.
			if reqs[0].Address != "" {
				t.Errorf("address = %q, want none on the CLI backend", reqs[0].Address)
			}
			// The card asks a human to approve something; without the arguments it
			// would be asking them to approve a tool name they cannot judge.
			if !strings.Contains(reqs[0].Question, "rm -rf /tmp/one") {
				t.Errorf("question = %q, want it to carry the tool's arguments", reqs[0].Question)
			}

			// The answer arrives on its own request. Asking for the conversation
			// again has to hand back the same one, or the answer would address a
			// session that is not the one blocked.
			answering, err := a.Session(t.Context(), key)
			if err != nil {
				t.Fatalf("Session: %v", err)
			}
			if answering != agent.Session(conv) {
				t.Fatal("a second request for the same conversation got a different one")
			}

			// An answer addressed to another run must be refused, and must leave the
			// real one still waiting: a stale card button is the case this guards.
			if err := answering.AnswerPermission(t.Context(), answer("run-not-this", tc.decision, tc.resume)); err == nil {
				t.Fatal("an answer addressed to another run was accepted")
			} else if !strings.Contains(err.Error(), "not the one in flight") {
				t.Errorf("error = %v, want it to name the run mismatch", err)
			}

			if err := answering.AnswerPermission(t.Context(), answer("run-7", tc.decision, tc.resume)); err != nil {
				t.Fatalf("AnswerPermission: %v", err)
			}
			if err := turn.wait(t); err != nil {
				t.Fatalf("AskStream: %v", err)
			}
			if rest := turn.rest(); len(rest) != 1 || rest[0].Type != agent.EventDone {
				t.Fatalf("the turn ended with %+v, want it to continue past the approval", rest)
			}

			answer := readAnswer(t, filepath.Join(workdir, "answer.json"))
			if answer.Type != "control_response" || answer.Response.Subtype != "success" {
				t.Errorf("envelope = %+v, want a successful control_response", answer)
			}
			if answer.Response.RequestID != "req-1" {
				t.Errorf("request_id = %q, want it to echo req-1", answer.Response.RequestID)
			}
			if got := answer.Response.Response.Behavior; got != tc.wantBehavior {
				t.Errorf("behavior = %q, want %q", got, tc.wantBehavior)
			}
			if got := answer.Response.Response.Message; got != tc.wantMessage {
				t.Errorf("message = %q, want %q", got, tc.wantMessage)
			}
		})
	}
}

// TestCLISessionRemembersAllowAll pins the promise behind "总是允许" (always allow): the CLI's
// protocol has no such answer, so the host keeps it. The second request for that
// tool must never reach the user — an approval card that reappeared after they
// said "always" is the whole feature failing.
func TestCLISessionRemembersAllowAll(t *testing.T) {
	skipWithoutShell(t)
	bin := writeScript(t, `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-cli-3"}'
printf '%s\n' '{"type":"control_request","request_id":"req-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"tu-1"}}'
IFS= read -r first
printf '%s\n' "$first" >> "$PWD/answers.jsonl"
printf '%s\n' '{"type":"control_request","request_id":"req-2","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls -l"},"tool_use_id":"tu-2"}}'
IFS= read -r second
printf '%s\n' "$second" >> "$PWD/answers.jsonl"
printf '%s\n' '{"type":"result","subtype":"success","result":"listed it"}'
`)

	root := t.TempDir()
	a := newTestAgent(t, Options{Bin: bin, WorkspaceRoot: root, MaxSessions: 2})
	key := "conv-allow-all"
	workdir, err := agentcli.SessionWorkspace(root, key)
	if err != nil {
		t.Fatalf("SessionWorkspace: %v", err)
	}
	conv := sessionFor(t, a, key)

	turn := runTurn(t, conv, agent.AskRequest{Question: "list it", RunID: "run-2"})
	ev := turn.next(t)
	if ev.Type != agent.EventInterrupted {
		t.Fatalf("event = %d, want the first approval request to reach the user", ev.Type)
	}
	if err := conv.AnswerPermission(t.Context(), answer("run-2", agent.DecisionAllowAll, "")); err != nil {
		t.Fatalf("AnswerPermission: %v", err)
	}

	if err := turn.wait(t); err != nil {
		t.Fatalf("AskStream: %v", err)
	}
	for _, ev := range turn.rest() {
		if ev.Type == agent.EventInterrupted {
			t.Fatal("the second call asked again after the user said always allow")
		}
	}

	raw, err := os.ReadFile(filepath.Join(workdir, "answers.jsonl"))
	if err != nil {
		t.Fatalf("the agent read nothing back: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("the agent read %d answers, want 2: %q", len(lines), raw)
	}
	for i, want := range []string{"req-1", "req-2"} {
		var answer controlAnswer
		if err := json.Unmarshal([]byte(lines[i]), &answer); err != nil {
			t.Fatalf("answer %d is not the response envelope: %v (%q)", i, err, lines[i])
		}
		if answer.Response.RequestID != want {
			t.Errorf("answer %d echoes %q, want %q", i, answer.Response.RequestID, want)
		}
		if got := answer.Response.Response.Behavior; got != "allow" {
			t.Errorf("answer %d says %q, want allow", i, got)
		}
	}
}

// TestCLISessionResumesAndRunsInItsOwnWorkspace pins the two things that make a
// conversation outlive one process: it runs in a directory of its own under the
// workspace root, and the next turn continues the agent's session rather than
// starting over. The snapshot the caller passes wins when there is one, because
// it is the only copy that survives a restart.
func TestCLISessionResumesAndRunsInItsOwnWorkspace(t *testing.T) {
	skipWithoutShell(t)
	// The file lands in the directory the process was started in, which is how the
	// test sees both the workspace and the argv without trusting either.
	bin := writeScript(t, `#!/bin/sh
printf '%s\n' "$@" > "$PWD/argv.txt"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-cli-4"}'
printf '%s\n' '{"type":"result","subtype":"success","result":"ok"}'
`)

	root := t.TempDir()
	a := newTestAgent(t, Options{Bin: bin, WorkspaceRoot: root, MaxSessions: 2})
	key := "conv-resume"
	workdir, err := agentcli.SessionWorkspace(root, key)
	if err != nil {
		t.Fatalf("SessionWorkspace: %v", err)
	}
	if workdir == root || !strings.HasPrefix(workdir, root) {
		t.Fatalf("workspace %q is not a subdirectory of the root %q", workdir, root)
	}
	conv := sessionFor(t, a, key)

	first := runTurn(t, conv, agent.AskRequest{Question: "hello", RunID: "run-a", Resume: "snap-9"})
	if err := first.wait(t); err != nil {
		t.Fatalf("AskStream: %v", err)
	}
	argv := readArgv(t, filepath.Join(workdir, "argv.txt"))
	if !hasFlagPair(argv, "--resume", "snap-9") {
		t.Errorf("argv = %q, want it to resume from the conversation's snapshot", argv)
	}

	// Turn two carries no snapshot, so it continues the agent's own session — the
	// one its init event reported — rather than starting a new conversation.
	second := runTurn(t, conv, agent.AskRequest{Question: "again", RunID: "run-b"})
	if err := second.wait(t); err != nil {
		t.Fatalf("AskStream: %v", err)
	}
	argv = readArgv(t, filepath.Join(workdir, "argv.txt"))
	if !hasFlagPair(argv, "--resume", "sess-cli-4") {
		t.Errorf("argv = %q, want the second turn to resume sess-cli-4", argv)
	}
}

// TestCLISessionRunsInTheRequestedDirectory pins the /dir path end to end: the
// directory a conversation asked for is the directory its process is started in.
//
// The requested directory is the deployment's extra allowed root — the knob
// exists so that a conversation can be pointed at a checkout that already lives
// somewhere, rather than the operator having to move it under the workspace root.
func TestCLISessionRunsInTheRequestedDirectory(t *testing.T) {
	skipWithoutShell(t)
	// The agent records its argv in the directory it was started in, so the file
	// appearing there is the proof of where it ran.
	bin := writeScript(t, `#!/bin/sh
printf '%s\n' "$@" > "$PWD/argv.txt"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-cli-6"}'
printf '%s\n' '{"type":"result","subtype":"success","result":"ok"}'
`)

	root := t.TempDir()
	extra := t.TempDir()
	a := newTestAgent(t, Options{
		Bin:           bin,
		WorkspaceRoot: root,
		AllowedDirs:   []string{extra},
		MaxSessions:   2,
	})

	roots := a.AllowedRoots()
	if len(roots) != 2 {
		t.Fatalf("AllowedRoots() = %v, want the workspace root and the extra directory", roots)
	}
	if roots[0] != root {
		t.Errorf("AllowedRoots()[0] = %q, want the workspace root %q first", roots[0], root)
	}

	conv := sessionFor(t, a, "conv-dir")
	turn := runTurn(t, conv, agent.AskRequest{Question: "where am I", RunID: "run-1", WorkDir: extra})
	if err := turn.wait(t); err != nil {
		t.Fatalf("AskStream: %v", err)
	}
	// readArgv fails the test if the file is not there, which is the claim: the
	// process was started in the requested directory and not in the session's own.
	readArgv(t, filepath.Join(extra, "argv.txt"))

	// The backend does not remember the directory: the caller passes it with each
	// turn, which is what lets the setting live in the conversation's snapshot
	// rather than in a process that a restart would lose.
	own, err := agentcli.SessionWorkspace(root, "conv-dir")
	if err != nil {
		t.Fatalf("SessionWorkspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(own, "argv.txt")); err == nil {
		t.Errorf("the first turn also ran in the session's own workspace %q", own)
	}

	second := runTurn(t, conv, agent.AskRequest{Question: "and now", RunID: "run-2"})
	if err := second.wait(t); err != nil {
		t.Fatalf("AskStream: %v", err)
	}
	readArgv(t, filepath.Join(own, "argv.txt"))
}

// TestCLISessionRefusesAWorkDirItWasNotGiven pins that the backend is the
// authority on the confinement, not its caller. Whoever accepted the directory —
// a command, a snapshot written before the configuration changed, an HTTP client
// — may be out of date or simply wrong, and this is the last check before a
// process is started.
func TestCLISessionRefusesAWorkDirItWasNotGiven(t *testing.T) {
	skipWithoutShell(t)
	bin := writeScript(t, `#!/bin/sh
printf '%s\n' "$@" > "$PWD/argv.txt"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-cli-7"}'
printf '%s\n' '{"type":"result","subtype":"success","result":"ok"}'
`)

	root := t.TempDir()
	extra := t.TempDir()
	file := filepath.Join(extra, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	a := newTestAgent(t, Options{
		Bin:           bin,
		WorkspaceRoot: root,
		AllowedDirs:   []string{extra},
		MaxSessions:   2,
	})
	conv := sessionFor(t, a, "conv-dir-refused")

	for _, tc := range []struct{ name, dir string }{
		{name: "a directory outside every root", dir: t.TempDir()},
		{name: "a sibling whose name starts with the root", dir: root + "-sibling"},
		{name: "a directory under a root that does not exist", dir: filepath.Join(extra, "gone")},
		{name: "a file inside an allowed root", dir: file},
		{name: "no directory at all", dir: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := conv.AskStream(t.Context(),
				agent.AskRequest{Question: "hi", RunID: "run-x", WorkDir: tc.dir},
				func(agent.Event) error { return nil })
			if tc.dir == "" {
				// An empty request is the default, not a refusal: it means the
				// conversation asked for nothing and runs in its own workspace.
				if err != nil {
					t.Fatalf("AskStream with no directory = %v, want the session's own workspace", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("AskStream ran in %q, want it refused", tc.dir)
			}
			if !strings.Contains(err.Error(), tc.dir) {
				t.Errorf("error = %v, want it to name the refused directory", err)
			}
		})
	}

	// A refused directory is refused before the conversation is claimed, so the
	// next turn is not told the previous one is still running.
	turn := runTurn(t, conv, agent.AskRequest{Question: "hello", RunID: "run-ok"})
	if err := turn.wait(t); err != nil {
		t.Fatalf("a turn after the refusals: %v", err)
	}
}

// TestCLISessionStopEndsTheTurnAsStopped pins that pressing stop reads as a stop.
// The process is killed either way; what is being tested is that the turn reports
// the end the caller asked for, because that is the difference between a card
// that says "已停止" (stopped) and one that says the agent is broken.
func TestCLISessionStopEndsTheTurnAsStopped(t *testing.T) {
	skipWithoutShell(t)
	bin := writeScript(t, `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-cli-5"}'
printf '%s\n' '{"type":"control_request","request_id":"req-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"sleep 300"},"tool_use_id":"tu-1"}}'
IFS= read -r answer
printf '%s\n' '{"type":"result","subtype":"success","result":"finished after all"}'
`)

	a := newTestAgent(t, Options{Bin: bin, WorkspaceRoot: t.TempDir(), MaxSessions: 2})
	conv := sessionFor(t, a, "conv-stop")

	turn := runTurn(t, conv, agent.AskRequest{Question: "wait", RunID: "run-3"})
	if ev := turn.next(t); ev.Type != agent.EventInterrupted {
		t.Fatalf("event = %d, want the approval request before the stop", ev.Type)
	}

	if err := conv.Stop(t.Context(), "run-other"); err == nil {
		t.Fatal("a stop addressed to another run was accepted")
	}
	if err := conv.Stop(t.Context(), "run-3"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := turn.wait(t); err != nil {
		t.Fatalf("AskStream: %v, want a stop to end the turn cleanly", err)
	}

	got := turn.rest()
	if len(got) != 1 || got[0].Type != agent.EventStopped {
		t.Fatalf("the stopped turn reported %+v, want exactly one stopped event", got)
	}
	for _, ev := range got {
		if ev.Type == agent.EventFailed {
			t.Error("the stopped turn reported a failure")
		}
	}

	// Once the turn is over there is nothing left to answer: a stale card button
	// must not be able to unblock whatever runs next.
	if err := conv.AnswerPermission(t.Context(), answer("run-3", agent.DecisionAllow, "")); err == nil {
		t.Error("a permission was accepted after the run it belonged to had ended")
	}
}

// --- helpers ---

// answer builds an answer to a CLI pause. This backend addresses nothing, so a
// text answer travels in the map under the empty key: the map is then only a
// container, and the backend reads the answer as text (AnswerText) rather than
// looking anything up in it.
func answer(runID, decision, text string) agent.AnswerRequest {
	req := agent.AnswerRequest{RunID: runID, Decision: decision}
	if text != "" {
		req.Answers = map[string]string{"": text}
	}
	return req
}

// liveTurn is a turn running in the background, reporting its events as they
// arrive. A turn has to be driven this way to be tested at all: the calls that
// answer and stop it come from outside it, and a turn that answered itself would
// not exercise the path that matters.
type liveTurn struct {
	events chan agent.Event
	done   chan error
}

func runTurn(t *testing.T, conv agent.Session, req agent.AskRequest) *liveTurn {
	t.Helper()
	lt := &liveTurn{events: make(chan agent.Event, 64), done: make(chan error, 1)}
	go func() {
		lt.done <- conv.AskStream(t.Context(), req, func(ev agent.Event) error {
			lt.events <- ev
			return nil
		})
	}()
	return lt
}

// next returns the next event, failing the test if the turn ends or stalls
// before it arrives.
func (lt *liveTurn) next(t *testing.T) agent.Event {
	t.Helper()
	select {
	case ev := <-lt.events:
		return ev
	case err := <-lt.done:
		t.Fatalf("the turn ended before the expected event (err = %v)", err)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for the turn to report an event")
	}
	return agent.Event{}
}

// wait blocks until the turn is over and returns how it ended.
func (lt *liveTurn) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-lt.done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for the turn to end")
		return nil
	}
}

// rest returns the events the turn reported after the ones already read. It is
// only meaningful once wait has returned: AskStream hands back its outcome after
// its last event, so by then everything it emitted is already buffered.
func (lt *liveTurn) rest() []agent.Event {
	var got []agent.Event
	for {
		select {
		case ev := <-lt.events:
			got = append(got, ev)
		default:
			return got
		}
	}
}

// newTestAgent builds a CLI backend whose agents are the caller's fake. The
// workspace root is always a temporary directory: a test that let an agent run
// where the developer's shell was would be a test that writes into the checkout.
func newTestAgent(t *testing.T, opts Options) *Agent {
	t.Helper()
	a, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func sessionFor(t *testing.T, a *Agent, key string) agent.Session {
	t.Helper()
	conv, err := a.Session(context.Background(), key)
	if err != nil {
		t.Fatalf("Session(%q): %v", key, err)
	}
	return conv
}

// readArgv reads the argv a fake agent recorded, refusing an empty record: the
// file not existing means the agent never ran where the test expected it to.
func readArgv(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the agent left no argv at %s: %v", path, err)
	}
	var argv []string
	for line := range strings.SplitSeq(strings.TrimRight(string(raw), "\n"), "\n") {
		argv = append(argv, line)
	}
	return argv
}

// readAnswer decodes the control_response a fake agent read back.
func readAnswer(t *testing.T, path string) controlAnswer {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the agent read nothing back: %v", err)
	}
	var answer controlAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatalf("the agent read %q, which is not the response envelope: %v", raw, err)
	}
	return answer
}

// controlAnswer is the envelope the CLI reads an answer back in: the decision
// travels nested under "response", matching the request it echoes.
type controlAnswer struct {
	Type     string `json:"type"`
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Response  struct {
			Behavior string `json:"behavior"`
			Message  string `json:"message"`
		} `json:"response"`
	} `json:"response"`
}

// hasFlagPair reports whether args carries a two-element flag. Matching the name
// alone would pass on argv that dropped the value, which for --resume is the
// difference between continuing a conversation and starting a new one.
func hasFlagPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// writeScript writes a fake agent and returns its path.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-agent")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write the fake agent: %v", err)
	}
	return path
}

// skipWithoutShell skips the tests that need a POSIX shell to stand in for the
// agent. The adapter's whole job is the process handshake and the permission
// round trip, and faking those in-process would test the fake.
func skipWithoutShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a /bin/sh script")
	}
}
