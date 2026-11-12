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

	"github.com/onexstack/onexai/internal/agent/pkg/approval"
)

// runState is the part of a run that belongs to the request rather than to the
// agent graph: the event sink its tools report to, the approval policy that
// governs it, and the authorization gate the policy defers to.
//
// It travels on the run's context rather than in the handlers' fields, which is
// what lets the handlers be built once per agent set and shared. Two runs of the
// same agent set differ in exactly this state and in nothing the graph depends
// on — so a handler that captured it could not be shared, and a handler that
// reads it from the context can be.
type runState struct {
	// emitter is where this run's tool steps are published. Nil means the run
	// reports no steps, which is what a caller that renders none gets.
	emitter *runEmitter
	// policy decides which tool calls need a human. The zero value asks for
	// nothing.
	policy approval.Policy
	// allowAll answers every remaining gate in this run without asking, because
	// a human already answered once with DecisionAllowAll. It arrives on a
	// different call than the interrupt it answers — possibly on a different
	// pod — which is why it is part of the state rather than of the graph.
	allowAll bool
	// fallback is what a gated call does when this run cannot pause for an
	// answer. Empty means the run can be paused.
	fallback string
	// gate is the run's authorization gate, or nil when authorization is off.
	gate *authzGate
}

// runStateKey is the context key. It is an unexported struct type so no other
// package can collide with it.
type runStateKey struct{}

// withRunState attaches the state to a run's context.
func withRunState(ctx context.Context, state *runState) context.Context {
	return context.WithValue(ctx, runStateKey{}, state)
}

// runStateFrom returns the state attached to a run's context, or nil when the
// context carries none.
//
// A nil result is not an error: it is how a handler behaves when it runs
// outside a prepared run, which is the case in tests that exercise a handler
// directly.
func runStateFrom(ctx context.Context) *runState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(runStateKey{}).(*runState)
	return state
}
