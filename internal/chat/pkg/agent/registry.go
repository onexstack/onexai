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
	"context"
	"errors"
	"fmt"
)

// Registry holds the agent backends this deployment has and the default to use
// when a conversation does not name one.
//
// It is read-only once built: the set of backends is decided at startup, so
// there is nothing to synchronise and no way for a request in flight to find the
// backend it started on missing.
type Registry struct {
	agents   map[string]Agent
	fallback string
}

// NewRegistry builds a registry over the given backends. fallback is the name
// used when neither the request nor the conversation picks one; an empty
// fallback means BackendLLM.
func NewRegistry(fallback string, agents ...Agent) *Registry {
	r := &Registry{
		agents:   make(map[string]Agent, len(agents)),
		fallback: ResolveBackend("", "", fallback),
	}
	for _, a := range agents {
		if a != nil {
			r.agents[a.Name()] = a
		}
	}
	return r
}

// Get returns the backend registered under name.
func (r *Registry) Get(name string) (Agent, bool) {
	if r == nil {
		return nil, false
	}
	a, ok := r.agents[name]
	return a, ok
}

// Fallback is the backend a new conversation starts on.
func (r *Registry) Fallback() string {
	if r == nil {
		return BackendLLM
	}
	return r.fallback
}

// Names returns the registered backend names, for logging at startup: a backend
// that failed to register is otherwise only visible when someone asks for it.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.agents))
	for name := range r.agents {
		names = append(names, name)
	}
	return names
}

// CloseSession releases the conversation behind key on every registered backend.
//
// Every backend is asked rather than the one the conversation is bound to,
// because the caller doing the asking — an idle reaper, a shutdown — has only the
// key. A backend that does not hold the conversation has nothing to release, so
// asking all of them costs a map lookup and one no-op per backend.
func (r *Registry) CloseSession(ctx context.Context, key string) error {
	if r == nil {
		return nil
	}
	var errs []error
	for _, a := range r.agents {
		if err := a.CloseSession(ctx, key); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// ResolveBackend decides which backend serves a conversation.
//
// The order is the point: an explicit request wins, then the backend the
// conversation is already bound to, then the deployment default. Resolving in
// any other order changes conversations under their users — a conversation that
// is mid-thread on the CLI backend must not silently switch to the LLM one
// because someone changed the default, and a user asking for the CLI backend on
// an existing LLM conversation must get it.
//
// It is a pure string function with no knowledge of what is registered: choosing
// a name and checking that the name exists are separate steps, so an unknown
// name a user typed is reported rather than quietly replaced by the default.
func ResolveBackend(requested, bound, fallback string) string {
	for _, name := range []string{requested, bound, fallback} {
		if name != "" {
			return name
		}
	}
	return BackendLLM
}
