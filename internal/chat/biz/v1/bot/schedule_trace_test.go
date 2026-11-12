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
	"strings"
	"sync"
	"testing"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/agentref"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// resolutionLog records the decider calls of one expansion, in order.
//
// The expansion loop is a fixed-point iteration, and its shape — how many
// passes, and which reference each pass resolves — is the thing that is easy to
// get subtly wrong: a single pass would hand the agent service a literal
// "@peer", and an unbounded loop would walk a cycle forever. Neither shows up in
// the final agent list alone, so the calls themselves have to be observable.
type resolutionLog struct {
	mu    sync.Mutex
	kinds []string
}

func (l *resolutionLog) add(kind string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.kinds = append(l.kinds, kind)
}

// calls returns the kinds resolved, in order.
func (l *resolutionLog) calls() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.kinds...)
}

// countingDecider delegates to a shipped decider and notes every call.
type countingDecider struct {
	inner Decider
	log   *resolutionLog
}

func (c countingDecider) Kind() string { return c.inner.Kind() }

func (c countingDecider) Resolve(ctx context.Context, d Decision, arg string) ([]string, error) {
	c.log.add(c.inner.Kind())
	return c.inner.Resolve(ctx, d, arg)
}

// watchResolutions wraps every registered decider so one test's expansion can be
// traced. The registry is package-level and the loop reads it directly, so this
// swaps the entries and restores them when the test ends.
//
// That is safe only because no test in this package calls t.Parallel: package
// tests run one at a time, so no other expansion is running while the wrappers
// are installed.
func watchResolutions(t *testing.T) *resolutionLog {
	t.Helper()

	log := &resolutionLog{}
	saved := make(map[string]Decider, len(deciders))
	for kind, dec := range deciders {
		saved[kind] = dec
		deciders[kind] = countingDecider{inner: dec, log: log}
	}
	t.Cleanup(func() {
		for kind, dec := range saved {
			deciders[kind] = dec
		}
	})
	return log
}

// TestPrivateChatTraceResolvesToAFixedPoint is the plan's worked example,
// asserted step by step.
//
// The configuration is the interesting one: a private chat asks for
// `["@peer","@default"]`, and the external default is itself `@peer`. That is a
// legitimate configuration rather than a mistake — it is how a deployment says
// "everyone outside the tenant uses whatever they bound themselves" — and it is
// exactly what forces the second pass: without the loop, pass one would hand the
// agent service a literal "@peer", which is not an agent.
func TestPrivateChatTraceResolvesToAFixedPoint(t *testing.T) {
	log := watchResolutions(t)

	d := Decision{
		Conversation: ConversationRef{Type: model.SessionTypePrivateChat},
		Speaker:      PrincipalRef{PeerBound: true, PeerAgents: []string{"onex-u-user-a1b2c3"}},
		Defaults:     AudienceDefaults{Internal: "helper", External: "@peer"},
		// The template an empty session.refs expands to, taken from the function
		// that produces it rather than restated here.
		Refs: defaultRefs(model.SessionTypePrivateChat),
	}

	got, err := scheduler().expand(context.Background(), d)
	if err != nil {
		t.Fatalf("expand() = %v", err)
	}
	if want := "onex-u-user-a1b2c3"; len(got) != 1 || got[0] != want {
		t.Errorf("expand() = %v, want [%s]", got, want)
	}

	// Pass one asks the speaker's binding and gets nothing (they are not bound in
	// this deployment's first attempt — see the caller's PeerBound), then turns
	// `@default` into `@peer`. Pass two resolves that `@peer` into a name.
	want := "peer,default,peer"
	if calls := strings.Join(log.calls(), ","); calls != want {
		t.Errorf("resolves %s, want %s: the default's answer must be fed back through the loop", calls, want)
	}
}

// TestExpandStopsAtACycleInsteadOfTruncating pins the bound that makes a
// misconfiguration fail loudly.
//
// The cycle here is not synthetic: an on-call group's agent list accepts the
// same reference syntax as a session declaration, so configuring the group's
// list as `@default` while the default points at `@oncall` is a configuration
// two different people can produce without either of them typing an obviously
// wrong thing.
func TestExpandStopsAtACycleInsteadOfTruncating(t *testing.T) {
	log := watchResolutions(t)

	d := Decision{
		Conversation: ConversationRef{OncallAgents: []string{"@default"}},
		Speaker:      PrincipalRef{TenantUserID: "go.kong"},
		Defaults:     AudienceDefaults{Internal: "@oncall"},
		Refs:         []string{"@default"},
	}

	got, err := scheduler().expand(context.Background(), d)
	if err == nil {
		t.Fatalf("expand() = %v, want an error: the chain never terminates", got)
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("error = %q, want it to name the bound it hit", err)
	}
	// Reported with the references in play, and — the point — truncated to
	// nothing: a half-walked chain is an agent set nobody asked for.
	if len(got) != 0 {
		t.Errorf("expand() returned %v alongside the error, want nothing", got)
	}
	if n := len(log.calls()); n != maxExpandDepth {
		t.Errorf("%d decider calls before giving up, want the bound of %d", n, maxExpandDepth)
	}
}

// TestExpandQueuesEachResultOnce pins the deduplication's real contract.
//
// The loop notes what a pass produced, so a reference that resolves to another
// reference is expanded in the next pass once — not once per occurrence. That is
// what keeps the cost of a list with repeats from being paid again on every
// pass. It does not dedupe the input: two identical entries are parsed as what
// they are, twice.
func TestExpandQueuesEachResultOnce(t *testing.T) {
	log := watchResolutions(t)

	d := Decision{
		// A tenant speaker, so the internal default below is the one consulted.
		Speaker: PrincipalRef{
			TenantUserID: "go.kong",
			PeerBound:    true,
			PeerAgents:   []string{"onex-u-user-a1b2c3"},
		},
		Defaults: AudienceDefaults{Internal: "@peer"},
		Refs:     []string{"@default", "@default"},
	}

	got, err := scheduler().expand(context.Background(), d)
	if err != nil {
		t.Fatalf("expand() = %v", err)
	}
	if want := "onex-u-user-a1b2c3"; len(got) != 1 || got[0] != want {
		t.Errorf("expand() = %v, want [%s] — the repeated result must collapse", got, want)
	}

	calls := log.calls()
	counts := map[string]int{}
	for _, k := range calls {
		counts[k]++
	}
	if counts[agentref.KindDefault] != 2 {
		t.Errorf("the default decider ran %d times, want 2 — the input is not deduplicated",
			counts[agentref.KindDefault])
	}
	if counts[agentref.KindPeer] != 1 {
		t.Errorf("the peer decider ran %d times, want 1 — its result was queued twice",
			counts[agentref.KindPeer])
	}
}

// TestTheSameDeclarationResolvesDifferentlyPerSpeaker pins where `@peer`'s
// judgement lives.
//
// It is the property the whole design turns on: a declaration is stored once per
// conversation, but two people in that conversation do not get the same agent.
// Nothing about the conversation differs between these two runs — same refs,
// same type, same on-call group — so a result that came out the same would mean
// the peer binding had been resolved once and cached on the conversation.
func TestTheSameDeclarationResolvesDifferentlyPerSpeaker(t *testing.T) {
	refs := []string{"@peer"}
	d := Decision{Conversation: ConversationRef{Type: model.SessionTypePrivateChat}, Refs: refs}

	bound := d
	bound.Speaker = PrincipalRef{PeerBound: true, PeerAgents: []string{"onex-u-user-a1b2c3"}}
	got, err := scheduler().expand(context.Background(), bound)
	if err != nil {
		t.Fatalf("expand(bound) = %v", err)
	}
	if len(got) != 1 || got[0] != "onex-u-user-a1b2c3" {
		t.Errorf("expand(bound) = %v, want the speaker's own agent", got)
	}

	unbound := d
	unbound.Speaker = PrincipalRef{TenantUserID: "go.kong", PeerBound: false}
	got, err = scheduler().expand(context.Background(), unbound)
	if err != nil {
		t.Fatalf("expand(unbound) = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expand(unbound) = %v, want nothing: an internal user has no binding", got)
	}
}

// TestTheDefaultIsReadPerConfiguredValue pins that the two configured defaults
// are consulted for what they are — including a reference — and that which one
// is read is decided by the speaker alone.
func TestTheDefaultIsReadPerConfiguredValue(t *testing.T) {
	tests := []struct {
		name         string
		tenantUserID string
		defaults     AudienceDefaults
		want         []string
	}{
		{
			name:         "internal speaker, internal default",
			tenantUserID: "go.kong",
			defaults:     AudienceDefaults{Internal: "helper", External: "public-agent"},
			want:         []string{"helper"},
		},
		{
			name:         "external speaker, external default",
			tenantUserID: "",
			defaults:     AudienceDefaults{Internal: "helper", External: "public-agent"},
			want:         []string{"public-agent"},
		},
		{
			// The configuration §14 asks about: the external default is not a
			// name at all but `@peer`, and it resolves through the same loop.
			name:         "external default configured as a reference goes through the decider",
			tenantUserID: "",
			defaults:     AudienceDefaults{Internal: "helper", External: "@peer"},
			want:         []string{"onex-u-user-a1b2c3"},
		},
		{
			// The audience picks the value, it does not fall back between them: a
			// mixed audience with no external default gets nothing, rather than the
			// internal agent by accident.
			name:         "an external speaker is never handed the internal default",
			tenantUserID: "",
			defaults:     AudienceDefaults{Internal: "helper"},
			want:         nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decision{
				Speaker: PrincipalRef{
					TenantUserID: tt.tenantUserID,
					PeerBound:    true,
					PeerAgents:   []string{"onex-u-user-a1b2c3"},
				},
				Defaults: tt.defaults,
				Refs:     []string{"@default"},
			}
			got, err := scheduler().expand(context.Background(), d)
			if err != nil {
				t.Fatalf("expand() = %v", err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("expand() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPrivateChatBindingOverrideResolvesOnlyTheBinding pins that the override
// replaces the declaration rather than extending it: only @peer is asked, so
// the declaration's own references — including @default — never expand.
func TestPrivateChatBindingOverrideResolvesOnlyTheBinding(t *testing.T) {
	log := watchResolutions(t)

	b, _, dr := privateChat(t, []string{"fz-public"}, []string{"fz-admin"}, "user-admin")

	res, err := b.resolvedAgents(context.Background(), dr)
	if err != nil {
		t.Fatalf("resolvedAgents() = %v", err)
	}
	if len(res.Agents) != 1 || res.Agents[0] != "fz-admin" {
		t.Errorf("Agents = %v, want the binding's agent", res.Agents)
	}
	if !res.BindingPreferred {
		t.Error("BindingPreferred = false, want true")
	}
	if calls := strings.Join(log.calls(), ","); calls != "peer" {
		t.Errorf("resolves %s, want peer alone: the declaration must not be expanded", calls)
	}
}

// TestAllAllocatorRunsOnlyTheBindingInAPrivateChat pins the reason the override
// replaces rather than prepends. Under `all`, a prepended @peer would run the
// binding's agent and the stale declaration together.
func TestAllAllocatorRunsOnlyTheBindingInAPrivateChat(t *testing.T) {
	b, _, dr := privateChat(t, []string{"fz-public"}, []string{"fz-admin"}, "user-admin")
	b.sr.AgentAllocator = chat.AllocatorAll

	result, err := b.schedule(context.Background(), dr)
	if err != nil {
		t.Fatalf("schedule() = %v", err)
	}
	if len(result.Agents) != 1 || result.Agents[0] != "fz-admin" {
		t.Errorf("Agents = %v, want only the binding's agent", result.Agents)
	}
}
