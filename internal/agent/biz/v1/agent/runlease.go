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
	"time"

	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/safego"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// runLease is what one run borrows before it can be handed to the relay
// goroutine: the conversation's concurrency slot, the bounded run context, and
// the stop registration. Ask and Approve borrow exactly the same three things
// and owe them back on exactly the same paths, so they are borrowed and
// returned here rather than twice.
//
// Its lifetime has phases, and the order matters:
//
//	leaseRun   the conversation slot is held; no run clock yet
//	bind       the run clock starts and the hard-cancel stop is registered
//	startSpan  the root span context replaces the plain run context
//	stopReady  the hard cancel is replaced by the engine's graceful stop
//	handoff    the relay goroutine owns cleanup from here on
//	unwind     the caller-side cleanup, a no-op after handoff
//
// The split between leaseRun and bind exists because Ask takes the conversation
// slot before loading and compacting history (compaction may call a model), but
// must not start the run's timeout until after that work: the timeout bounds
// the run, not the caller's preparation.
type runLease struct {
	// b is the owning business object; the lease is never used on its own.
	b *AgentBiz
	// release returns the conversation slot. It is idempotent.
	release func()
	// ctx is the run's context. Nil until bind.
	ctx context.Context
	// cancel bounds the run. It is a no-op until bind.
	cancel context.CancelFunc
	// deregister drops the stop registration. It is a no-op until bind, and is
	// replaced by stopReady when the engine hands back its safe-point stop.
	deregister func()
	// subject is the authorization subject carried by ctx. bind returns it so a
	// caller does not have to compute a second one that could disagree.
	subject authz.Subject
	// handedOff reports that the relay goroutine owns cleanup. Once set, unwind
	// must do nothing.
	handedOff bool
}

// leaseRun takes the conversation's concurrency slot. It is the first phase of
// a run; bind starts the run's clock later, once the caller's preparation is
// done.
func (b *AgentBiz) leaseRun(sessionID string) (*runLease, error) {
	release, ok := b.acquire(sessionID)
	if !ok {
		return nil, ErrConversationBusy
	}
	return &runLease{
		b:          b,
		release:    release,
		cancel:     func() {},
		deregister: func() {},
	}, nil
}

// bind builds the run's context and registers the stop that governs it.
//
// The context descends from the app-lifetime root rather than the request, so a
// run survives the caller returning but is cancelled on server shutdown; see
// runContext for the identity it carries. The returned subject is the same
// value carried in the context, because authorization needs it in hand.
//
// The deadline is the run's own bound (zero means the deployment default). It
// is applied here rather than at lease time so Ask's history compaction runs on
// the request context and outside the run's timeout.
func (l *runLease) bind(
	runID string, deadline time.Duration,
	userID, userName, kind string, agents []string,
) (context.Context, authz.Subject) {
	l.ctx, l.cancel, l.subject = l.b.runContext(deadline, userID, userName, kind, agents)
	if l.b.deps.Stops != nil {
		l.deregister = l.b.deps.Stops.Register(runID, l.cancel)
	}
	return l.ctx, l.subject
}

// startSpan opens the run's root span and installs it on the run's context.
// Both entry points call it after the selection is resolved, so every span the
// run produces joins the same trace.
func (l *runLease) startSpan(runID, sessionID, subjectID string, agents []string) context.Context {
	l.ctx = l.b.startRunSpan(l.ctx, runID, sessionID, subjectID, agents)
	return l.ctx
}

// stopReady returns the callback through which the engine hands back its
// safe-point stop.
//
// It replaces the run's registration, which up to this point is the hard
// context cancel installed by bind — that one still governs a stop arriving
// while the run is being prepared. The previous deregister must not be called:
// registrations are keyed by run ID, so calling it would delete the new one.
func (l *runLease) stopReady(runID string) func(func()) {
	return func(stop func()) {
		if l.b.deps.Stops == nil || stop == nil {
			return
		}
		l.deregister = l.b.deps.Stops.Register(runID, stop)
	}
}

// unwind returns everything a run that was refused before hand-off borrowed.
//
// It is the deferred guard of both entry points: every failure between leaseRun
// and handoff unwinds the same three things, and one guard instead of a call at
// each return is what keeps a newly added admission step from leaking the
// conversation slot — a leaked slot answers that session with
// ErrConversationBusy until the process restarts.
//
// It is a no-op once handoff has transferred ownership, so a caller may both
// defer it and hand off without coordinating.
func (l *runLease) unwind() {
	if l.handedOff {
		return
	}
	if l.ctx != nil {
		endRunSpan(l.ctx)
	}
	l.deregister()
	l.cancel()
	l.release()
}

// handoff transfers ownership of the lease to the relay goroutine and returns
// the run's event stream. The goroutine releases each borrowed resource exactly
// once; the endRunSpan that pairs with startSpan is relay's own first defer.
//
// After this call the caller must not touch the lease again.
func (l *runLease) handoff(engineEvents <-chan agentapi.Event, sp suspend) <-chan agentapi.Event {
	l.handedOff = true
	// Snapshot before the goroutine exists: ownership has moved, and the
	// deferred calls must capture these values rather than read the lease.
	b, ctx, cancel, release, deregister := l.b, l.ctx, l.cancel, l.release, l.deregister
	out := make(chan agentapi.Event, 16)
	safego.Go(ctx, "agent relay", func() {
		defer close(out)
		defer release()
		defer deregister()
		defer cancel()
		b.relay(ctx, engineEvents, out, sp)
	})
	return out
}
