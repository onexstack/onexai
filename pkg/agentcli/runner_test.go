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

package agentcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDefaultPermissionModeIsStdio(t *testing.T) {
	// The default is the safe one. A default of "skip" is what the two existing
	// callers did, and it is the wrong default for a runtime that a chat message
	// can reach: it turns "answer in a group chat" into "run on the host".
	opts := Options{}
	if err := opts.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if opts.PermissionMode != PermissionStdio {
		t.Fatalf("a zero Options chose permission mode %q, want %q", opts.PermissionMode, PermissionStdio)
	}

	args := opts.args("do the thing")
	if !hasFlagPair(args, "--permission-prompt-tool", "stdio") {
		t.Errorf("argv %q does not ask over the control channel", args)
	}
	if slices.Contains(args, "--dangerously-skip-permissions") {
		t.Errorf("argv %q skips permissions by default", args)
	}
}

func TestSkipPermissionModeIsExplicit(t *testing.T) {
	opts := Options{PermissionMode: PermissionSkip}
	if err := opts.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	args := opts.args("do the thing")
	if !slices.Contains(args, "--dangerously-skip-permissions") {
		t.Errorf("argv %q does not carry the skip flag it was asked for", args)
	}
	if slices.Contains(args, "--permission-prompt-tool") {
		t.Errorf("argv %q both skips and asks: %q", args, PermissionSkip)
	}

	// An unknown mode is a mistake that must not resolve to either behaviour.
	bad := Options{PermissionMode: "yolo"}
	if err := bad.normalize(); err == nil {
		t.Error("an unknown permission mode must be rejected, not silently defaulted")
	}
	if _, err := New(Options{Bin: "claude", PermissionMode: "yolo"}); err == nil {
		t.Error("New must reject an unknown permission mode before a process is started")
	}
}

func TestSkipModeKeepsPromptOutOfArgv(t *testing.T) {
	opts := Options{PermissionMode: PermissionSkip}
	if err := opts.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	const prompt = "SECRET-CONVENTION-TEXT"
	args := opts.args(prompt)
	if !slices.Contains(args, "-p") {
		t.Fatalf("argv %q is missing -p", args)
	}
	for _, arg := range args {
		if strings.Contains(arg, prompt) {
			t.Fatalf("prompt leaked into argv: %q", args)
		}
	}
}

func TestArgsCarryTheSessionOptions(t *testing.T) {
	opts := Options{
		Resume:             "sess-abc",
		Model:              "opus",
		AppendSystemPrompt: "be terse",
		AllowedTools:       []string{"Read", "Grep"},
		DisallowedTools:    []string{"Bash"},
	}
	if err := opts.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	args := opts.args("look around")

	// --verbose is not optional: stream-json requires it, and leaving it out is
	// the difference between a stream and a single blob at the end.
	if !slices.Contains(args, "--verbose") {
		t.Errorf("argv %q is not verbose, which stream-json requires", args)
	}
	if !hasFlagPair(args, "--output-format", "stream-json") {
		t.Errorf("argv %q does not ask for stream-json", args)
	}
	if !hasFlagPair(args, "-p", "look around") {
		t.Errorf("argv %q does not carry the prompt", args)
	}
	if !hasFlagPair(args, "--resume", "sess-abc") {
		t.Errorf("argv %q does not resume the session", args)
	}
	if !hasFlagPair(args, "--model", "opus") {
		t.Errorf("argv %q does not carry the model", args)
	}
	if !hasFlagPair(args, "--append-system-prompt", "be terse") {
		t.Errorf("argv %q does not carry the system prompt", args)
	}
	if !hasFlagPair(args, "--allowedTools", "Read,Grep") {
		t.Errorf("argv %q does not narrow the allowed tools", args)
	}
	if !hasFlagPair(args, "--disallowedTools", "Bash") {
		t.Errorf("argv %q does not carry the disallowed tools", args)
	}

	// A run with nothing set must not invent flags.
	plain := Options{}
	if err := plain.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	for _, flag := range []string{"--resume", "--model", "--allowedTools", "--disallowedTools", "--append-system-prompt"} {
		if slices.Contains(plain.args("x"), flag) {
			t.Errorf("an unset %s still reached argv: %q", flag, plain.args("x"))
		}
	}
}

func TestRunStreamsEventsAndReportsTheResult(t *testing.T) {
	skipWithoutShell(t)
	bin := writeScript(t, `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-1","tools":["Read"]}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"looking"}]}}'
printf '%s\n' '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu-1","content":"a b c"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","result":"the answer","duration_ms":42,"usage":{"input_tokens":5,"output_tokens":2}}'
printf '%s\n' 'warning: something' >&2
`)

	r, err := New(Options{Bin: bin, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close()

	res, err := drain(t, r, "prompt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1 (it is what --resume takes)", res.SessionID)
	}
	if res.Answer != "the answer" {
		t.Errorf("Answer = %q, want the result event's text", res.Answer)
	}
	if res.Usage.OutputTokens != 2 || res.Usage.Input() != 5 {
		t.Errorf("Usage = %+v, want the result event's accounting", res.Usage)
	}
	if res.DurationMS != 42 {
		t.Errorf("DurationMS = %d, want 42", res.DurationMS)
	}
	if res.IsError {
		t.Error("IsError must follow the result event")
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	// stderr is kept for the failure message. A run that succeeded should still
	// have recorded what it said, so a later failure can be read in context.
	if !strings.Contains(res.Stderr, "warning: something") {
		t.Errorf("Stderr = %q, want the agent's stderr", res.Stderr)
	}
}

func TestRunHandsEveryLineToOnLine(t *testing.T) {
	skipWithoutShell(t)
	bin := writeScript(t, `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-1"}'
printf '%s\n' '{"type":"telemetry","payload":{"ms":3}}'
printf '%s\n' '{"type":"result","subtype":"success","result":"done"}'
`)

	var mu sync.Mutex
	var seen []string
	r, err := New(Options{
		Bin:     bin,
		Timeout: 30 * time.Second,
		// OnLine runs in Run's goroutine while this test's is draining the event
		// channel, so the transcript needs a lock of its own.
		OnLine: func(line string) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, line)
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close()

	if _, err := drain(t, r, "prompt"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Three lines written, three lines transcribed — including the one this
	// package has no use for.
	if len(seen) != 3 {
		t.Fatalf("OnLine saw %d lines, want 3: %q", len(seen), seen)
	}
	if !strings.Contains(seen[1], "telemetry") {
		t.Errorf("line 1 = %q, want the line Decode ignores: it still belongs in the transcript", seen[1])
	}
}

func TestRunReportsAFailureWithTheAgentsOwnReason(t *testing.T) {
	skipWithoutShell(t)
	bin := writeScript(t, `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-2"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"starting"}]}}'
printf '%s\n' 'error: the model refused' >&2
exit 3
`)

	r, err := New(Options{Bin: bin, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close()

	res, err := drain(t, r, "prompt")
	if err == nil {
		t.Fatal("a non-zero exit must be an error")
	}
	if !strings.Contains(err.Error(), "the model refused") {
		t.Errorf("error = %v, want the agent's stderr in it: that is the only explanation there is", err)
	}
	// The result is still populated. A turn that died mid-answer still has a
	// session to resume and something to show.
	if res.SessionID != "sess-2" {
		t.Errorf("SessionID = %q, want the init event's session even on failure", res.SessionID)
	}
	if res.Answer != "starting" {
		t.Errorf("Answer = %q, want what the agent managed to say", res.Answer)
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
}

func TestRunAnswersAPermissionRequest(t *testing.T) {
	skipWithoutShell(t)
	// The script blocks the way the real agent does: it asks, then reads exactly
	// one line from stdin before it can continue. If RespondPermission writes
	// anything the agent cannot read, this run never finishes.
	answerFile := filepath.Join(t.TempDir(), "answer.json")
	bin := writeScript(t, `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-3"}'
printf '%s\n' '{"type":"control_request","request_id":"req-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -rf /tmp/one"},"tool_use_id":"tu-1"}}'
IFS= read -r answer
printf '%s' "$answer" > "$ANSWER_FILE"
printf '%s\n' '{"type":"result","subtype":"success","result":"ran it"}'
`)

	r, err := New(Options{Bin: bin, Env: []string{"ANSWER_FILE=" + answerFile}, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close()

	ch := make(chan Event, 8)
	type runResult struct {
		res Result
		err error
	}
	done := make(chan runResult, 1)
	go func() {
		res, err := r.Run(t.Context(), "prompt", ch)
		done <- runResult{res, err}
	}()

	var asked *PermissionRequest
	for ev := range ch {
		if ev.Control == nil {
			continue
		}
		asked = ev.Control
		if err := r.RespondPermission(t.Context(), ev.Control.RequestID, PermissionResult{Allow: true}); err != nil {
			t.Errorf("RespondPermission: %v", err)
		}
	}
	got := <-done
	if got.err != nil {
		t.Fatalf("Run: %v", got.err)
	}

	if asked == nil {
		t.Fatal("the agent asked for permission and the request never arrived")
	}
	if asked.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want Bash", asked.ToolName)
	}
	if got.res.Answer != "ran it" {
		t.Errorf("Answer = %q, want the agent to have continued past the approval", got.res.Answer)
	}

	// What the agent read is the whole point, so assert the bytes rather than
	// trusting that they were written.
	raw, err := os.ReadFile(answerFile)
	if err != nil {
		t.Fatalf("the agent read nothing back: %v", err)
	}
	var answer struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Response  struct {
				Behavior string `json:"behavior"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatalf("the agent read %q, which is not the response envelope: %v", raw, err)
	}
	if answer.Type != "control_response" || answer.Response.Subtype != "success" {
		t.Errorf("envelope = %+v, want a successful control_response", answer)
	}
	if answer.Response.RequestID != "req-1" {
		t.Errorf("request_id = %q, want it to echo req-1", answer.Response.RequestID)
	}
	if answer.Response.Response.Behavior != "allow" {
		t.Errorf("behavior = %q, want allow", answer.Response.Response.Behavior)
	}
}

func TestRunCancelsAWedgedAgent(t *testing.T) {
	skipWithoutShell(t)
	bin := writeScript(t, `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-4"}'
# Then wedge, for far longer than the test is willing to wait.
sleep 300
`)

	// The timeout is what a wedged agent runs into. Without it the run would
	// outlive the caller with a process still holding the session.
	r, err := New(Options{Bin: bin, Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close()

	start := time.Now()
	if _, err := drain(t, r, "prompt"); err == nil {
		t.Fatal("a run that hit its timeout must report an error")
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("the run took %s to give up, want it bounded by the timeout", elapsed)
	}
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	b := newTailBuffer(8)
	if _, err := b.Write([]byte("1234567890")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got, want := b.String(), "34567890"; got != want {
		t.Errorf("String() = %q, want %q: the reason a run failed is in the last lines", got, want)
	}
	// Written in pieces, the tail is still the tail.
	b = newTailBuffer(4)
	for _, chunk := range []string{"ab", "cd", "ef"} {
		if _, err := b.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if got, want := b.String(), "cdef"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if got := newTailBuffer(8).String(); got != "" {
		t.Errorf("an empty buffer reads as %q, want empty", got)
	}
}

// drain runs r to completion, discarding the events, and returns its result.
func drain(t *testing.T, r *Runner, prompt string) (Result, error) {
	t.Helper()
	ch := make(chan Event, 16)
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	res, err := r.Run(t.Context(), prompt, ch)
	<-done
	return res, err
}

// writeScript writes an executable /bin/sh script and returns its path.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-agent")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write the fake agent: %v", err)
	}
	return path
}

// skipWithoutShell skips the tests that need a POSIX shell to stand in for the
// agent. Faking the process any other way would test the fake instead of the
// pipes, the scanner and the stdin handshake, which is exactly what is at stake.
func skipWithoutShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a /bin/sh script")
	}
}

// hasFlagPair reports whether args carries a two-element flag, which is how a
// value-bearing flag has to be read: matching the name alone would pass on argv
// that dropped its value.
func hasFlagPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
