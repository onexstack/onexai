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

package agentref

import (
	"errors"
	"fmt"
	"strings"
)

// Package agentref parses the agent references that appear in a conversation's
// declaration and in an on-call group's agent list.
//
// It is a leaf package on purpose. Two callers need it and neither may import
// the other: the scheduler (biz/v1/bot) and the /agent command (pkg/command/ai),
// where the latter is imported *by* the former. Putting the parsing in either
// would close a cycle, and duplicating it would give the two a chance to
// disagree about what a reference is.
//
// RefSigil marks a reference: an entry that is resolved at scheduling time
// rather than naming an agent directly.
//
// # Why `@` and not the repository's own `kind:` prefix
//
// This service already has a prefix convention for "not a literal" — `robot:`
// and `platform:` on conversation ids (internal/chat/biz/v1/chat/agent.go). Those
// are internal identifiers that no user ever types. A reference here appears in
// **user input** (`/agent @peer`), where readability carries more weight, and `@`
// is what command-line tools use to mean "point at this object".
//
// It is also unambiguous by construction: no agent name begins with `@`, because
// ParseRef would read it as a reference — which is why ValidateAgentName exists,
// to refuse such a name at the point it is created rather than let one exist and
// mean two things.
const RefSigil = "@"

// Errors ParseRef reports. They are values rather than formatted strings so a
// caller can tell "empty" from "malformed" when it words a reply.
var (
	// ErrEmptyRef is an entry that is blank after trimming.
	ErrEmptyRef = errors.New("agent reference is empty")
	// ErrMalformedRef is a sigil with nothing usable after it.
	ErrMalformedRef = errors.New("agent reference is malformed")
)

// Ref is one entry of a reference list: either a literal agent name, or a
// reference the scheduler resolves.
type Ref struct {
	// Literal is the agent name, set when Kind is empty.
	Literal string
	// Kind names the decider (Handler.Kind) that resolves this reference.
	Kind string
	// Arg is the optional `:<arg>` tail, empty when there is none.
	Arg string
}

// IsRef reports whether this entry needs resolving rather than naming an agent.
func (r Ref) IsRef() bool { return r.Kind != "" }

// String renders the reference back to its stored form.
func (r Ref) String() string {
	if !r.IsRef() {
		return r.Literal
	}
	if r.Arg == "" {
		return RefSigil + r.Kind
	}
	return RefSigil + r.Kind + ":" + r.Arg
}

// ParseRef parses one stored entry.
//
// A bare name passes through: the static case is not a special case of the
// reference syntax, it is the absence of it. Everything else is `@kind` or
// `@kind:arg`.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Ref{}, ErrEmptyRef
	}
	if !strings.HasPrefix(s, RefSigil) {
		return Ref{Literal: s}, nil
	}
	kind, arg, _ := strings.Cut(s[len(RefSigil):], ":")
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return Ref{}, fmt.Errorf("%w: %q", ErrMalformedRef, s)
	}
	return Ref{Kind: kind, Arg: strings.TrimSpace(arg)}, nil
}

// ValidateAgentName refuses a name the reference syntax would shadow.
//
// An agent named `@peer` would be unreachable — ParseRef reads it as the peer
// reference — and worse, it would make the two mean the same thing. Refusing it
// where names are created keeps the two spaces disjoint.
//
// It also refuses a name containing `.`, for a reason that is historical rather
// than structural: these lists were once stored dot-joined, and a stray name with
// a dot in it was silently split in two. The encoding is JSON now, so the rule is
// no longer load-bearing — but a name that once broke the storage is not one to
// quietly start allowing.
func ValidateAgentName(name string) error {
	if name == "" {
		return errors.New("agent name is empty")
	}
	if strings.HasPrefix(name, RefSigil) {
		return fmt.Errorf("agent name %q must not begin with %q: that prefix is the reference syntax",
			name, RefSigil)
	}
	if strings.Contains(name, ".") {
		return fmt.Errorf("agent name %q must not contain '.'", name)
	}
	return nil
}

// ParseRefList parses a whole stored list, reporting the first bad entry.
//
// The position is named because the message is shown to whoever typed it: "entry
// 2" is actionable, "invalid reference" is not.
func ParseRefList(raw []string) ([]Ref, error) {
	out := make([]Ref, 0, len(raw))
	for i, s := range raw {
		r, err := ParseRef(s)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", i+1, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// Kind names the shipped reference kinds.
//
// They live here rather than beside the deciders because two packages need to
// agree on them and neither may import the other: the scheduler builds a decider
// per kind, and the /agent command refuses a misspelled kind before writing it.
// A single list is what keeps those two from disagreeing — and the scheduler
// checks its registry against this list at startup, so a decider added without
// its name being added here fails there rather than at the first message.
const (
	// KindPeer is the agents bound to whoever is speaking.
	KindPeer = "peer"
	// KindOncall is the agents the conversation's on-call group binds.
	KindOncall = "oncall"
	// KindDefault is the platform agent this audience defaults to.
	KindDefault = "default"
)

// Kinds lists every shipped reference kind.
func Kinds() []string { return []string{KindPeer, KindOncall, KindDefault} }

// KnownKind reports whether a kind is one this build understands.
func KnownKind(kind string) bool {
	for _, k := range Kinds() {
		if k == kind {
			return true
		}
	}
	return false
}
