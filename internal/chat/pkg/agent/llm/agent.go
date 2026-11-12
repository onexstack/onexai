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

// Package llm is the oai-agent-apiserver backend for the agent port.
//
// It holds no conversation state: a run is one HTTP request whose response is an
// SSE stream, and the history — memory, the run registry that stop and approve
// address — lives in agent under the session key. That is why a Session's ID
// is empty: there is no local handle to resume with, so there is nothing to name.
//
// What it does hold is where the current run's events go, because a run outlives
// the call that started it. A run that pauses for a decision ends its stream, and
// the answer that continues it arrives as a later, unrelated request — a card
// click, on whichever replica the platform happens to reach. That is why one
// Session per key is kept here and handed back on every lookup: the answer has to
// reach the same sink the first half rendered through, and a fresh session per
// call would have no way to know what that was.
package llm

import (
	"context"
	"errors"
	"sync"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	agentclient "github.com/onexstack/onexai/pkg/clientset/agent"
)

// Agent is the port's oai-agent-apiserver backend.
type Agent struct {
	client agentclient.Interface

	// mu guards sessions, the per-conversation bindings. The map is bounded by
	// the conversations this process is serving: the session manager announces
	// each one it forgets through CloseSession, which is where an entry goes.
	mu       sync.Mutex
	sessions map[string]*session
}

var _ agent.Agent = (*Agent)(nil)

// New returns the agent backend over the given client.
func New(client agentclient.Interface) *Agent {
	return &Agent{client: client, sessions: map[string]*session{}}
}

// Name implements agent.Agent.
func (a *Agent) Name() string { return agent.BackendLLM }

// Session returns the conversation bound to key, binding it on first use.
// Nothing is created on the agent side here — the first run creates it there
// — so this cannot fail except on a missing client, which is a wiring error worth
// reporting at the first turn rather than at the first call.
func (a *Agent) Session(_ context.Context, key string) (agent.Session, error) {
	if a.client == nil {
		return nil, errors.New("the oai-agent-apiserver client is not configured")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sessions[key]; ok {
		return s, nil
	}
	s := &session{client: a.client, key: key}
	a.sessions[key] = s
	return s, nil
}

// CloseSession implements agent.Agent: the conversation behind key is over, so
// the binding is dropped.
//
// agent keeps the conversation itself, and a later turn on the same key
// continues it whether or not this process remembered anything — what is
// released here is only the local record of where a paused run's events go. A
// pause whose conversation is reaped therefore stops being answerable from this
// replica, which is the same end it would reach anyway: the run behind it is
// abandoned, and its checkpoint expires on its own.
func (a *Agent) CloseSession(_ context.Context, key string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, key)
	return nil
}

// Close implements agent.Agent.
func (a *Agent) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions = map[string]*session{}
	return nil
}
