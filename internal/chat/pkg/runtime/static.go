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
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/onexstack/onexai/internal/chat/duty"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset"
	"github.com/onexstack/onexai/internal/chat/pkg/dnswhitelist"
	"github.com/onexstack/onexai/internal/chat/pkg/i18n"
	"github.com/onexstack/onexai/internal/chat/pkg/roleauthz"
	"github.com/onexstack/onexai/internal/chat/pkg/roleset"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
	"github.com/onexstack/onexai/internal/chat/pkg/stream"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/pkg/wshub"
)

// EscalationStarter starts the escalation state for a new ticket.
type EscalationStarter interface {
	Start(ctx context.Context, ticket *model.TicketM)
}

// StaticRuntime holds long-lived, shared dependencies.
// One instance is created at server startup and reused across requests.
type StaticRuntime struct {
	// Duty is the duty/on-call capability: the native schedule service, with
	// the in-memory store kept behind it as the legacy Regen fallback.
	Duty duty.Service
	// Escalation starts a ticket's escalation when its group opts in. Nil
	// disables automatic starts; manual escalation is a REST action.
	Escalation EscalationStarter
	// Store is the persistence layer (sessions + tickets).
	Store store.IStore
	// Clientset provides access to cross-service clients (Lark + oai-agent-apiserver).
	Clientset clientset.Interface
	// CMDBEnabled turns on CMDB enrichment inside the resource commands. It is
	// opt-in and defaults to false: the CMDB code is kept, but /pod answers
	// from metrics alone until CMDB is ready.
	CMDBEnabled bool
	// DNSWhitelist is the in-memory DNS whitelist; nil when not configured.
	DNSWhitelist *dnswhitelist.Whitelist
	// Hub is the WebSocket hub used to mirror bot replies into the TUI.
	Hub *wshub.Hub
	// Sessions owns per-conversation state and serialises the turns of one
	// conversation. Nil disables serialisation, in which case two messages sent
	// close together run concurrently and race over the same streaming card.
	Sessions *session.Manager
	// Agents holds the backends a conversation can run on, and the default to
	// use for a new one. Nil disables every agent path, which fails the turn
	// with a report rather than a panic.
	Agents *agent.Registry
	// AgentCommands answers which commands a conversation's agents have. Nil
	// grants nothing, which is what a deployment with no rules gets anyway —
	// there is no second source of grants for a missing resolver to fall back
	// on.
	AgentCommands AgentCommandResolver
	// SkillRunner runs a skill command as a turn. Nil makes a skill command
	// visible but refused, which is the honest state of a command whose
	// executor was not wired — see bot.New, which sets it.
	SkillRunner SkillRunner
	// CustomRunner runs a user-defined command. Nil makes such a command
	// visible but refused, for the same reason as SkillRunner.
	CustomRunner CustomRunner
	// Streaming tunes how often a partially-generated answer is published to a
	// streaming card. It is consulted only when StreamingEnabled is true.
	Streaming stream.Config
	// StreamingEnabled false makes the card a placeholder that receives the
	// answer in one piece at the end of the turn. It is a separate field because
	// the zero value of stream.Config means "no throttle", not "off": a caller
	// that configures nothing wants every delta published, not none.
	StreamingEnabled bool
	// StreamingPrintFrequencyMs, StreamingPrintStep and StreamingPrintStrategy
	// are Feishu's client-side typing cadence for a streaming card: how often the
	// client reveals buffered characters, how many at a time, and whether it
	// favours staying current over rendering smoothly. Zero values mean the
	// platform defaults. They are display hints; the content sent is unaffected.
	StreamingPrintFrequencyMs int
	StreamingPrintStep        int
	StreamingPrintStrategy    string
	// MaxEventAge drops an inbound message delivered later than this after it was
	// sent. Zero disables the bound; see DedupOptions.MaxEventAgeSec.
	MaxEventAge time.Duration
	// PeerBindClaimTTL is how long a `/bind-onex <code>` claim stays readable by
	// the
	// business side. It must outlive the code it claims; see
	// options.PeerBindOptions.ClaimTTL.
	PeerBindClaimTTL time.Duration
	// AppID is the Lark App ID.
	AppID string
	// AppSecret is the Lark App secret, required to refresh a stale tenant
	// access token on demand. It must never be logged.
	AppSecret string
	// BotName is the bot display-name used to detect @-mentions of this bot.
	BotName string
	// Roles is who may run the restricted commands: the SRE list and the admin
	// list, read from chat.setting and refreshed in the background. Every SRE is
	// an admin, and an admin is not necessarily an SRE — the two roles gate
	// different commands.
	//
	// It is a concrete type on purpose: a nil *Cache stored in an interface
	// field would not compare equal to nil, and "no roles configured" has to
	// stay visible. A nil Roles grants nothing.
	//
	// Roles stays the roster even when RoleAuthz answers the questions below:
	// the rosters are what a rule's group subjects are derived from, and
	// Roles.Status() is what an operator reads to see them.
	Roles *roleset.Cache

	// RoleAuthz, when non-nil, answers the two role questions from the shared
	// authorization rule table instead of the lists (see internal/chat/pkg/roleauthz).
	// Nil keeps the list-only behaviour every deployment had before the switch
	// existed.
	RoleAuthz roleauthz.Resolver

	// AllowFrom is the set of sender identities (Lark user_ids, the same value
	// as the role lists) the bot responds to; empty means everyone.
	AllowFrom map[string]struct{}
	// AllowChat is the set of chat ids the bot responds in; empty means every chat.
	AllowChat map[string]struct{}
	// GroupOnly ignores private (P2P) chats when true.
	GroupOnly bool
	// ReplyToTrigger threads replies under the triggering message when true.
	ReplyToTrigger bool
	// ThreadIsolation derives per-thread sessions when true.
	ThreadIsolation bool
	// ImageBatchWindow is how long consecutive image messages are buffered before
	// they are merged into a single dispatch.
	ImageBatchWindow time.Duration
	// OutboundLimiter caps the rate of outbound Lark messages; nil disables
	// limiting.
	OutboundLimiter *rate.Limiter
	// Language is the language replies are written in. The zero value means the
	// source language, so a deployment that configures nothing is answered exactly
	// as it was before this field existed.
	Language i18n.Language

	// Async runs work that outlives the request that started it. Nil means
	// `go f()`, which is what a deployment wants.
	//
	// It exists because that is not what a test wants. A command whose work
	// continues after its reply has no place to return to, so a test that asserts
	// on the result has to either wait for a goroutine it cannot see or race it —
	// and this repository runs its tests under -race. A test replaces this with a
	// function that calls f and returns, which turns "eventually" into "before the
	// next assertion" without changing anything a deployment does.
	Async func(f func())

	// Oncall reads a ticket group's conversation. Nil disables the closing-summary
	// path, which is then simply not attempted — the ticket closes and its summary
	// stays empty, which is the same state a failed generation leaves behind.
	//
	// It is a narrow interface rather than the on-call biz because a command needs
	// exactly one of that package's methods: depending on the whole biz would make
	// the command layer's tests fake three methods to exercise one.
	Oncall OncallReader

	// InternalDefaultAgent and ExternalDefaultAgent are what a conversation's
	// `@default` reference resolves to, picked by whether the speaker belongs to
	// this tenant.
	//
	// Either may itself be a reference (`@peer`), not only a name — the
	// scheduler resolves whatever it finds. ExternalDefaultAgent is empty by
	// default, which resolves to no agent at all and is reported to the speaker
	// rather than silently replaced by the internal one.
	InternalDefaultAgent string
	ExternalDefaultAgent string
	// AgentAllocator names the strategy that picks the final set from the
	// resolved candidates. Empty means the first strategy the build ships.
	AgentAllocator string

	// DutyMaintainerUserID is the Lark user_id shown as the fallback on-caller on
	// the /duty card. Display-only; empty leaves the slot blank.
	DutyMaintainerUserID string
	// P4DeferGroups is the set of lower-cased module codes whose P4 tickets do
	// not page the on-call up front; /level invites them on escalation.
	P4DeferGroups map[string]struct{}
}

// IsP4DeferOncall reports whether a module defers paging the on-call for P4
// tickets. A nil runtime — which a command's own tests construct — answers
// false rather than panicking.
func (r *StaticRuntime) IsP4DeferOncall(groupCode string) bool {
	if r == nil || len(r.P4DeferGroups) == 0 {
		return false
	}
	_, ok := r.P4DeferGroups[strings.ToLower(strings.TrimSpace(groupCode))]
	return ok
}

// Translate renders a source string in this deployment's language.
//
// It is a method rather than a call to i18n.T because every command already
// holds the static runtime, and a nil runtime — which a command's own tests
// construct — has to answer in the source language rather than panic.
func (r *StaticRuntime) Translate(source string, args ...any) string {
	lang := i18n.Default
	if r != nil && r.Language != "" {
		lang = r.Language
	}
	return i18n.T(lang, source, args...)
}

// UserError builds a user-facing error in this deployment's language.
//
// It exists as a method because NewUserError takes a format string, and the
// translated text is not one: passing it as the format would interpret any
// percent sign a translation happens to contain. Commands reply with this;
// NewUserError stays for the errors that carry no translatable text.
func (r *StaticRuntime) UserError(source string, args ...any) *UserError {
	return NewUserError("%s", r.Translate(source, args...))
}

// IsSREUser reports whether the given Lark user_id is an SRE team member.
//
// A nil runtime answers false: r == nil means the server failed to build its
// runtime, not that the user has no role, and granting permission there would
// turn a broken deployment into an open one. A user_id the list does not name
// answers false too — the role lists restrict, they do not merely enumerate.
func (r *StaticRuntime) IsSREUser(userID string) bool {
	if r == nil || userID == "" {
		return false
	}
	if r.RoleAuthz != nil {
		return r.RoleAuthz.IsSREUser(userID)
	}
	return r.Roles.IsSREUser(userID)
}

// IsAdminUser reports whether the given Lark user_id may run admin-only
// commands. An admin is whoever the deployment listed plus every SRE; the union
// is maintained by the cache that holds the lists, so this is one lookup rather
// than two.
//
// A nil Roles answers false, which is the same direction as a nil runtime: an
// unwired list grants nobody.
func (r *StaticRuntime) IsAdminUser(userID string) bool {
	if r == nil || userID == "" {
		return false
	}
	if r.RoleAuthz != nil {
		return r.RoleAuthz.IsAdminUser(userID)
	}
	return r.Roles.IsAdminUser(userID)
}

// AllowsSender reports whether the given sender identity (a Lark user_id) may
// trigger the bot. An empty allow-list means everyone is allowed.
func (r *StaticRuntime) AllowsSender(userID string) bool {
	if r == nil || len(r.AllowFrom) == 0 {
		return true
	}
	_, ok := r.AllowFrom[userID]
	return ok
}

// AllowsChat reports whether the given chat id may trigger the bot. An empty
// allow-list means every chat is allowed.
func (r *StaticRuntime) AllowsChat(chatID string) bool {
	if r == nil || len(r.AllowChat) == 0 {
		return true
	}
	_, ok := r.AllowChat[chatID]
	return ok
}
