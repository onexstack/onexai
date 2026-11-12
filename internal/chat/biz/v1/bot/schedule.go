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
	"sync"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/agentref"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// Audience says whether a speaker belongs to this tenant.
//
// It exists for exactly one decision — which agent the `@default` reference
// resolves to — and it is a **function of the current message**, not a property
// of the conversation: no column, no member-event subscription, no reconciler.
//
// The alternative was a conversation-level audience, which needs the group's
// member list and therefore an event subscription this service does not have
// (`im.chat.member.user.added_v1` is not among the webhook handlers), a periodic
// reconciliation because those events get dropped, and a column to cache the
// answer. All of that buys one row of the following table:
//
//	speaker        audience   audience-of-the-conversation would say
//	internal       internal   internal
//	external       mixed      mixed
//	internal, in a mixed group   internal  |  mixed
//
// That last row is the whole difference. This service answers the internal
// speaker with the internal agent even in a room that holds outsiders — and the
// answer is then visible to them. That is deliberate: the bot is no more
// restrictive than the person typing, who could have said the same thing
// themselves, in a room they chose to be in.
const (
	// AudienceInternal is a speaker the chat backend placed in this tenant.
	AudienceInternal = "internal"
	// AudienceMixed is a speaker it did not — someone outside the tenant, or a
	// deployment whose app cannot read tenant identities at all. The two are one
	// answer on purpose: in both cases this service cannot tell who the speaker
	// is, and the response to that is the same.
	AudienceMixed = "mixed"
)

// AudienceOf derives the audience from the speaker alone.
func AudienceOf(d Decision) string {
	if d.Speaker.TenantUserID == "" {
		return AudienceMixed
	}
	return AudienceInternal
}

// ConversationRef is the conversation half of a scheduling decision.
type ConversationRef struct {
	// Type is the session type (model.SessionType*), which picks the template
	// for an empty Ref list and decides whether a binding may override the
	// declaration. It comes from conversationTypeOf: the stored row when it
	// states one, otherwise a direct receiver identifies a private chat.
	Type string
	// ID is the conversation id, for logs.
	ID string
	// OncallAgents is the agent list of the on-call group this conversation's
	// ticket belongs to, resolved by the caller. Empty for a conversation with
	// no ticket, which is the ordinary case.
	//
	// It is carried resolved rather than looked up inside the decider so that a
	// Decision is a snapshot a test can build by hand, with no store behind it.
	OncallAgents []string
}

// PrincipalRef is the speaker half of a scheduling decision.
//
// # Three identifiers that must not be confused
//
//	PeerID        the chat platform's name for this person (Lark: open_id).
//	              Used to look up the binding, and **never sent anywhere else**.
//	TenantUserID  the chat backend's tenant-scoped name (Lark: user_id), empty
//	              for anyone outside the tenant. Used only for the audience.
//	Subject       what the agent service authorizes as. Derived below.
//
// They are all strings, they are all "a user id", and swapping two of them is
// silent: the authorization simply answers about a different identity. That is
// why they are named differently and separately commented rather than both being
// called SenderID.
type PrincipalRef struct {
	Backend      string
	PeerID       string
	TenantUserID string
	// Subject is the identity the agent service receives. It is the binding's
	// canonical identity when there is one, and the tenant identity otherwise.
	//
	// The two call paths into the agent service — this one and
	// ox-agent-apiserver's — must present the same subject for the same person,
	// or the authorization rules match on one path and not the other. OneX pushes
	// its rules with the subject set to its own userID, so the binding is what
	// translates a chat identity into it.
	Subject string
	// PeerBound reports whether a binding row exists at all. It is what
	// distinguishes "not bound" from "bound, but with no usable agent" — two
	// states that an empty agent list alone cannot tell apart.
	PeerBound bool
	// PeerAgents is the binding's agent list, carried here so the `@peer`
	// decider does not repeat the lookup the subject already needed.
	PeerAgents []string
	IsSRE      bool
}

// Decision is the input of one scheduling pass. Everything a decider needs is
// already resolved on it, so deciders are pure functions of their input.
type Decision struct {
	Conversation ConversationRef
	Speaker      PrincipalRef
	// Defaults are the two configured fallbacks `@default` chooses between.
	//
	// They are carried on the decision rather than read from the runtime inside
	// the decider so that deciders stay pure: every input they consult is on this
	// struct, which is what lets the registry be a package-level constant instead
	// of something each bot instance has to build.
	Defaults AudienceDefaults

	// Refs is the **effective declaration**: whatever the pass will resolve.
	// It is the conversation's own declaration, a private chat's binding when
	// that binding is usable, or the conversation type's template — decided by
	// effectiveRefs and never re-derived here. The scheduler consumes it and
	// never re-applies a template — applying one twice would treat
	// `["@peer","@default"]` as a single entry, and applying it nowhere would
	// leave every default conversation with nothing.
	Refs []string

	// BindingPreferred reports that Refs came from a private chat's speaker
	// binding rather than from the conversation's declaration. It lets a reader
	// explain why a declaration it just wrote is not the one in effect.
	BindingPreferred bool
}

// Hint names why a scheduling pass produced no agent. Empty means it produced
// at least one.
//
// The type and its values live in the runtime package because the answer leaves
// this package: a command asks for a resolution through
// DynamicRuntime.ResolvedAgents (the bot imports the command package, not the
// other way round) and the reason has to travel with it.
type Hint = runtime.AgentHint

const (
	// HintNone is the success case.
	HintNone = runtime.AgentHintNone
	// HintUnbound: the conversation asked for `@peer` and this speaker has no
	// binding. The reply is an invitation to bind.
	HintUnbound = runtime.AgentHintUnbound
	// HintNoAgent: the speaker is bound but nothing is usable — no membership,
	// or one that lapsed. The reply is an invitation to subscribe.
	HintNoAgent = runtime.AgentHintNoAgent
	// HintUnresolved: nothing else applies. The default agent may simply not be
	// configured for this audience, which is the state of a fresh deployment.
	HintUnresolved = runtime.AgentHintUnresolved
)

// Decider resolves one reference kind into agent names.
//
// Resolve returns names *or further references*: the caller feeds the result
// back through the expansion loop, so a decider whose answer is another
// reference — `@default` configured as `@peer` — needs no special case here.
//
// An empty result is not an error. "This speaker has no binding" and "this
// conversation has no on-call group" are ordinary states, and a decider that
// returned an error for them would make every caller classify them by hand.
type Decider interface {
	// Kind is the name that follows the sigil, e.g. "peer" for @peer.
	Kind() string
	// Resolve returns what this reference expands to. arg is the `:<arg>` tail,
	// empty when the reference had none.
	Resolve(ctx context.Context, d Decision, arg string) ([]string, error)
}

// Allocator picks the final agent set from the resolved candidates.
//
// It is a separate seam from Decider because the two change for different
// reasons and at different rates: a decider is added when a new kind of
// reference appears, while an allocator changes when the way candidates combine
// changes. They also differ in what an empty answer means — for a decider it is
// normal, for an allocator it is the failure.
type Allocator interface {
	Name() string
	Allocate(ctx context.Context, d Decision, candidates []string) ([]string, error)
}

// defaultRefs is the template a conversation type falls back to.
//
// It depends on the session type **only**. What `@default` turns into is the
// audience's business, and keeping the two apart is what lets a new session type
// change the shape of the chain without touching the audience rule, and a new
// audience change the value without touching the shape.
func defaultRefs(sessionType string) []string {
	switch sessionType {
	case model.SessionTypeTicketGroup:
		// The on-call group first — that is what the conversation is for. A
		// ticket group belongs to its duty group, so the agent configured there
		// is the one that answers; the speaker's own binding is only the
		// fallback for a group that configures no agent, followed by the
		// platform default so such a group still answers at all.
		return []string{"@oncall", "@peer", "@default"}
	case model.SessionTypePrivateChat:
		// Reached only when no usable binding chose the refs — see effectiveRefs.
		// The speaker's own agent, then the default. The default is not optional
		// here: an internal user has no binding, and without that second entry a
		// private chat would resolve to nothing at all — a regression from the
		// hardcoded "default" this replaced.
		return []string{"@peer", "@default"}
	case model.SessionTypeNormalGroup:
		// A group resolves per speaker as well, which is what the account-binding
		// offer promises ("绑定飞书后，你在飞书群里提问时会用你的专属助手" — after binding Feishu, your questions in Feishu groups use your own assistant) and what
		// the /agent command can already be used to set explicitly. An unbound
		// speaker resolves @peer to nothing and falls to @default, so the entry is
		// a fallback rather than a requirement — same shape as a private chat.
		//
		// The known consequence, recorded rather than discovered: an answer given
		// in a group is visible to everyone in it, and a member's agent can
		// retrieve what that member bought. This template is why "which agent"
		// follows the speaker into a group; restricting what a group answer may
		// *retrieve* is a separate decision that has not been made.
		return []string{"@peer", "@default"}
	default:
		// Everything else keeps the platform default alone. This is the OpenAPI
		// surface and any session type this build does not know: neither has a
		// peer identity to look up today, so asking for @peer would be asking a
		// question nothing can answer. It is deliberately not folded into the
		// group case above — "unknown" should stay on the more conservative
		// template rather than inherit a change made for groups.
		return []string{"@default"}
	}
}

// conversationTypeOf reports the conversation type a decision should use.
//
// The stored row wins when it states a type: it is the conversation's own
// record. When the row is missing — or states nothing — a direct receiver is
// still a fact about the platform, and it is the one a private chat's agent
// resolution depends on: the catch-all template would skip `@peer`, the only
// reference a private chat exists to consult. A group with no row deliberately
// keeps the conservative catch-all; inferring normal_group there is a separate
// decision and is not made here.
func conversationTypeOf(sessionM *model.SessionM, isP2P bool) string {
	if sessionM != nil && sessionM.SessionType != "" {
		return sessionM.SessionType
	}
	if isP2P {
		return model.SessionTypePrivateChat
	}
	return ""
}

// effectiveRefs returns the declaration this pass will resolve, and whether a
// private chat's binding chose it.
//
// Precedence differs by conversation type because a declaration means something
// different in the two kinds of room:
//
//   - private chat: a usable binding wins. The room and the speaker are the same
//     person there, so the binding the user sees and edits in the console is the
//     only statement that can be about them. A session declaration written
//     earlier by /agent is history; it still applies when the binding names no
//     agent, so an unbound speaker keeps whatever the conversation asked for.
//   - every other type: the session's declaration wins. A group is a shared
//     room, and /agent there is a room-level decision made by an admin.
//   - nothing declared: the type's template.
//
// The override REPLACES the declaration rather than prepending `@peer`: under
// the `all` allocator a prepended entry would run the binding's agent and the
// old declaration together, which is not what "the binding wins" means.
func effectiveRefs(sessionType string, declared []string, sp PrincipalRef) ([]string, bool) {
	if sessionType == model.SessionTypePrivateChat &&
		sp.PeerBound && hasUsablePeerAgents(sp.PeerAgents) {
		return []string{"@peer"}, true
	}
	if len(declared) > 0 {
		return declared, false
	}
	return defaultRefs(sessionType), false
}

// hasUsablePeerAgents reports whether a binding names any agent at all.
//
// A row with an empty list is a deliberate state — "bound, but @peer is not
// offered" — not a missing one. Treating it as usable would make it override the
// room's declaration and then resolve to nothing, which is the one outcome the
// user did not ask for. The empty-string check matters for the same reason: a
// malformed cell decodes to `[""]`, and that must not count as a name.
func hasUsablePeerAgents(names []string) bool {
	for _, n := range names {
		if n != "" {
			return true
		}
	}
	return false
}

// maxExpandDepth bounds the reference expansion.
//
// It is what makes a cycle terminate, and it catches **every** cycle rather than
// the one shape a targeted check would: `@default` pointing at `@peer` is a
// legitimate configuration, and `@default` pointing back at itself is not, but
// so is `@default` → `@a` → `@default`, and only a depth bound sees all three.
//
// Four is well above any real chain — the longest shipped one is two — and low
// enough that a mistake fails on the first message rather than walking a graph.
const maxExpandDepth = 4

// Result is what one scheduling pass produced.
type Result struct {
	// Refs is the effective declaration the pass consumed: the conversation's
	// declaration, a private chat's usable binding, or the type's template.
	Refs []string
	// Agents is the final set, in order.
	Agents []string
	// Subject is the identity to hand the agent service.
	Subject string
	// Hint is non-empty exactly when Agents is empty, and names the situation
	// so the caller can pick the right reply.
	Hint Hint
	// BindingPreferred reports that Refs came from a private chat's binding
	// rather than from the conversation's declaration.
	BindingPreferred bool
}

// schedule resolves a conversation's agents and reports the identity to run as.
//
// It is the single point where "which agent" is decided. Everything downstream —
// the request to the agent service, the log line, the card — reads its answer.
//
// It writes nothing. The outcome used to be recorded on the conversation's row
// (chat.session.agents) and read back by whoever rendered a menu next; that
// column is gone because the answer belongs to a **speaker**, not to a room, so
// a recorded one described whoever talked last rather than whoever is asking.
// Every reader resolves for itself now, which is why this is a pure function of
// the request and the stores.
func (b *botBiz) schedule(ctx context.Context, dr *runtime.DynamicRuntime) (Result, error) {
	d, err := b.buildDecision(ctx, dr)
	if err != nil {
		return Result{}, err
	}

	candidates, err := b.expand(ctx, d)
	if err != nil {
		return Result{}, err
	}

	final, err := b.allocate(ctx, d, candidates)
	if err != nil {
		return Result{}, err
	}

	return Result{
		Refs:             d.Refs,
		Agents:           final,
		Subject:          d.Speaker.Subject,
		Hint:             hintFor(d, final),
		BindingPreferred: d.BindingPreferred,
	}, nil
}

// hintFor names the empty case.
//
// It keys on whether `@peer` was among the requests, because that is what makes
// an empty result actionable: a speaker who asked for their own agent and has
// none needs to be told to bind, while a conversation that resolved to nothing
// because no default is configured needs a different sentence entirely.
func hintFor(d Decision, final []string) Hint {
	if len(final) > 0 {
		return HintNone
	}
	if !wantsPeer(d.Refs) {
		return HintUnresolved
	}
	if !d.Speaker.PeerBound {
		return HintUnbound
	}
	return HintNoAgent
}

// wantsPeer reports whether a reference list asks for the peer binding.
//
// It walks the raw refs rather than the parsed forms so it can answer for a list
// that failed to parse — the caller has already reported that failure, and this
// only decides which sentence accompanies the empty result.
func wantsPeer(refs []string) bool {
	for _, raw := range refs {
		if r, err := agentref.ParseRef(raw); err == nil && r.Kind == "peer" {
			return true
		}
	}
	return false
}

// buildDecision assembles the snapshot, resolving everything a decider would
// otherwise have to look up.
func (b *botBiz) buildDecision(ctx context.Context, dr *runtime.DynamicRuntime) (Decision, error) {
	conv := ConversationRef{
		ID:   dr.Address(),
		Type: conversationTypeOf(dr.SessionM, dr.IsP2P()),
	}
	if dr.TicketM != nil && b.sr.Duty != nil {
		// Only the metadata is needed here. GetGroupMeta deliberately skips the
		// schedule sub-tables, so a decision on an ordinary message does not pay
		// for a rotation evaluation it never reads.
		if g, ok, err := b.sr.Duty.GetGroupMeta(ctx, dr.TicketM.GroupID); err == nil && ok {
			conv.OncallAgents = g.Agents
		}
	}

	sp := PrincipalRef{
		Backend:      chat.BackendLark,
		PeerID:       dr.Handle(),
		TenantUserID: dr.Identity(),
	}
	// The runtime already answered this for the same identity when it was built,
	// and in live mode every answer is a fresh pass over the RBAC bindings. One
	// message asked three times; this is the third answer taken from the first.
	sp.IsSRE = dr.IsSRE

	// The binding lookup. It fills two fields at once — the subject and the agent
	// list — because they are the same row, written at the same moment and read
	// by the same decision. Looking the row up twice (once per field) would be
	// two queries on the message path for one fact.
	sp.Subject = sp.TenantUserID
	if sp.PeerID != "" && b.sr.Store != nil {
		row, err := b.sr.Store.PeerAgents().GetByPeer(ctx, sp.Backend, b.sr.AppID, sp.PeerID)
		if err != nil {
			// A lookup that failed is not "not bound": reporting it as unbound
			// would invite a user who is bound to bind again. The turn is refused
			// rather than answered as somebody else.
			return Decision{}, fmt.Errorf("look up peer binding: %w", err)
		}
		if row != nil {
			sp.PeerBound = true
			sp.PeerAgents = decodeNames(row.Agents)
		}
		// The subject is chosen by the same function /agent uses, so the command
		// and the run cannot disagree about who is asking.
		sp.Subject = runtime.SubjectFor(sp.TenantUserID, row)
	}

	// A nil SessionM is a conversation the store has no row for yet — the bot was
	// added and its row failed to persist, or this message arrived before the
	// P2P-entered event created it. There is no declaration to read, but a direct
	// receiver still identifies a private chat, and the template only needs the
	// type (see conversationTypeOf). This path deliberately writes nothing: the
	// row is created by the event handlers in event.go.
	var declared []string
	if dr.SessionM != nil {
		declared = dr.SessionM.RefList()
	}
	refs, bindingPreferred := effectiveRefs(conv.Type, declared, sp)

	defaults := AudienceDefaults{
		Internal: b.sr.InternalDefaultAgent,
		External: b.sr.ExternalDefaultAgent,
	}

	return Decision{
		Conversation:     conv,
		Speaker:          sp,
		Defaults:         defaults,
		Refs:             refs,
		BindingPreferred: bindingPreferred,
	}, nil
}

// expand resolves a reference list to concrete agent names, to a fixed point.
//
// # Why a loop rather than one pass
//
// A decider may answer with another reference. `@default` configured as `@peer`
// is the shipped example: the first pass turns `@default` into `@peer`, and the
// second turns `@peer` into names. One pass would hand the agent service a
// literal `@peer`, which is not an agent.
//
// # The queue is deduplicated as it is filled
//
// What a pass produced is queued once, so a reference that resolves to another
// reference is expanded in the next pass once however many times it appeared.
// The input is not deduplicated: two identical entries are parsed as what they
// are, twice, and a decider is a pure function of the Decision, so the second
// answer is the first one again.
//
// The saving is one pass's worth of work per repeat rather than a lookup — no
// shipped decider reads a store; everything they consult was resolved while the
// Decision was built.
func (b *botBiz) expand(ctx context.Context, d Decision) ([]string, error) {
	// names collects the resolved literals; pending holds entries that still
	// need resolving, in order. It starts as the raw strings and is refilled with
	// whatever a decider answers, which is why the type is []string and not []Ref
	// — a decider's output is itself subject to parsing.
	var names []string
	pending := d.Refs

	for depth := 0; depth < maxExpandDepth; depth++ {
		if len(pending) == 0 {
			return dedupe(names), nil
		}

		var next []string
		seen := make(map[string]struct{}, len(pending))
		for i, raw := range pending {
			r, err := agentref.ParseRef(raw)
			if err != nil {
				return nil, fmt.Errorf("agent reference %d: %w", i+1, err)
			}
			if !r.IsRef() {
				names = append(names, r.Literal)
				continue
			}
			dec, ok := deciders[r.Kind]
			if !ok {
				return nil, fmt.Errorf("unknown agent reference %q: no decider is registered for %q",
					r.String(), r.Kind)
			}
			got, err := dec.Resolve(ctx, d, r.Arg)
			if err != nil {
				return nil, fmt.Errorf("resolve %s: %w", r.String(), err)
			}
			for _, g := range got {
				if _, dup := seen[g]; dup {
					continue
				}
				seen[g] = struct{}{}
				next = append(next, g)
			}
		}
		pending = next
	}

	// Out of passes with work left: a reference chain that does not terminate.
	// Reported rather than truncated — a silently dropped half of a chain is an
	// agent set nobody asked for.
	return nil, fmt.Errorf("agent reference expansion exceeded %d passes (a reference probably points back at itself): %v",
		maxExpandDepth, d.Refs)
}

// allocate runs the configured strategy, defaulting to the first one shipped.
func (b *botBiz) allocate(ctx context.Context, d Decision, candidates []string) ([]string, error) {
	name := b.sr.AgentAllocator
	if name == "" {
		name = chat.AllocatorFirst
	}
	alloc, ok := allocators[name]
	if !ok {
		return nil, fmt.Errorf("unknown agent allocator %q", name)
	}
	return alloc.Allocate(ctx, d, candidates)
}

// dedupe removes repeats while keeping the first occurrence's position.
//
// Order is meaning here: the first allocator takes the leading candidate, so the
// earliest reference in the list has to stay earliest.
func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// decodeNames reads a JSON agent list out of a binding row.
//
// A malformed cell yields nil rather than an error, matching the leniency the
// session model's own accessors choose: the row is a projection, and a torn one
// should render as "nothing usable" — which the caller already has a reply for —
// rather than fail a turn.
func decodeNames(raw string) []string {
	if raw == "" {
		return nil
	}
	return (&model.PeerAgentM{Agents: raw}).AgentNames()
}

// resolvedAgents runs a scheduling pass and answers it in the shape a command
// can consume.
//
// # Why it is the answer to every question about "which agent"
//
// It is what DynamicRuntime.ResolvedAgents is wired to, so one call feeds three
// readers: the command menu a conversation's granted commands are filtered by,
// /agent's "what does the declaration I just wrote pick", and /chat's report of
// what this caller is running as. It is a callback on the runtime rather than a
// lookup the command package performs because the scheduler lives in this
// package, and the command package cannot import it.
//
// # Why it resolves rather than reads a recorded answer
//
// There is nothing recorded to read: chat.session.agents was deleted, because a
// resolution belongs to a **speaker** and not to a conversation. `@peer` is one
// agent for whoever bound an account and another for the next person to talk, so
// a conversation-wide record answered "what did the last speaker run as" while
// every caller was asking "what do I run as" — and in a group those differ. That
// is why in a group a menu now lists the commands **this** caller's agent was
// granted, where it used to list the last speaker's.
//
// The cost of asking instead is one indexed point read of chat.peer_agent, and
// it writes nothing. It is wired into a runtime through resolvedAgentsOnce, so
// one request pays that read once however many times it asks.
//
// # Why a declaration change no longer needs a special case
//
// It used to be documented as the one caller that had to schedule immediately,
// because writing `@peer` says nothing about whether it picks an agent until
// somebody resolves it. Now every reader resolves, so that is no longer special
// — only the wording of the reply is, and that lives with the caller
// (AgentResolution.Line).
//
// The error is returned rather than logged here because the readers want
// different things from it: a command explains, and a menu renders nothing.
func (b *botBiz) resolvedAgents(ctx context.Context, dr *runtime.DynamicRuntime) (runtime.AgentResolution, error) {
	result, err := b.schedule(ctx, dr)
	if err != nil {
		return runtime.AgentResolution{}, err
	}
	return runtime.AgentResolution{
		Refs:             result.Refs,
		Agents:           result.Agents,
		Hint:             result.Hint,
		BindingPreferred: result.BindingPreferred,
	}, nil
}

// resolvedAgentsOnce returns the ResolvedAgents callback for one request: the
// first call runs the pass and every later call in the same request is answered
// from it.
//
// # Why one request asks twice, and why the second answer must be the first
//
// A command request reaches the question from two places — assembling the tree,
// and the gate that runs before the command does (command.ValidateGranted) — and
// the answer is a function of the request alone: the same caller, the same
// conversation row, the same peer binding. Running the pass again would read the
// same row for the same answer, and it would do it on the path a person waits
// on.
//
// It is per request rather than per bot because it is *not* a cache: two requests
// from two people in one group have different answers, and each has to do its
// own read. The closure is what carries the scope — nothing here remembers
// anything after the request it was built for.
func (b *botBiz) resolvedAgentsOnce() func(context.Context, *runtime.DynamicRuntime) (runtime.AgentResolution, error) {
	var (
		once sync.Once
		res  runtime.AgentResolution
		err  error
	)
	return func(ctx context.Context, dr *runtime.DynamicRuntime) (runtime.AgentResolution, error) {
		once.Do(func() { res, err = b.resolvedAgents(ctx, dr) })
		return res, err
	}
}
