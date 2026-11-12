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

package bot

import (
	"context"
	"fmt"
)

// The shipped deciders.
//
// They are pure functions of a Decision. Everything they need was resolved while
// the Decision was built — including the peer binding, which the subject also
// needed — so none of them touches a store, and each is testable by constructing
// its input.
//
// Two of the three keys are constants; the peer one is not, because the lookup
// that fills it belongs to buildDecision. The rule that puts a lookup there
// rather than here is: **decide once, in one place, for every consumer of the
// same fact.**

// peerDecider resolves `@peer`: the agents bound to whoever is speaking.
//
// The binding was already fetched, because the authorization subject comes from
// the same row. A second lookup here would be a second query on the message path
// for a fact the caller is holding.
//
// An empty result is the ordinary case, not a failure: most people are not bound
// to anything. hintFor turns it into an invitation to bind when the conversation
// actually asked for it.
type peerDecider struct{}

func (peerDecider) Kind() string { return "peer" }

func (peerDecider) Resolve(_ context.Context, d Decision, arg string) ([]string, error) {
	if err := noArg("peer", arg); err != nil {
		return nil, err
	}
	return d.Speaker.PeerAgents, nil
}

// oncallDecider resolves `@oncall`: the agents the conversation's on-call group
// binds.
//
// The list was resolved from the duty store while the Decision was built, so
// this is also a pure read. It is empty for any conversation that is not a
// ticket group, and for a ticket whose module is no longer configured — both
// ordinary, and both meant to fall through to whatever the next reference says.
type oncallDecider struct{}

func (oncallDecider) Kind() string { return "oncall" }

func (oncallDecider) Resolve(_ context.Context, d Decision, arg string) ([]string, error) {
	if err := noArg("oncall", arg); err != nil {
		return nil, err
	}
	return d.Conversation.OncallAgents, nil
}

// AudienceDefaults is what `@default` resolves to, per audience.
type AudienceDefaults struct {
	// Internal applies when the speaker belongs to this tenant.
	Internal string
	// External applies otherwise. Empty is a legitimate value: it resolves to no
	// agent, which the speaker is told about rather than being handed the
	// internal one.
	External string
}

// defaultDecider resolves `@default` against the audience.
//
// # It returns whatever the configuration says, which may itself be a reference
//
// `@peer` is a legitimate value here, and it is exactly how a conversation group
// whose members are outsiders resolves to "each of them uses their own agent".
// The expansion loop feeds the answer back, so this decider needs no case for it
// — the same way no decider needs a case for returning a name.
//
// # An unconfigured external default resolves to nothing, on purpose
//
// The empty answer becomes an empty candidate set, which becomes a card telling
// the speaker there is no agent here. The alternative — falling back to the
// internal default — hands an internal resource to somebody outside the tenant,
// and does it silently. See the field comment on StaticRuntime.
type defaultDecider struct{}

func (defaultDecider) Kind() string { return "default" }

func (defaultDecider) Resolve(_ context.Context, dec Decision, arg string) ([]string, error) {
	if err := noArg("default", arg); err != nil {
		return nil, err
	}
	configured := dec.Defaults.Internal
	if AudienceOf(dec) == AudienceMixed {
		configured = dec.Defaults.External
	}
	if configured == "" {
		return nil, nil
	}
	return []string{configured}, nil
}

// noArg refuses an argument on a reference that takes none.
//
// Silently ignoring `@oncall:pay` would make a typo look like it worked. The
// reference syntax reserves the tail for deciders that use it, and a decider
// that does not is the one that has to say so.
func noArg(kind, arg string) error {
	if arg == "" {
		return nil
	}
	return fmt.Errorf("@%s takes no argument, got %q", kind, arg)
}
