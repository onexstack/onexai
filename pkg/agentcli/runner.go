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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Permission modes. The default is stdio — the agent asks, and the host answers
// through RespondPermission.
const (
	// PermissionStdio makes the agent stop before any tool call that needs
	// approval and ask over the control channel instead of guessing. A host that
	// does not answer leaves the run blocked, which is the point: the alternative
	// is running the tool unasked.
	PermissionStdio = "stdio"
	// PermissionSkip adds --dangerously-skip-permissions, so every tool runs
	// unasked. It is only for unattended pipelines that have already decided the
	// work is safe to do without a human: nothing else in this package can make
	// the decision not to ask.
	PermissionSkip = "skip"
)

const (
	// DefaultBin is the executable driven by default.
	DefaultBin = "claude"
	// MaxLineBytes is the default scanner limit for one stream-json line.
	//
	// It is deliberately far above any normal event: a single tool_result for a
	// large file, or the JSON of a `kubectl get -o json`, is one line, and a
	// scanner limit a real command can exceed does not fail gracefully — it
	// abandons the stream and kills the session mid-answer.
	MaxLineBytes = 8 << 20
	// DefaultTimeout bounds one run when the caller sets none. It is a backstop
	// against a wedged process, not a budget: callers that know how long their
	// work should take should say so.
	DefaultTimeout = 30 * time.Minute
)

// Options configures a Runner. The zero value is usable: it drives "claude" in
// the caller's working directory, asks before running tools that need approval,
// and gives up after DefaultTimeout.
type Options struct {
	// Bin is the agent executable. Empty means DefaultBin.
	Bin string
	// WorkDir is the directory the agent runs in, which is also the tree its
	// relative paths mean. Empty inherits the caller's working directory.
	WorkDir string
	// Resume continues a previous session by its ID (event init's SessionID).
	Resume string
	// PermissionMode is PermissionStdio (the default) or PermissionSkip.
	PermissionMode string
	// AllowedTools and DisallowedTools narrow what the agent may run at all,
	// independently of whether it asks. An empty list means no restriction here.
	AllowedTools    []string
	DisallowedTools []string
	// Model overrides the model the agent uses.
	Model string
	// AppendSystemPrompt adds to the agent's system prompt.
	AppendSystemPrompt string
	// Timeout bounds the run. Zero means DefaultTimeout; a negative value
	// disables the bound and leaves the run to the caller's context.
	Timeout time.Duration
	// MaxLineBytes overrides MaxLineBytes. Zero means the default.
	MaxLineBytes int
	// Env is appended to the inherited environment.
	Env []string
	// Stderr, when set, receives the agent's stderr as it is produced. Whatever
	// is written is also kept in Result.Stderr, so a caller that only wants the
	// failure reason can leave this unset.
	Stderr io.Writer
	// OnLine, when set, receives every stdout line as it arrives, before it is
	// decoded, and whatever Decode makes of it. It is called from Run's
	// goroutine, so it must be safe to call while the caller drains the event
	// channel.
	//
	// This is for transcripts, which are read by a human debugging a run. A
	// transcript rebuilt from decoded events would silently lose every line this
	// package does not model, and would not be the bytes the agent wrote.
	OnLine func(line string)
}

// normalize fills in defaults and rejects a combination that cannot work.
func (o *Options) normalize() error {
	if o.Bin == "" {
		o.Bin = DefaultBin
	}
	switch o.PermissionMode {
	case "":
		o.PermissionMode = PermissionStdio
	case PermissionStdio, PermissionSkip:
	default:
		return fmt.Errorf("unknown permission mode %q (want %q or %q)",
			o.PermissionMode, PermissionStdio, PermissionSkip)
	}
	if o.MaxLineBytes <= 0 {
		o.MaxLineBytes = MaxLineBytes
	}
	if o.Timeout == 0 {
		o.Timeout = DefaultTimeout
	}
	return nil
}

// args builds the argv for one run. It is separate from Run so the flags a mode
// produces can be asserted without starting a process.
func (o Options) args(prompt string) []string {
	// --verbose is not optional: stream-json requires it.
	args := []string{"--output-format", "stream-json", "--verbose"}

	if o.PermissionMode == PermissionSkip {
		// -p with no positional prompt reads it from stdin. Keeping the prompt
		// out of argv stops convention text and file contents from showing up
		// in ps/proc cmdline.
		args = append(args, "-p", "--dangerously-skip-permissions")
	} else {
		// The stdio permission channel owns stdin, so the prompt travels in argv.
		args = append(args, "-p", prompt, "--permission-prompt-tool", PermissionStdio)
	}
	if o.Resume != "" {
		args = append(args, "--resume", o.Resume)
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	if len(o.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(o.AllowedTools, ","))
	}
	if len(o.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(o.DisallowedTools, ","))
	}
	if o.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", o.AppendSystemPrompt)
	}
	return args
}

// Result is what a finished run reports. It is populated even when the run
// fails: a turn that was interrupted or that died mid-answer still has a session
// to resume, an answer to show and a reason to give.
type Result struct {
	// SessionID is the session the run belongs to, and what --resume takes.
	SessionID string
	// Answer is the agent's final answer text (FinalText over the stream).
	Answer string
	// Usage and DurationMS are the accounting from the result event.
	Usage      Usage
	DurationMS int
	// IsError reports that the agent called the turn a failure.
	IsError bool
	// ExitCode is the process exit status.
	ExitCode int
	// Stderr is what the agent wrote to stderr, bounded to its last lines.
	Stderr string
}

// Runner drives one agent process. Create it with New, run it once with Run, and
// answer its permission requests with RespondPermission from another goroutine.
type Runner struct {
	opts Options

	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	// writeMu serialises writes to the agent's stdin: two approvals answered at
	// the same time must not interleave halves of two JSON lines.
	writeMu sync.Mutex
}

// New returns a Runner for opts, validating them up front so a run cannot fail
// on a bad flag after a process is already up.
func New(opts Options) (*Runner, error) {
	if err := opts.normalize(); err != nil {
		return nil, err
	}
	return &Runner{opts: opts}, nil
}

// Run starts the agent with prompt and streams its events to ch, closing ch when
// the process ends. It blocks until then, so a caller that needs to answer
// permission requests must do so from another goroutine — every Event with a
// non-nil Control is waiting for exactly that.
//
// The caller owns draining ch. A caller that stops reading blocks the run rather
// than losing events, which is the failure mode to prefer: the alternative is an
// agent talking into a void.
//
// A Runner runs once. Run returns the run's Result alongside any error, so a
// failed or interrupted run still reports what it managed to do.
func (r *Runner) Run(ctx context.Context, prompt string, ch chan<- Event) (Result, error) {
	defer close(ch)

	if r.opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, r.opts.Bin, r.opts.args(prompt)...)
	cmd.Dir = r.opts.WorkDir
	if len(r.opts.Env) > 0 {
		cmd.Env = append(os.Environ(), r.opts.Env...)
	}
	// The agent spawns tools of its own; killing the group is what keeps a
	// cancelled run from leaving them behind.
	configureProcessGroup(cmd)

	var stdin io.WriteCloser
	if r.opts.PermissionMode == PermissionSkip {
		// The prompt goes over stdin, not argv; no permission channel is needed
		// because the agent runs with --dangerously-skip-permissions.
		cmd.Stdin = strings.NewReader(prompt)
	} else {
		var err error
		stdin, err = cmd.StdinPipe()
		if err != nil {
			return Result{}, fmt.Errorf("agent stdin: %w", err)
		}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		if stdin != nil {
			_ = stdin.Close()
		}
		return Result{}, fmt.Errorf("agent stdout: %w", err)
	}
	stderr := newTailBuffer(maxStderrBytes)
	if r.opts.Stderr != nil {
		cmd.Stderr = io.MultiWriter(r.opts.Stderr, stderr)
	} else {
		cmd.Stderr = stderr
	}

	if err := cmd.Start(); err != nil {
		if stdin != nil {
			_ = stdin.Close()
		}
		return Result{}, fmt.Errorf("start %s: %w", r.opts.Bin, err)
	}

	r.mu.Lock()
	r.cmd, r.stdin = cmd, stdin
	r.mu.Unlock()

	// The stream is read to EOF before Wait, so the agent's last events are seen
	// even when it exits non-zero.
	var (
		res    Result
		events []Event
	)
	scanErr := readStream(stdout, r.opts.MaxLineBytes, r.opts.OnLine, func(ev Event) {
		if ev.Type == EventSystem && res.SessionID == "" {
			res.SessionID = ev.SessionID
		}
		if ev.Type == EventResult {
			res.Usage, res.DurationMS, res.IsError = ev.Usage, ev.DurationMS, ev.IsError
		}
		events = append(events, ev)
		ch <- ev
	})

	waitErr := cmd.Wait()

	r.mu.Lock()
	r.stdin = nil
	r.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}

	res.Answer = FinalText(events)
	res.Stderr = stderr.String()
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}

	switch {
	case scanErr != nil:
		// The stream was abandoned, not the run: whatever the agent said before
		// this line is still in Result.Answer.
		return res, fmt.Errorf("reading agent output (line limit %d bytes): %w", r.opts.MaxLineBytes, scanErr)
	case ctx.Err() != nil:
		return res, ctx.Err()
	case waitErr != nil:
		if reason := strings.TrimSpace(res.Stderr); reason != "" {
			return res, fmt.Errorf("agent exited: %w: %s", waitErr, reason)
		}
		return res, fmt.Errorf("agent exited: %w", waitErr)
	}
	return res, nil
}

// RespondPermission answers one permission request, unblocking the agent that is
// waiting on it. It may be called while Run is streaming, and from a different
// goroutine.
//
// Answering nothing is not a neutral act: the agent stays stopped until an
// answer arrives, so every request must be answered exactly once — by this call,
// or by ending the run.
func (r *Runner) RespondPermission(_ context.Context, requestID string, result PermissionResult) error {
	if requestID == "" {
		return errors.New("permission request carries no request id")
	}

	r.mu.Lock()
	stdin := r.stdin
	r.mu.Unlock()
	if stdin == nil {
		return fmt.Errorf("agent is not running: cannot answer permission request %s", requestID)
	}

	line, err := json.Marshal(newControlResponse(requestID, result.decision()))
	if err != nil {
		return fmt.Errorf("encode permission response: %w", err)
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if _, err := stdin.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("send permission response for %s: %w", requestID, err)
	}
	return nil
}

// Close stops the agent and everything it spawned, and is safe to call at any
// point, including after the run has finished.
func (r *Runner) Close() error {
	r.mu.Lock()
	cmd := r.cmd
	r.stdin = nil
	r.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return terminateGroup(cmd.Process)
}

// maxStderrBytes bounds the stderr kept for the failure message. An agent
// started with --verbose can be chatty, and the reason a run failed is in the
// last lines, not the first.
const maxStderrBytes = 64 << 10

// tailBuffer is a bounded writer that keeps the most recent bytes written to it,
// so remembering why a run failed cannot grow without limit.
type tailBuffer struct {
	mu   sync.Mutex
	max  int
	data []byte
}

func newTailBuffer(max int) *tailBuffer { return &tailBuffer{max: max} }

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if over := len(b.data) - b.max; over > 0 {
		// Copy rather than reslice: keeping the old array alive would hold every
		// byte ever written.
		kept := make([]byte, len(b.data)-over)
		copy(kept, b.data[over:])
		b.data = kept
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.data))
}
