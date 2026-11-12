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

	"github.com/onexstack/onexai/internal/chat/pkg/agentref"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// The shipped allocation strategies.
//
// A strategy turns the resolved candidates into the set the turn will actually
// run as. It is a separate seam from Decider because the two change for
// different reasons: a decider is added when a new kind of reference appears, an
// allocator changes when the way candidates combine changes. Neither has any
// effect on the other.
//
// The reason they are not one interface: **an empty answer means opposite things
// on the two sides.** A decider returning nothing is ordinary — most people have
// no peer binding — and the caller carries on to the next reference. An
// allocator returning nothing is the failure, and the caller has to say so. One
// interface returning `[]string{}` for both would make every caller guess.

// firstAllocator keeps the leading candidate.
//
// It is the default because a reference chain already puts the most specific
// answer first: `["@peer", "@default"]` means "this person's own agent, or the
// default if they have none", and that ordering *is* the policy. Taking the
// first is not a fallback — it is reading the policy out.
type firstAllocator struct{}

func (firstAllocator) Name() string { return chat.AllocatorFirst }

func (firstAllocator) Allocate(_ context.Context, _ Decision, candidates []string) ([]string, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	// Copied rather than sliced: the caller keeps the full candidate list for
	// its log line, and a slice sharing that backing array would let a later
	// append write into it.
	return []string{candidates[0]}, nil
}

// allAllocator keeps every candidate.
//
// It is what a conversation means when it wants one agent's knowledge unioned
// with another's. It is not the default because running several agents is the
// exception: it costs more, and the answer no longer has one owner.
type allAllocator struct{}

func (allAllocator) Name() string { return chat.AllocatorAll }

func (allAllocator) Allocate(_ context.Context, _ Decision, candidates []string) ([]string, error) {
	return candidates, nil
}

// allocators is the set of strategies this build ships, keyed by the name a
// deployment configures.
//
// Package-level because every allocator is stateless: the decision is passed in,
// not held. A per-instance copy would be the same map built once per bot for no
// reason.
var allocators = func() map[string]Allocator {
	all := []Allocator{firstAllocator{}, allAllocator{}}
	out := make(map[string]Allocator, len(all))
	for _, a := range all {
		out[a.Name()] = a
	}
	return out
}()

// deciders is the set of reference kinds this build ships, keyed by the name
// that follows the sigil.
//
// Adding a kind is adding an entry here and nothing else: the expansion loop,
// the validation, and the option that selects a strategy are all written against
// the interface.
//
// Package-level for the same reason as allocators — and it is only possible
// because every decider is pure. A decider that needed the runtime would have to
// be built per instance, and then every test fixture that assembles a partial
// runtime would also have to remember to build it.
var deciders = func() map[string]Decider {
	all := []Decider{peerDecider{}, oncallDecider{}, defaultDecider{}}
	out := make(map[string]Decider, len(all))
	for _, d := range all {
		out[d.Kind()] = d
	}
	// The vocabulary and the implementation must agree, and a disagreement is
	// silent in both directions: a kind in agentref with no decider fails only
	// when someone writes it, and a decider whose kind is unlisted is one the
	// /agent command refuses to accept. Checked here, at init, so it is a startup
	// failure instead.
	for _, k := range agentref.Kinds() {
		if _, ok := out[k]; !ok {
			panic(fmt.Sprintf("agentref declares kind %q but no decider implements it", k))
		}
	}
	for k := range out {
		if !agentref.KnownKind(k) {
			panic(fmt.Sprintf("decider %q is not declared in agentref.Kinds", k))
		}
	}
	return out
}()
