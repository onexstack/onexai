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

package runtime

import (
	"context"

	"github.com/onexstack/onexai/internal/chat/model"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// RequestSource identifies how a command invocation was triggered.
type RequestSource string

const (
	RequestSourceLark RequestSource = "lark"
	RequestSourceAPI  RequestSource = "api"
	RequestSourceMCP  RequestSource = "mcp"
	RequestSourceTUI  RequestSource = "tui"
	RequestSourceNone RequestSource = "none"
)

// DynamicRuntime holds per-request context derived from the incoming call.
// Obtain it via From(ctx); inject it via Inject(ctx, dr).
type DynamicRuntime struct {
	Source RequestSource
	// IsSRE reports whether the caller is an SRE team member.
	IsSRE bool
	// IsAdmin reports whether the caller may run admin-only commands. It is not
	// derived from IsSRE here: the two roles are decided by whoever builds this —
	// the SRE list and the admin list for a Lark caller, the token's claims for a
	// REST one — and a role that was guessed at in the middleware would be a role
	// nobody configured.
	IsAdmin bool
	// Sender is the caller, in both of the namespaces a platform can report them
	// in, and it is the only carrier of that fact on this struct: every caller —
	// a Lark message, a card click, a REST request, an MCP tool call — is a Sender,
	// and one whose platform reports no handle simply has an empty one.
	//
	// Identity and address are read from it through the methods below rather than
	// off its fields, so that where each comes from is decided in one place. They
	// are different questions: Identity answers "is this one of ours" and is what
	// every authorization, record owner and allow-list names, while Address answers
	// "where does a reply go" and is what a direct message is sent to. Reading the
	// wrong one used to be indistinguishable from reading the right one, because
	// for a tenant-mate they are the same string.
	Sender v1.Sender

	// Receiver is where a reply to this call goes; nil for a surface that cannot
	// reply (MCP, TUI). Its contents are reached through Address/IsP2P/IsGroup, so
	// that a nil receiver is a case the accessors handle rather than one every
	// caller has to remember.
	Receiver *v1.Receiver
	// ConvID is the conversation this call acts on. It is not the address: a REST
	// caller names a conversation it is acting for and has no place to be replied
	// to, and the two coincide only on a Lark surface, where the conversation is
	// the one the reply goes to.
	ConvID string

	// SessionM is the session metadata (lazily loaded from the store).
	SessionM *model.SessionM
	// TicketM is the associated ticket, when the session has one.
	TicketM *model.TicketM
	// Mentions is the list of @-mentioned users in the triggering message.
	Mentions []MentionedUser
	// InvokedSkills names the skills this request invoked explicitly, because
	// the user chose a skill command rather than typing a sentence. Empty is
	// the ordinary case.
	//
	// It is a field rather than an argument threaded through the turn because
	// it is a fact about the request, like Mentions: the turn already carries
	// this struct everywhere it needs to go.
	InvokedSkills []string
	// LarkServices provides outbound Lark actions; nil for non-Lark callers.
	LarkServices LarkServices

	// ResolvedAgents, when set, runs the scheduling pass this request would run
	// and answers what the conversation resolves to for this caller: the agents,
	// in the order they were settled on, the declaration they came from, and —
	// when there are none — why.
	//
	// # Why a callback
	//
	// For a conversation the answer is the scheduler's, and the scheduler lives in
	// the bot, which imports the command package and therefore cannot be imported
	// by it — so the bot hands the command this one call, the same way it hands
	// over its own Lark actions: a capability the caller has and the command
	// cannot reach. Handing over the answer the turn itself uses is what keeps the
	// menu a user sees and the agent that answers them from disagreeing.
	//
	// # Why it resolves rather than reads a recorded answer
	//
	// It used to read chat.session.agents, a column the scheduler wrote on every
	// message, and that column is gone. The saving it bought — not touching a
	// store while rendering a menu — was one indexed point read of chat.peer_agent,
	// and what it cost was a menu built from **the last speaker's** agents: in a
	// group, whoever clicked it saw the commands derived for somebody else. That is
	// not a stale answer, it is an answer about a different person, and no refresh
	// rate fixes it. So the pass runs here, per call, and it describes the caller.
	//
	// The cost is real and worth stating: this runs on every command tree build
	// (once per /command, once per card render, and a second time in
	// ValidateGranted). It reads chat.peer_agent once and writes nothing.
	//
	// An MCP call has no scheduler: the agent service states the agents it runs
	// on the wire, and the transport hands this back a fixed answer (FixedAgents).
	// It is the same question and the same field on purpose — one place names the
	// agents, so no two surfaces can come to filter by different ones.
	//
	// It is nil on a surface that cannot name an agent, and a nil or empty answer
	// means the same thing: no agent context. Grant is deny-by-default, so that
	// answer is an empty selection rather than an unfiltered one — a surface that
	// cannot name an agent has no granted commands, which is why the surfaces that
	// want the whole catalog without a conversation (the MCP bridge, the TUI tree)
	// build from NewCommandCatalogTree instead of asking this question.
	//
	// A failure is answered the same way as "no agent": the caller has no way to
	// name an agent, and deny-by-default is what that means. It is logged where it
	// happens rather than returned to a menu that could only render it as an empty
	// list anyway.
	ResolvedAgents func(ctx context.Context, dr *DynamicRuntime) (AgentResolution, error)
	// IsCardAction marks an invocation triggered by a card button.
	IsCardAction bool
}

// FixedAgents returns a ResolvedAgents answer that always names the same set.
//
// It is how an MCP call declares the agents it runs as: the agent service puts
// them on the wire and the transport hands them to the command tree as this one
// answer, where a conversation would otherwise consult its scheduler. The set is
// captured rather than looked up because an MCP caller has no scheduler to ask.
//
// It reports no declaration and no hint: an MCP caller was not resolved against
// one, and inventing either would be the surface answering a question about a
// conversation it does not have.
func FixedAgents(agents []string) func(context.Context, *DynamicRuntime) (AgentResolution, error) {
	return func(context.Context, *DynamicRuntime) (AgentResolution, error) {
		return AgentResolution{Agents: agents}, nil
	}
}

func (dr *DynamicRuntime) IsLark() bool { return dr.Source == RequestSourceLark }
func (dr *DynamicRuntime) IsAPI() bool  { return dr.Source == RequestSourceAPI }
func (dr *DynamicRuntime) IsMCP() bool  { return dr.Source == RequestSourceMCP }
func (dr *DynamicRuntime) IsTUI() bool  { return dr.Source == RequestSourceTUI }

// Identity is who the caller is: the tenant-scoped Lark user_id for somebody
// messaging the bot, the token's subject for a REST or MCP caller. It is empty
// when the caller has none this house can name — a sender from another tenant, say
// — and that empty value is the answer rather than a reason to reach for the
// open_id: an open_id is an address, and every authorization, record owner and
// allow-list in this service is keyed by identity.
//
// It is a method rather than a field because the answer has one definition and
// every caller used to spell it out: "the token's user, else the sender's id" was
// written at half a dozen call sites, two of them in the opposite order.
func (dr *DynamicRuntime) Identity() string {
	if dr == nil {
		return ""
	}
	return dr.Sender.Identity()
}

// Handle is the platform's own handle for the caller — empty on a surface whose
// platform reports none. It is not an identity: what is bound by handle (a peer
// binding) is looked up by it, and nothing is authorized by it.
func (dr *DynamicRuntime) Handle() string {
	if dr == nil {
		return ""
	}
	return dr.Sender.Handle()
}

// Address is where a reply to this call goes, or empty when it goes nowhere: a
// REST or MCP caller names a conversation it is acting for and has no place to be
// replied to. Nil-safe, because those surfaces carry no receiver at all.
func (dr *DynamicRuntime) Address() string {
	if dr == nil {
		return ""
	}
	return dr.Receiver.GetId()
}

// IsP2P and IsGroup report the kind of conversation this call came from. A call
// with no receiver is neither.
func (dr *DynamicRuntime) IsP2P() bool   { return dr != nil && dr.Receiver.IsP2P() }
func (dr *DynamicRuntime) IsGroup() bool { return dr != nil && dr.Receiver.IsGroup() }

// ChatType names that kind in the vocabulary the records use ("p2p"/"group").
func (dr *DynamicRuntime) ChatType() string {
	if dr == nil {
		return ""
	}
	return dr.Receiver.ChatType()
}

// ReceiverType is the namespace the address is sent in — "user_id", "open_id",
// "chat_id", or one of the surface markers ("api", "tui_client") for a caller
// that cannot be replied to. It is recorded rather than derived because a job
// remembers which surface created it.
func (dr *DynamicRuntime) ReceiverType() string {
	if dr == nil {
		return ""
	}
	return dr.Receiver.GetType()
}

// MessageID is the message (or card) this call came from — what a threaded reply
// or an in-place card update is addressed to.
func (dr *DynamicRuntime) MessageID() string {
	if dr == nil {
		return ""
	}
	return dr.Receiver.GetMessageId()
}

// ParentMessageID is the message the triggering one replies to, empty when it
// replies to nothing: it is what a quoted-message answer has to read.
//
// Nil-safe like the rest of these: a surface with no receiver has no thread
// either, and reading the receiver's fields directly is what made "this call
// cannot be replied to" a nil dereference on the MCP and TUI surfaces.
func (dr *DynamicRuntime) ParentMessageID() string {
	if dr == nil {
		return ""
	}
	return dr.Receiver.GetParentMessageId()
}

// HasLarkContext reports whether the invocation carries a usable Lark receiver
// and outbound services.
func (dr *DynamicRuntime) HasLarkContext() bool {
	return dr != nil && dr.Address() != "" && dr.LarkServices != nil
}

type contextKey struct{}

// Inject stores dr into the context.
func Inject(ctx context.Context, dr *DynamicRuntime) context.Context {
	return context.WithValue(ctx, contextKey{}, dr)
}

// From retrieves the DynamicRuntime from ctx. Never returns nil.
func From(ctx context.Context) *DynamicRuntime {
	if dr, ok := ctx.Value(contextKey{}).(*DynamicRuntime); ok && dr != nil {
		return dr
	}
	return &DynamicRuntime{Source: RequestSourceNone}
}
