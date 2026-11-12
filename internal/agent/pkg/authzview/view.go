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

// Package authzview applies policy decisions to the console's resource reads.
//
// The authorization engine answers a single question about a single resource.
// A list is many questions, and the console must not show a row the caller may
// not read. This package holds the request-scoped subject, the rollout mode and
// the per-request decision cache the list loops consult.
package authzview

import (
	"context"
	"strings"
	"sync"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

// Mode is how much of the resource guard is enforced.
//
//   - off: no decision is consulted; behaviour is exactly the pre-guard one.
//   - dry-run: decisions are evaluated and logged, then the legacy behaviour
//     stands. It exists so an operator can see what would change before it does.
//   - additive: an explicit Deny hides/refuses; an explicit Allow grants; a
//     NoOpinion keeps the legacy behaviour (owner or shared).
//   - authoritative: NoOpinion no longer keeps the legacy behaviour for reads
//     (the row is hidden) or for shared writes (they are refused). Owner-scoped
//     writes still let the owner through.
type Mode string

const (
	ModeOff           Mode = "off"
	ModeDryRun        Mode = "dry-run"
	ModeAdditive      Mode = "additive"
	ModeAuthoritative Mode = "authoritative"
)

// State is the request-scoped authorization view.
type State struct {
	Coordinator *authz.Coordinator
	Subjects    []authz.Subject
	// ReadMode governs visibility; WriteMode governs mutations. They are
	// separate so an operator can hide denied rows without yet refusing writes,
	// or the other way around.
	ReadMode  Mode
	WriteMode Mode
	Admin     bool
	// Kinds narrows the rows that are filtered; empty means every kind the
	// caller asks about.
	Kinds map[string]struct{}

	mu    sync.Mutex
	cache map[string]authz.Decision
}

type stateKey struct{}

// WithState attaches the view to ctx. A nil state is a no-op, which is what a
// deployment with authorization disabled passes.
func WithState(ctx context.Context, state *State) context.Context {
	if state == nil {
		return ctx
	}
	return context.WithValue(ctx, stateKey{}, state)
}

// StateFromContext returns the view attached by WithState, or nil.
func StateFromContext(ctx context.Context) *State {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(stateKey{}).(*State)
	return state
}

// ReadEnabled reports whether visibility should be filtered.
func (s *State) ReadEnabled() bool {
	return s != nil && s.Coordinator != nil && s.ReadMode != "" && s.ReadMode != ModeOff
}

// WriteEnabled reports whether mutations should be checked.
func (s *State) WriteEnabled() bool {
	return s != nil && s.Coordinator != nil && s.WriteMode != "" && s.WriteMode != ModeOff
}

// Visible reports whether the caller may read one row.
//
// The order is the same as the write guard: an explicit Deny wins over
// everything except administrator access; an explicit Allow grants; a
// NoOpinion falls back to the legacy owner/namespace behaviour. An owner may
// read their own row when no rule speaks, but a Deny written against the
// resource still hides it — that is what make deny useful for taking a single
// object away from its owner.
func (s *State) Visible(ctx context.Context, kind, namespace, name, owner string) bool {
	if !s.ReadEnabled() {
		return true
	}
	if len(s.Kinds) > 0 {
		if _, ok := s.Kinds[kind]; !ok {
			return true
		}
	}
	if s.Admin {
		return true
	}

	canonical := name
	if namespace != "" && !strings.Contains(name, "/") {
		canonical = namespace + "/" + name
	}

	decision := s.decide(ctx, authz.VerbRead, kind, canonical)
	if decision == authz.DecisionDeny {
		return false
	}
	if decision == authz.DecisionAllow {
		return true
	}

	// Resource-level NoOpinion: a grant on the enclosing namespace also makes
	// the resource readable. This is the same fallback the CMDB path uses, and
	// it is what lets an operator grant a whole business line in one rule.
	if namespace != "" {
		nsDecision := s.decide(ctx, authz.VerbRead, string(authz.ResourceKindNamespace), namespace)
		if nsDecision == authz.DecisionDeny {
			return false
		}
		if nsDecision == authz.DecisionAllow {
			return true
		}
	}

	// No rule spoke. An owner still reads their own row; otherwise additive
	// keeps the legacy "signed-in users can read" behaviour and authoritative
	// hides.
	if owner != "" && s.owns(owner) {
		return true
	}
	return s.ReadMode != ModeAuthoritative
}

// Decide answers one write authorization question.
func (s *State) Decide(ctx context.Context, verb authz.Verb, kind, name string) (authz.Decision, string) {
	if !s.WriteEnabled() {
		return authz.DecisionAllow, "authz disabled"
	}
	if s.Admin {
		return authz.DecisionAllow, "administrator"
	}
	if len(s.Subjects) == 0 {
		return authz.DecisionDeny, "no subject"
	}
	decision, reason, err := s.Coordinator.AuthorizeUseWithDefault(
		ctx, s.Subjects, verb, authz.ResourceKind(kind), name, authz.DecisionNoOpinion,
	)
	if err != nil {
		// An evaluation failure is not evidence of permission.
		return authz.DecisionDeny, "authorization error: " + err.Error()
	}
	return decision, reason
}

func (s *State) decide(ctx context.Context, verb authz.Verb, kind, name string) authz.Decision {
	key := string(verb) + "|" + kind + "|" + name
	s.mu.Lock()
	if decision, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return decision
	}
	s.mu.Unlock()

	decision, _, err := s.Coordinator.AuthorizeUseWithDefault(
		ctx, s.Subjects, verb, authz.ResourceKind(kind), name, authz.DecisionNoOpinion,
	)
	if err != nil {
		decision = authz.DecisionDeny
	}

	s.mu.Lock()
	if s.cache == nil {
		s.cache = make(map[string]authz.Decision)
	}
	s.cache[key] = decision
	s.mu.Unlock()
	return decision
}

// owns reports whether one of the caller's identities is the row owner.
func (s *State) owns(owner string) bool {
	for _, subject := range s.Subjects {
		if subject.Kind != authz.SubjectKindUser {
			continue
		}
		if subject.UserID != "" && subject.UserID == owner {
			return true
		}
		if subject.UserName != "" && subject.UserName == owner {
			return true
		}
	}
	return false
}

// NewState builds the request state for a signed-in caller.
func NewState(
	coordinator *authz.Coordinator, subject authz.Subject, admin bool,
	readMode, writeMode Mode, kinds map[string]struct{},
) *State {
	return &State{
		Coordinator: coordinator,
		Subjects:    []authz.Subject{subject},
		Admin:       admin,
		ReadMode:    readMode,
		WriteMode:   writeMode,
		Kinds:       kinds,
		cache:       make(map[string]authz.Decision),
	}
}

// Visible reports whether a list row may be shown. A request without a guard
// state (authorization disabled, a service caller, a deployment with the guard
// off) shows everything, which is the pre-guard behaviour.
func Visible(ctx context.Context, kind, namespace, name, owner string) bool {
	state := StateFromContext(ctx)
	if state == nil {
		return true
	}
	return state.Visible(ctx, kind, namespace, name, owner)
}

// Filter removes the rows the caller may not read. It returns the input
// unchanged when the request has no filter, so the common path allocates
// nothing.
func Filter[T any](ctx context.Context, kind string, rows []T, fields func(T) (namespace, name, owner string)) []T {
	if !ReadFiltered(ctx, kind) {
		return rows
	}
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		namespace, name, owner := fields(row)
		if Visible(ctx, kind, namespace, name, owner) {
			out = append(out, row)
		}
	}
	return out
}

// AdjustCount subtracts the hidden rows from a total without going negative.
//
// It is exact for the console's "fetch the whole list" read (limit 0), which is
// what every resource list page uses. For a server-paged read the total is the
// unfiltered total, so the adjustment is a best-effort lower bound rather than
// a lie about the page itself.
func AdjustCount(total int64, hidden int) int64 {
	if hidden <= 0 {
		return total
	}
	total -= int64(hidden)
	if total < 0 {
		return 0
	}
	return total
}

// ReadFiltered reports whether this request's list of kind should be filtered.
// It lets a list skip the extra allocation when the guard is off.
func ReadFiltered(ctx context.Context, kind string) bool {
	state := StateFromContext(ctx)
	if state == nil || !state.ReadEnabled() {
		return false
	}
	if len(state.Kinds) > 0 {
		_, ok := state.Kinds[kind]
		return ok
	}
	return true
}
