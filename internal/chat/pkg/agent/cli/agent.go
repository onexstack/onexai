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

// Package cli is the agent port's local-process backend: it drives a Claude Code
// process on this host as one conversation.
//
// It is a translation layer and nothing else. The process lifecycle, the
// stream-json codec, the permission handshake and the per-session workspace all
// live in pkg/agentcli, which the CLI builds on too; what happens here
// is the mapping between that runtime's vocabulary and the port's, in both
// directions.
//
// Two consequences of being a process are worth stating, because they are where
// this backend differs from the llm one:
//
//   - A conversation must be able to answer a permission request that arrives
//     during a turn, from a different HTTP request than the one that started the
//     run. Sessions are therefore shared per key rather than created per call —
//     the card callback and the turn must reach the same object.
//   - A run holds a real process, so a conversation can genuinely be busy in a
//     way the caller cannot work around. That is what agent.ErrBusy means here.
package cli

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/pkg/agentcli"
)

// Options configures a CLI backend.
//
// WorkspaceRoot is required, and deliberately has no default: it is the
// directory every conversation's agent runs inside, and defaulting it to the
// process's working directory would silently hand a chat message the server's
// own checkout.
type Options struct {
	// Bin is the agent executable. Empty means agentcli's default ("claude").
	Bin string
	// WorkspaceRoot is the directory each conversation gets a subdirectory of.
	WorkspaceRoot string
	// AllowedDirs are extra trees a conversation may be pointed at with /dir, on
	// top of WorkspaceRoot. They are not where conversations start — they are for
	// an operator who wants a conversation to work on a checkout that already
	// lives somewhere else, rather than having to move it under the root.
	AllowedDirs []string
	// MaxSessions caps concurrently live conversations.
	MaxSessions int
	// PermissionMode is "stdio" (the default, routed to the user) or "skip".
	PermissionMode string
	// AllowedTools, when non-empty, is the only set of tools the agent may use.
	AllowedTools []string
	// DisallowedTools is removed from the tool set after AllowedTools applies.
	DisallowedTools []string
	// Model overrides the model the agent runs.
	Model string
	// TurnTimeout bounds one turn. Zero means agentcli's default.
	TurnTimeout time.Duration
	// MaxLineBytes bounds one line of the agent's event stream. Zero means
	// agentcli's default.
	MaxLineBytes int
}

// Agent is the cli backend, and the owner of its conversations.
type Agent struct {
	opts Options

	mu       sync.Mutex
	sessions map[string]*session
	closed   bool
}

var _ agent.Agent = (*Agent)(nil)

// New returns a CLI backend for opts. It refuses an unusable configuration up
// front: a backend that cannot say where its agents run is a backend that would
// run them somewhere nobody chose.
func New(opts Options) (*Agent, error) {
	if opts.WorkspaceRoot == "" {
		return nil, errors.New("cli agent: a workspace root is required")
	}
	if opts.MaxSessions < 1 {
		return nil, fmt.Errorf("cli agent: max sessions must be at least 1, got %d", opts.MaxSessions)
	}
	return &Agent{opts: opts, sessions: make(map[string]*session)}, nil
}

// Name implements agent.Agent.
func (a *Agent) Name() string { return agent.BackendCLI }

// AllowedRoots implements agent.DirScope: where this backend's conversations may
// run. It is computed once, here, so every caller that offers a directory to a
// user and every check that refuses one are looking at the same list.
func (a *Agent) AllowedRoots() []string {
	return agentcli.AllowedRoots(a.opts.WorkspaceRoot, a.opts.AllowedDirs...)
}

// Session implements agent.Agent. The same key returns the same conversation for
// the life of the process: the run and the permission answer that unblocks it
// arrive on different requests, and they have to meet.
func (a *Agent) Session(_ context.Context, key string) (agent.Session, error) {
	if key == "" {
		return nil, errors.New("cli agent: a conversation key is required")
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("cli agent: the backend is closed")
	}
	if s, ok := a.sessions[key]; ok {
		return s, nil
	}
	if len(a.sessions) >= a.opts.MaxSessions {
		// Refused rather than queued: the limit exists because each conversation
		// is a process on this host, and a request that waits for one to free up
		// is holding a chat message open for a process's whole lifetime.
		return nil, fmt.Errorf("%w: %d CLI conversations are already live (max %d)",
			agent.ErrTooManySessions, len(a.sessions), a.opts.MaxSessions)
	}

	s := newSession(a, key)
	a.sessions[key] = s
	return s, nil
}

// CloseSession implements agent.Agent: the conversation behind key is over, so
// its process is terminated and forgotten. An unknown key is not an error — a
// conversation may have been reaped already, or never have used this backend.
func (a *Agent) CloseSession(_ context.Context, key string) error {
	a.mu.Lock()
	s := a.sessions[key]
	a.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.Close()
}

// Close implements agent.Agent: it terminates every conversation's process and
// forgets them.
func (a *Agent) Close() error {
	a.mu.Lock()
	sessions := make([]*session, 0, len(a.sessions))
	for _, s := range a.sessions {
		sessions = append(sessions, s)
	}
	a.sessions = make(map[string]*session)
	a.closed = true
	a.mu.Unlock()

	var errs []error
	for _, s := range sessions {
		if err := s.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// forget drops a conversation once it has been closed, so a later request for the
// same key starts a new one instead of finding a dead session. Closing is
// separate from forgetting because a caller may close a conversation to stop its
// work while the conversation itself is still the same one.
func (a *Agent) forget(s *session) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cur, ok := a.sessions[s.key]; ok && cur == s {
		delete(a.sessions, s.key)
	}
}

// Live returns how many conversations this backend currently holds, for logging
// and for tests that pin the session cap.
func (a *Agent) Live() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.sessions)
}
