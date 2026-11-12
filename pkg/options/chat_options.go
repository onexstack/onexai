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

package options

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/onexstack/onexai/pkg/known/chat"
)

// ChatOptions holds the session, streaming and agent-backend tuning for
// oai-chat-apiserver.
//
// Every default reproduces the behaviour the server had before these knobs
// existed, so a YAML file with no "chat:" block starts the server exactly
// as it started before. The block is opt-in tuning, not a required section.
type ChatOptions struct {
	// Session configures per-conversation lifecycle: how long an idle
	// conversation survives, how long a turn may run, and how many messages may
	// queue behind a running turn.
	Session SessionOptions `json:"session" mapstructure:"session"`
	// Streaming configures the incremental preview of a running answer.
	Streaming StreamingOptions `json:"streaming" mapstructure:"streaming"`
	// Progress configures the live tool-progress panel.
	Progress ProgressOptions `json:"progress" mapstructure:"progress"`
	// Dedup configures inbound event de-duplication.
	Dedup DedupOptions `json:"dedup" mapstructure:"dedup"`
	// WebSocket configures the browser-facing conversation socket.
	WebSocket WebSocketOptions `json:"websocket" mapstructure:"websocket"`
	// AgentCommand configures the snapshots behind the per-agent command
	// rules (AgentBinding objects, projected by the chat resolver).
	AgentCommand AgentCommandOptions `json:"agent-command" mapstructure:"agent-command"`
	// PeerBind configures the Feishu account-binding handshake.
	PeerBind PeerBindOptions `json:"peer-bind" mapstructure:"peer-bind"`
	// Agents selects and configures the agent backends a conversation may run.
	Agents AgentsOptions `json:"agents" mapstructure:"agents"`
	// I18n selects the language the service answers in.
	I18n I18nOptions `json:"i18n" mapstructure:"i18n"`
	// Login gates the credential providers the public /v1/auth/login endpoint
	// accepts. Every provider is off by default.
	Login LoginOptions `json:"login" mapstructure:"login"`
	// OIDC configures the OIDC-compatible access token issued alongside the
	// browser token. Off by default; when enabled it also publishes the
	// discovery and JWKS endpoints a Kubernetes authenticator consumes.
	OIDC OIDCOptions `json:"oidc" mapstructure:"oidc"`
	// Dissolve configures the dissolution of closed ticket groups.
	Dissolve DissolveOptions `json:"dissolve" mapstructure:"dissolve"`

	// RoleAuthz switches the two role questions ("is this identity an SRE?",
	// "is this identity an administrator?") from the chat.setting lists to the
	// shared authorization rule table, so a deployment can express role
	// membership in the same rules as everything else. Off by default: an
	// enabled switch with no rules refuses every role, which is the correct
	// fail-closed answer but not what an unconfigured deployment expects.
	RoleAuthz RoleAuthzOptions `json:"role-authz" mapstructure:"role-authz"`

	// InternalDefault and ExternalDefault are what a conversation's `@default`
	// reference resolves to, chosen by whether the speaker belongs to this
	// tenant. See internal/chat/biz/v1/bot/schedule.go for the resolution.
	//
	// # They are references, not names
	//
	// `helper` is a concrete agent; `@peer` is itself a decision. Both are
	// accepted, because the resolution pipeline treats a literal name and a
	// reference as the same kind of thing — there is no second code path for the
	// second form.
	//
	// # Why the external one defaults to empty
	//
	// InternalDefault keeps the value this service hardcoded before the setting
	// existed, so an unconfigured deployment answers exactly as it did.
	// ExternalDefault defaults to **nothing**: an external speaker then resolves
	// to no agent at all and gets a card saying so, rather than being handed the
	// internal assistant. An empty default is the failure that is visible;
	// inheriting the internal one is the failure that is not.
	InternalDefault string `json:"internal-default" mapstructure:"internal-default"`
	ExternalDefault string `json:"external-default" mapstructure:"external-default"`

	// AgentAllocator names the strategy that picks the final agent set from the
	// resolved candidates. See internal/chat/biz/v1/bot/allocator.go; an unknown
	// name fails at startup rather than at the first message.
	AgentAllocator string `json:"agent-allocator" mapstructure:"agent-allocator"`

	// InternalCredentials authenticates the services that write chat-side
	// state on behalf of their own users.
	//
	// # Why a list and not one shared secret
	//
	// The peer-binding endpoints answer "which chat identity is this business
	// account", and the writer has to be attributable for that answer to mean
	// anything: `chat.peer_agent.source` records which credential wrote a row,
	// and a single shared secret would make every row say the same thing. It
	// also means revoking one integrator does not revoke the others.
	//
	// Empty disables the endpoints entirely, which is the state of a deployment
	// that has no such integrator — see the middleware, which refuses rather
	// than allowing when nothing is configured.
	InternalCredentials []InternalCredential `json:"internal-credentials" mapstructure:"internal-credentials"`
}

// InternalCredential is one service's right to write chat-side peer state.
type InternalCredential struct {
	// Secret is the bearer value the caller presents. It is compared in
	// constant time; nothing logs it.
	Secret string `json:"secret" mapstructure:"secret"`
	// Subject names the caller, and is what lands in chat.peer_agent.source.
	// It is also the identity the caller is authorized as, so it must be the
	// same string the agent service's rules name.
	Subject string `json:"subject" mapstructure:"subject"`
}

// LoginOptions gates the /v1/auth/login credential providers.
//
// # What changed with the account table
//
// local used to accept any non-empty username and password, because there was
// no user table to check them against; it was therefore an authentication
// bypass and defaulted off. There is now a real credential store (chat.user,
// bcrypt hashes), so local login verifies the pair it is given and can default
// on. Registration is a separate switch and defaults on for the same reason:
// the account it creates is a real one with a real password.
//
// The other two providers remain the old "opaque identity" behaviour and stay
// off. lark_token is not verified against Feishu; the browser-facing Feishu
// login is the OAuth flow in lark.oauth-redirect-uri, which proves the identity
// by exchanging a one-time code.
type LoginOptions struct {
	// AllowLocal enables password login and password registration.
	AllowLocal bool `json:"allow-local" mapstructure:"allow-local"`
	// AllowRegistration enables POST /v1/auth/register. A deployment that wants
	// to create accounts by hand can close it.
	AllowRegistration bool `json:"allow-registration" mapstructure:"allow-registration"`
	// BootstrapAdmin grants the administrator role to the first account created
	// while chat.user is empty. It is off by default: with open registration and
	// an exposed endpoint, "first one wins" is a race an attacker can win. A
	// deployment that turns it on does so deliberately, usually on a developer
	// machine or before the endpoint is exposed.
	BootstrapAdmin bool `json:"bootstrap-admin" mapstructure:"bootstrap-admin"`
	// AllowLark enables the legacy provider=lark. It treats the supplied
	// lark_token as an opaque identity without verifying it against the Lark
	// API, and stays off by default.
	AllowLark bool `json:"allow-lark" mapstructure:"allow-lark"`
	// AllowAPIKey enables provider=apikey. It accepts any non-empty api_key;
	// there is no key registry in this service to check it against.
	AllowAPIKey bool `json:"allow-apikey" mapstructure:"allow-apikey"`

	// TokenTTL is how long an issued token stays valid. Empty means 24h, which
	// is what the service has always used.
	TokenTTL time.Duration `json:"token-ttl" mapstructure:"token-ttl"`
}

// I18nOptions selects the language of the replies the service sends.
//
// Only the strings the service has translations for are affected; everything
// else is sent as it is written in the code. That makes an unlisted language
// harmless — it changes nothing — rather than a configuration that answers in
// empty messages.
type I18nOptions struct {
	// Language is the language replies are written in: zh (the source language)
	// or en. Empty means zh, which is what a deployment that never sets this
	// field has always received.
	Language string `json:"language" mapstructure:"language"`
}

// Languages the service can answer in. They mirror the tags understood by
// internal/chat/pkg/i18n; the two lists are checked against each other by
// TestLanguageOptionsMatchTheTranslator, so a tag added to one and not the other
// fails a test rather than reaching production as a config value that validates
// and then translates to nothing.
const (
	// LanguageZH is Simplified Chinese, the source language of this repository.
	LanguageZH = "zh"
	// LanguageZHTW is Traditional Chinese. It has no catalog of its own yet and
	// falls back to Simplified Chinese.
	LanguageZHTW = "zh-TW"
	// LanguageEN is English.
	LanguageEN = "en"
)

// SessionOptions configures conversation and turn lifecycle.
type SessionOptions struct {
	// IdleTimeout is how long a conversation may sit untouched before its
	// resources are released.
	IdleTimeout time.Duration `json:"idle-timeout" mapstructure:"idle-timeout"`
	// EventIdleTimeout is the longer bound applied to conversations that are
	// still receiving events but not producing turns.
	EventIdleTimeout time.Duration `json:"event-idle-timeout" mapstructure:"event-idle-timeout"`
	// CloseTimeout bounds the graceful shutdown of a conversation's backend.
	CloseTimeout time.Duration `json:"close-timeout" mapstructure:"close-timeout"`
	// BusyStaleLockMaxHeld is how long a busy lock may be held before another
	// message may break it, protecting users from a wedged turn.
	BusyStaleLockMaxHeld time.Duration `json:"busy-stale-lock-max-held" mapstructure:"busy-stale-lock-max-held"`
	// MaxQueuedMessages is how many messages may wait behind a running turn
	// before further messages are rejected with a "queue full" notice.
	MaxQueuedMessages int `json:"max-queued-messages" mapstructure:"max-queued-messages"`
	// MaxConcurrentTurns is how many turns may run at once in one conversation.
	// 1 serialises turns and queues the overflow; N>1 runs up to N concurrently
	// and queues only what arrives beyond them.
	MaxConcurrentTurns int `json:"max-concurrent-turns" mapstructure:"max-concurrent-turns"`
	// TurnTimeout bounds a single AI turn.
	TurnTimeout time.Duration `json:"turn-timeout" mapstructure:"turn-timeout"`
	// DataDir is where conversation snapshots are written. Empty means the
	// server's home directory (<home>/.onexai).
	DataDir string `json:"data-dir" mapstructure:"data-dir"`
}

// StreamingOptions configures the incremental preview of a running answer.
//
// The two thresholds are ANDed, not ORed: a flush happens only once both the
// interval has elapsed and at least MinDeltaChars of new text have accumulated.
// That keeps a long but slow answer from producing a flurry of near-identical
// card updates.
type StreamingOptions struct {
	// Enabled turns incremental previewing on. When false the answer is written
	// once, at the end of the turn.
	Enabled bool `json:"enabled" mapstructure:"enabled"`
	// Interval is the minimum time between two updates of the same sink.
	Interval time.Duration `json:"interval" mapstructure:"interval"`
	// MinDeltaChars is the minimum number of new characters required to flush.
	MinDeltaChars int `json:"min-delta-chars" mapstructure:"min-delta-chars"`
	// MaxChars bounds the previewed text; beyond it the preview stops growing
	// and only the final render carries the full answer.
	MaxChars int `json:"max-chars" mapstructure:"max-chars"`
	// PrintFrequencyMs is the cardkit typing cadence hint sent to Feishu.
	PrintFrequencyMs int `json:"print-frequency-ms" mapstructure:"print-frequency-ms"`
	// PrintStep is the cardkit characters-per-tick hint sent to Feishu.
	PrintStep int `json:"print-step" mapstructure:"print-step"`
	// PrintStrategy is the cardkit rendering strategy hint ("fast" or "delay").
	PrintStrategy string `json:"print-strategy" mapstructure:"print-strategy"`
}

// ProgressOptions configures the live tool-progress panel shown while a turn
// runs.
type ProgressOptions struct {
	// Style selects the progress rendering; currently only "card".
	Style string `json:"style" mapstructure:"style"`
	// MaxEntries caps how many tool steps the panel lists.
	MaxEntries int `json:"max-entries" mapstructure:"max-entries"`
	// Interval is the minimum time between two progress panel updates.
	Interval time.Duration `json:"interval" mapstructure:"interval"`
}

// DedupOptions configures inbound event de-duplication. Platforms redeliver
// events on transport hiccups, and a redelivered prompt would otherwise run
// twice.
// WebSocketOptions configures the browser-facing conversation socket.
//
// # Why an origin allowlist
//
// A WebSocket handshake is not covered by the browser's same-origin policy:
// without a check, any page can open a socket to this server with the user's
// cookies. The historical behaviour was to accept every origin; an empty list
// keeps that behaviour so an upgrade does not break a deployment whose console
// lives on another origin, while configuring the list closes the hole. Requests
// with no Origin header (CLI clients) are always accepted.
type WebSocketOptions struct {
	// AllowedOrigins lists the origins a browser may connect from. Entries may
	// be full origins ("https://console.example.com") or bare hosts
	// ("console.example.com"). Empty accepts every origin.
	AllowedOrigins []string `json:"allowed-origins" mapstructure:"allowed-origins"`
	// MaxConcurrentFrames bounds the frames the hub handles at once. A frame
	// handler dispatches work and returns; the bound exists so a burst cannot
	// create an unbounded number of goroutines.
	MaxConcurrentFrames int `json:"max-concurrent-frames" mapstructure:"max-concurrent-frames"`
}

type DedupOptions struct {
	// Enabled turns de-duplication on.
	Enabled bool `json:"enabled" mapstructure:"enabled"`
	// TTLSec is how long a seen event ID is remembered.
	TTLSec int `json:"ttl-sec" mapstructure:"ttl-sec"`
	// MaxEntries bounds the dedup cache so a busy bot cannot grow it without
	// limit.
	MaxEntries int `json:"max-entries" mapstructure:"max-entries"`
	// MaxEventAgeSec drops an inbound message that the platform delivered late:
	// a message older than this many seconds is not answered. Feishu re-delivers
	// events after a websocket reconnect, so a bot that has been down can be
	// handed a backlog whose questions were already answered elsewhere. Zero
	// disables the bound.
	MaxEventAgeSec int `json:"max-event-age-sec" mapstructure:"max-event-age-sec"`
}

// AgentCommandOptions configures the lifetimes of the snapshots the command
// resolver keeps: which commands an agent was granted (the rules in
// chat.agent_command), an agent's labels, and an agent's skill commands.
//
// # What the TTLs buy
//
// A resolve runs on every message and may not touch a database, so each of the
// three answers is read into a snapshot and reused; the TTL is how long a
// change may go unnoticed on a replica that did not serve the write. They are
// configured rather than compiled in because they trade freshness against load
// on a different service, and the right point on that curve is a property of
// the deployment — a single-replica host that is being edited by hand wants a
// shorter rule TTL than a fleet whose rules change once a month.
//
// The defaults are the agentcmd package's own constants rather than numbers
// repeated here, so the two cannot drift.
type AgentCommandOptions struct {
	// RulesTTL bounds how long a change to a command rule goes unnoticed on a
	// replica that did not serve the write.
	RulesTTL time.Duration `json:"rules-ttl" mapstructure:"rules-ttl"`
	// There is no agent TTL: the agents a rule selects on come from the control
	// plane's cache, which a watch keeps current. A knob here would only let a
	// deployment reintroduce staleness the architecture has removed.
	// SkillsTTL bounds how long an agent's list of skill commands can be stale.
	// It is the longest because enumerating skills is the most expensive read.
	SkillsTTL time.Duration `json:"skills-ttl" mapstructure:"skills-ttl"`
	// CustomsTTL bounds how long an agent's list of user-defined commands can be
	// stale. It is short because binding a command is followed immediately by
	// someone testing it, and the read is a cheap control-plane call.
	CustomsTTL time.Duration `json:"customs-ttl" mapstructure:"customs-ttl"`
}

// PeerBindOptions configures the Feishu account-binding handshake.
//
// The handshake has two halves and this service owns one of them: a user sends
// the binding code the console showed them to the bot as `/bind-onex <code>`,
// and
// this service parks that code next to the chat identity that spoke it, in
// chat.peer_bind_claim. The business side (OneX's iam) then polls for the claim
// and does the binding itself.
type PeerBindOptions struct {
	// ClaimTTL is how long a spoken code stays readable by the business side.
	//
	// # It must outlive the code it claims
	//
	// iam mints the code and keeps its own copy (feishu.code-ttl, 10m by
	// default); this value must be **strictly greater** than that. The console
	// polls iam for as long as iam's code lives, so a claim that expires first
	// does not fail loudly — iam reads Get() == nil and reports an `expired`
	// binding that the user did in fact speak, with nothing logged anywhere.
	// The default is iam's 10m plus five minutes of slack.
	ClaimTTL time.Duration `json:"claim-ttl" mapstructure:"claim-ttl"`
}

// AgentsOptions selects and configures the agent backends.
type AgentsOptions struct {
	// DefaultBackend is the backend a new conversation uses when the request
	// does not name one: "llm" (the oai-agent-apiserver agent) or "cli" (a local
	// agent CLI process).
	DefaultBackend string `json:"default-backend" mapstructure:"default-backend"`
	// CLI configures the local agent-CLI backend.
	CLI CLIAgentOptions `json:"cli" mapstructure:"cli"`
}

// CLIAgentOptions configures the local agent-CLI backend.
//
// This backend lets a chat conversation drive a process on the server host.
// It is off by default and must be enabled deliberately: everything in this
// struct is part of the trust boundary once it is on.
type CLIAgentOptions struct {
	// Enabled turns the backend on. While false, no CLI session can be created
	// and the backend is not registered.
	Enabled bool `json:"enabled" mapstructure:"enabled"`
	// MaxSessions caps concurrently live CLI sessions.
	MaxSessions int `json:"max-sessions" mapstructure:"max-sessions"`
	// WorkspaceRoot is the directory under which each conversation gets its own
	// subdirectory. Required when Enabled.
	WorkspaceRoot string `json:"workspace-root" mapstructure:"workspace-root"`
	// AllowedDirs are extra directories a conversation may point its working
	// directory at, in addition to its own workspace.
	AllowedDirs []string `json:"allowed-dirs" mapstructure:"allowed-dirs"`
	// IdleTimeout is how long a CLI session may sit idle before the process is
	// reaped.
	IdleTimeout time.Duration `json:"idle-timeout" mapstructure:"idle-timeout"`
	// TurnTimeout bounds a single CLI turn.
	TurnTimeout time.Duration `json:"turn-timeout" mapstructure:"turn-timeout"`
	// PermissionMode is how the CLI handles tool permission prompts: "stdio"
	// (interactive, answered from chat) or "skip" (bypass all prompts).
	PermissionMode string `json:"permission-mode" mapstructure:"permission-mode"`
	// AllowedTools, when non-empty, is the only set of tools the CLI may use.
	AllowedTools []string `json:"allowed-tools" mapstructure:"allowed-tools"`
	// DisallowedTools is removed from the tool set after AllowedTools applies.
	DisallowedTools []string `json:"disallowed-tools" mapstructure:"disallowed-tools"`
	// Model overrides the model the CLI backend runs.
	Model string `json:"model" mapstructure:"model"`
	// MaxLineBytes bounds a single line of the CLI's event stream. It must clear
	// the largest tool result the backend can emit (a large file read or a
	// JSON-formatted API response), or the session dies mid-turn.
	MaxLineBytes int `json:"max-line-bytes" mapstructure:"max-line-bytes"`
}

// Permission modes for the local agent-CLI backend.
const (
	// CLIPermissionModeStdio answers tool permission prompts over the CLI's
	// stdio control channel, so approval is routed to the user. This is the
	// default.
	CLIPermissionModeStdio = "stdio"
	// CLIPermissionModeSkip bypasses all permission prompts. It is an explicit
	// opt-in and grants the agent unattended write access to the workspace.
	CLIPermissionModeSkip = "skip"
)

// Agent backend names.
const (
	// AgentBackendLLM is the oai-agent-apiserver agent.
	AgentBackendLLM = "llm"
	// AgentBackendCLI is a local agent-CLI process.
	AgentBackendCLI = "cli"
)

// NewChatOptions creates a ChatOptions populated with defaults that
// reproduce the behaviour of the server before these options existed.
// RoleAuthzOptions configures policy-backed role resolution.
type RoleAuthzOptions struct {
	// Enabled reads role membership from AuthorizationRule objects
	// (resource_kind "role") instead of the chat.setting lists. The lists stay
	// the roster the group subjects are derived from — see
	// internal/chat/pkg/roleauthz.
	Enabled bool `json:"enabled" mapstructure:"enabled"`
	// RuleTableRefreshInterval is the fallback re-read interval. Rule changes
	// arrive through the control plane's watch; this only covers a watch that
	// silently stops delivering. A value <= 0 keeps the default.
	RuleTableRefreshInterval time.Duration `json:"rule-table-refresh-interval" mapstructure:"rule-table-refresh-interval"`
}

// DefaultRoleAuthzRefreshInterval is how often a policy-backed deployment
// re-reads the rule table. It matches the agent service's own default so the two
// processes converge within the same window.
const DefaultRoleAuthzRefreshInterval = time.Minute

func NewChatOptions() *ChatOptions {
	return &ChatOptions{
		OIDC: OIDCOptions{TokenTTL: time.Hour},
		Session: SessionOptions{
			IdleTimeout:          15 * time.Minute,
			EventIdleTimeout:     2 * time.Hour,
			CloseTimeout:         150 * time.Second,
			BusyStaleLockMaxHeld: 2 * time.Minute,
			MaxQueuedMessages:    5,
			MaxConcurrentTurns:   3,
			TurnTimeout:          5 * time.Minute,
		},
		Streaming: StreamingOptions{
			Enabled:          true,
			Interval:         1500 * time.Millisecond,
			MinDeltaChars:    30,
			MaxChars:         2000,
			PrintFrequencyMs: 50,
			PrintStep:        2,
			PrintStrategy:    "fast",
		},
		Progress: ProgressOptions{
			Style:      "card",
			MaxEntries: 10,
			Interval:   250 * time.Millisecond,
		},
		Dedup: DedupOptions{
			Enabled:        true,
			TTLSec:         300,
			MaxEntries:     4096,
			MaxEventAgeSec: 7200,
		},
		WebSocket: WebSocketOptions{
			MaxConcurrentFrames: 64,
		},
		// Taken from pkg/known/chat, which the resolver also reads, so this file
		// and the resolver's own fallback cannot disagree about "the default".
		AgentCommand: AgentCommandOptions{
			RulesTTL:   chat.DefaultRulesTTL,
			SkillsTTL:  chat.DefaultSkillsTTL,
			CustomsTTL: chat.DefaultCustomsTTL,
		},
		PeerBind: PeerBindOptions{
			// iam's feishu.code-ttl is 10m; see ClaimTTL for why this must be
			// the larger of the two.
			ClaimTTL: 15 * time.Minute,
		},
		Agents: AgentsOptions{
			DefaultBackend: AgentBackendLLM,
			CLI: CLIAgentOptions{
				Enabled:        false,
				MaxSessions:    4,
				IdleTimeout:    15 * time.Minute,
				TurnTimeout:    5 * time.Minute,
				PermissionMode: CLIPermissionModeStdio,
				MaxLineBytes:   8 << 20,
			},
		},
		I18n: I18nOptions{
			Language: LanguageZH,
		},
		// Local login and registration verify a real bcrypt credential against
		// chat.user, so they start on. The two opaque-identity providers stay off;
		// see LoginOptions.
		Login: LoginOptions{
			AllowLocal:        true,
			AllowRegistration: true,
			TokenTTL:          24 * time.Hour,
		},
		// The value this service hardcoded before the setting existed, so an
		// unconfigured deployment resolves a default-agent conversation exactly
		// as it always did. ExternalDefault is deliberately left empty: see the
		// field's comment — the safe failure is the visible one.
		InternalDefault: DefaultAgentName,
		ExternalDefault: "",
		AgentAllocator:  chat.AllocatorFirst,
		RoleAuthz: RoleAuthzOptions{
			// Off: the lists are what every deployment had, and enabling this
			// without writing rules refuses every role.
			Enabled:                  false,
			RuleTableRefreshInterval: DefaultRoleAuthzRefreshInterval,
		},
	}
}

// DefaultAgentName is the built-in agent a conversation falls back to when
// nothing else applies. It was a literal in this file until the name gained a
// namespace; it now comes from the package that owns the vocabulary, so the
// configuration default and the code that compares against it cannot drift.
const DefaultAgentName = chat.DefaultAgentName

// Validate checks the ChatOptions fields.
func (o *ChatOptions) Validate() []error {
	var errs []error

	if o.Session.TurnTimeout <= 0 {
		errs = append(errs, fmt.Errorf("chat.session.turn-timeout must be positive, got %s", o.Session.TurnTimeout))
	}
	if o.Session.IdleTimeout <= 0 {
		errs = append(errs, fmt.Errorf("chat.session.idle-timeout must be positive, got %s", o.Session.IdleTimeout))
	}
	if o.Session.MaxQueuedMessages < 0 {
		errs = append(errs, fmt.Errorf("chat.session.max-queued-messages must not be negative, got %d", o.Session.MaxQueuedMessages))
	}
	if o.Session.MaxConcurrentTurns < 1 {
		errs = append(errs, fmt.Errorf("chat.session.max-concurrent-turns must be at least 1, got %d", o.Session.MaxConcurrentTurns))
	}

	if o.Login.TokenTTL <= 0 {
		errs = append(errs, fmt.Errorf("chat.login.token-ttl must be positive, got %s", o.Login.TokenTTL))
	}
	if o.AgentAllocator == "" {
		// The zero value is reachable from a hand-written config file, and an
		// empty strategy name would fail at the first message instead of here.
		o.AgentAllocator = chat.AllocatorFirst
	}
	if !slices.Contains(chat.Allocators, o.AgentAllocator) {
		errs = append(errs, fmt.Errorf("chat.agent-allocator must be one of %v, got %q", chat.Allocators, o.AgentAllocator))
	}
	if strings.HasPrefix(o.ExternalDefault, "@") || strings.HasPrefix(o.InternalDefault, "@") {
		// A reference here is legitimate — `@peer` means "whoever is speaking".
		// What is not legitimate is a reference to `@default`, which would send
		// the resolution back to itself; see bot/ref.go, where the depth bound
		// also catches it, but not until a message arrives.
		if o.ExternalDefault == "@default" || o.InternalDefault == "@default" {
			errs = append(errs, fmt.Errorf("chat.(internal|external)-default must not reference @default itself"))
		}
	}

	if o.Dedup.MaxEventAgeSec < 0 {
		errs = append(errs, fmt.Errorf("chat.dedup.max-event-age-sec must not be negative, got %d", o.Dedup.MaxEventAgeSec))
	}

	// A non-positive claim lifetime is refused rather than defaulted: it would
	// stamp ExpiresAt = now on every claim, so iam would read nil and report
	// "expired" for a binding the user had just spoken. Nobody would see an
	// error anywhere — the failure is that binding silently stops working.
	if o.PeerBind.ClaimTTL <= 0 {
		errs = append(errs, fmt.Errorf("chat.peer-bind.claim-ttl must be positive, got %s", o.PeerBind.ClaimTTL))
	}

	if o.Streaming.Interval <= 0 {
		errs = append(errs, fmt.Errorf("chat.streaming.interval must be positive, got %s", o.Streaming.Interval))
	}
	if o.Streaming.MinDeltaChars < 1 {
		errs = append(errs, fmt.Errorf("chat.streaming.min-delta-chars must be at least 1, got %d", o.Streaming.MinDeltaChars))
	}
	if o.Streaming.MaxChars < o.Streaming.MinDeltaChars {
		errs = append(errs, fmt.Errorf("chat.streaming.max-chars (%d) must be >= min-delta-chars (%d)",
			o.Streaming.MaxChars, o.Streaming.MinDeltaChars))
	}

	if o.Dedup.TTLSec < 0 {
		errs = append(errs, fmt.Errorf("chat.dedup.ttl-sec must not be negative, got %d", o.Dedup.TTLSec))
	}
	if o.Dedup.MaxEntries < 0 {
		errs = append(errs, fmt.Errorf("chat.dedup.max-entries must not be negative, got %d", o.Dedup.MaxEntries))
	}

	// Non-positive snapshot lifetimes are refused rather than defaulted, for the
	// same reason as the peer-bind claim above: the resolver reads <= 0 as "use
	// the compiled-in default", so a deployment that wrote 0 would get its
	// configured intent silently replaced instead of told about it.
	for name, ttl := range map[string]time.Duration{
		"chat.agent-command.rules-ttl":   o.AgentCommand.RulesTTL,
		"chat.agent-command.skills-ttl":  o.AgentCommand.SkillsTTL,
		"chat.agent-command.customs-ttl": o.AgentCommand.CustomsTTL,
	} {
		if ttl <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive, got %s", name, ttl))
		}
	}

	switch o.Agents.DefaultBackend {
	case AgentBackendLLM, AgentBackendCLI:
	case "":
		errs = append(errs, fmt.Errorf("chat.agents.default-backend must not be empty (want %s|%s)",
			AgentBackendLLM, AgentBackendCLI))
	default:
		errs = append(errs, fmt.Errorf("chat.agents.default-backend %q is invalid (want %s|%s)",
			o.Agents.DefaultBackend, AgentBackendLLM, AgentBackendCLI))
	}

	errs = append(errs, o.Agents.CLI.Validate()...)

	// An unknown language is refused rather than silently falling back to the
	// source language: a deployment that asked for a language this service cannot
	// speak would otherwise discover it from a user who cannot read the replies.
	// An empty value is spelled out separately because it is not unknown — it is
	// the documented spelling of "the source language".
	switch o.I18n.Language {
	case "", LanguageZH, LanguageZHTW, LanguageEN:
	default:
		errs = append(errs, fmt.Errorf("chat.i18n.language %q is invalid (want %s|%s|%s)",
			o.I18n.Language, LanguageZH, LanguageZHTW, LanguageEN))
	}

	return errs
}

// Validate checks the CLIAgentOptions fields. An enabled CLI backend is
// constrained hard: it is the only configuration that lets a chat message
// execute a local process, so a half-configured backend is refused at startup
// rather than failing open at request time.
func (o *CLIAgentOptions) Validate() []error {
	var errs []error

	switch o.PermissionMode {
	case CLIPermissionModeStdio, CLIPermissionModeSkip:
	default:
		errs = append(errs, fmt.Errorf("chat.agents.cli.permission-mode %q is invalid (want %s|%s)",
			o.PermissionMode, CLIPermissionModeStdio, CLIPermissionModeSkip))
	}

	if !o.Enabled {
		return errs
	}

	if o.WorkspaceRoot == "" {
		errs = append(errs, fmt.Errorf("chat.agents.cli.workspace-root is required when agents.cli.enabled is true"))
	} else if !filepath.IsAbs(o.WorkspaceRoot) {
		errs = append(errs, fmt.Errorf("chat.agents.cli.workspace-root %q must be an absolute path", o.WorkspaceRoot))
	}
	if o.MaxSessions < 1 {
		errs = append(errs, fmt.Errorf("chat.agents.cli.max-sessions must be at least 1, got %d", o.MaxSessions))
	}
	// A too-small line buffer kills a session the first time a tool returns a
	// large result, which is a confusing failure to debug; refuse it up front.
	if o.MaxLineBytes < 64<<10 {
		errs = append(errs, fmt.Errorf("chat.agents.cli.max-line-bytes must be at least %d, got %d",
			64<<10, o.MaxLineBytes))
	}
	if o.TurnTimeout <= 0 {
		errs = append(errs, fmt.Errorf("chat.agents.cli.turn-timeout must be positive, got %s", o.TurnTimeout))
	}
	if o.IdleTimeout <= 0 {
		errs = append(errs, fmt.Errorf("chat.agents.cli.idle-timeout must be positive, got %s", o.IdleTimeout))
	}

	return errs
}

// AddFlags adds chat-related flags to the given FlagSet.
// DissolveOptions configures the sweep that dissolves closed ticket groups.
//
// # Off by default, and that is the point
//
// Dissolving cannot be undone, and Lark keeps no history for a group it
// dissolved — the messages are gone from the platform the moment the call
// returns. A deployment therefore opts into this having decided that its
// closed groups should go, rather than discovering after an upgrade that they
// are going.
type DissolveOptions struct {
	// Enabled turns the sweep on.
	Enabled bool `json:"enabled" mapstructure:"enabled"`
	// GraceAfterClose is how long a closed group is left alone before it is a
	// candidate. Zero uses the sweep's default of a day, which is the number
	// the bot already promised the group in the message it sends on close.
	GraceAfterClose time.Duration `json:"grace-after-close" mapstructure:"grace-after-close"`
	// Interval is how often a pass runs. Zero uses the sweep's default.
	Interval time.Duration `json:"interval" mapstructure:"interval"`
	// Batch bounds how many groups one pass handles. Zero uses the default.
	Batch int `json:"batch" mapstructure:"batch"`
	// HistoryLimit bounds how many messages are archived per group.
	HistoryLimit int `json:"history-limit" mapstructure:"history-limit"`
}

// AddFlags binds DissolveOptions to command-line flags.
func (o *DissolveOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.BoolVar(&o.Enabled, fullPrefix+".enabled", o.Enabled,
		"Dissolve closed ticket groups after the grace period. Irreversible: Lark keeps no history for a dissolved group.")
	fs.DurationVar(&o.GraceAfterClose, fullPrefix+".grace-after-close", o.GraceAfterClose,
		"How long a closed group is left alone before it may be dissolved.")
	fs.DurationVar(&o.Interval, fullPrefix+".interval", o.Interval, "How often the dissolution sweep runs.")
	fs.IntVar(&o.Batch, fullPrefix+".batch", o.Batch, "Groups handled per sweep pass (default 50).")
	fs.IntVar(&o.HistoryLimit, fullPrefix+".history-limit", o.HistoryLimit,
		"Messages archived per group before it is dissolved (default 500).")
}

func (o *ChatOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.BoolVar(&o.RoleAuthz.Enabled, fullPrefix+".role-authz.enabled", o.RoleAuthz.Enabled,
		"Answer the SRE/admin role questions from the authorization rule table instead of the chat.setting lists.")
	fs.DurationVar(&o.RoleAuthz.RuleTableRefreshInterval, fullPrefix+".role-authz.rule-table-refresh-interval",
		o.RoleAuthz.RuleTableRefreshInterval, "How often the role rules are re-read.")
	fs.DurationVar(&o.Session.IdleTimeout, fullPrefix+".session.idle-timeout", o.Session.IdleTimeout,
		"How long an untouched conversation survives before its resources are released.")
	fs.DurationVar(&o.Session.EventIdleTimeout, fullPrefix+".session.event-idle-timeout", o.Session.EventIdleTimeout,
		"Idle bound for conversations still receiving events but not producing turns.")
	fs.DurationVar(&o.Session.CloseTimeout, fullPrefix+".session.close-timeout", o.Session.CloseTimeout,
		"Graceful shutdown bound for a conversation's backend.")
	fs.DurationVar(&o.Session.BusyStaleLockMaxHeld, fullPrefix+".session.busy-stale-lock-max-held",
		o.Session.BusyStaleLockMaxHeld, "How long a busy lock may be held before another message may break it.")
	fs.IntVar(&o.Session.MaxQueuedMessages, fullPrefix+".session.max-queued-messages", o.Session.MaxQueuedMessages,
		"Messages that may queue behind a running turn before further messages are rejected.")
	fs.IntVar(&o.Session.MaxConcurrentTurns, fullPrefix+".session.max-concurrent-turns", o.Session.MaxConcurrentTurns,
		"Turns that may run at once in one conversation (1 = serialise and queue, N>1 = up to N concurrently).")
	fs.DurationVar(&o.Session.TurnTimeout, fullPrefix+".session.turn-timeout", o.Session.TurnTimeout,
		"Upper bound on a single AI turn.")
	fs.StringVar(&o.Session.DataDir, fullPrefix+".session.data-dir", o.Session.DataDir,
		"Directory for conversation snapshots; empty uses the server's home directory.")

	fs.BoolVar(&o.Streaming.Enabled, fullPrefix+".streaming.enabled", o.Streaming.Enabled,
		"Stream the answer incrementally; when false it is written once at the end of the turn.")
	fs.DurationVar(&o.Streaming.Interval, fullPrefix+".streaming.interval", o.Streaming.Interval,
		"Minimum time between two updates of the same streaming sink.")
	fs.IntVar(&o.Streaming.MinDeltaChars, fullPrefix+".streaming.min-delta-chars", o.Streaming.MinDeltaChars,
		"Minimum number of new characters required before a streaming flush.")
	fs.IntVar(&o.Streaming.MaxChars, fullPrefix+".streaming.max-chars", o.Streaming.MaxChars,
		"Upper bound on previewed characters; the final render always carries the full answer.")
	fs.IntVar(&o.Streaming.PrintFrequencyMs, fullPrefix+".streaming.print-frequency-ms", o.Streaming.PrintFrequencyMs,
		"Cardkit typing cadence hint (ms) sent to Feishu.")
	fs.IntVar(&o.Streaming.PrintStep, fullPrefix+".streaming.print-step", o.Streaming.PrintStep,
		"Cardkit characters-per-tick hint sent to Feishu.")
	fs.StringVar(&o.Streaming.PrintStrategy, fullPrefix+".streaming.print-strategy", o.Streaming.PrintStrategy,
		"Cardkit rendering strategy hint: fast | delay.")

	fs.StringVar(&o.Progress.Style, fullPrefix+".progress.style", o.Progress.Style,
		"Tool-progress rendering style.")
	fs.IntVar(&o.Progress.MaxEntries, fullPrefix+".progress.max-entries", o.Progress.MaxEntries,
		"How many tool steps the progress panel lists.")
	fs.DurationVar(&o.Progress.Interval, fullPrefix+".progress.interval", o.Progress.Interval,
		"Minimum time between two progress panel updates.")

	fs.BoolVar(&o.Dedup.Enabled, fullPrefix+".dedup.enabled", o.Dedup.Enabled,
		"Drop redelivered inbound events instead of running them twice.")
	fs.IntVar(&o.Dedup.TTLSec, fullPrefix+".dedup.ttl-sec", o.Dedup.TTLSec,
		"How long a seen event ID is remembered, in seconds.")
	fs.IntVar(&o.Dedup.MaxEntries, fullPrefix+".dedup.max-entries", o.Dedup.MaxEntries,
		"Upper bound on the dedup cache size.")

	fs.DurationVar(&o.AgentCommand.RulesTTL, fullPrefix+".agent-command.rules-ttl", o.AgentCommand.RulesTTL,
		"How long a change to a command rule goes unnoticed on a replica that did not serve the write.")
	fs.DurationVar(&o.AgentCommand.CustomsTTL, fullPrefix+".agent-command.customs-ttl", o.AgentCommand.CustomsTTL,
		"How long an agent's list of user-defined commands is cached before being refreshed.")
	fs.DurationVar(&o.AgentCommand.SkillsTTL, fullPrefix+".agent-command.skills-ttl", o.AgentCommand.SkillsTTL,
		"How long an agent's list of skill commands can be stale.")

	fs.StringVar(&o.Agents.DefaultBackend, fullPrefix+".agents.default-backend", o.Agents.DefaultBackend,
		"Backend a new conversation uses when none is named: llm | cli.")
	fs.BoolVar(&o.Agents.CLI.Enabled, fullPrefix+".agents.cli.enabled", o.Agents.CLI.Enabled,
		"Enable the local agent-CLI backend (lets a chat conversation run a process on this host).")
	fs.IntVar(&o.Agents.CLI.MaxSessions, fullPrefix+".agents.cli.max-sessions", o.Agents.CLI.MaxSessions,
		"Maximum number of concurrently live CLI sessions.")
	fs.StringVar(&o.Agents.CLI.WorkspaceRoot, fullPrefix+".agents.cli.workspace-root", o.Agents.CLI.WorkspaceRoot,
		"Directory under which each conversation gets its own workspace subdirectory.")
	fs.StringSliceVar(&o.Agents.CLI.AllowedDirs, fullPrefix+".agents.cli.allowed-dirs", o.Agents.CLI.AllowedDirs,
		"Extra directories a conversation may use as its working directory.")
	fs.DurationVar(&o.Agents.CLI.IdleTimeout, fullPrefix+".agents.cli.idle-timeout", o.Agents.CLI.IdleTimeout,
		"How long a CLI session may sit idle before its process is reaped.")
	fs.DurationVar(&o.Agents.CLI.TurnTimeout, fullPrefix+".agents.cli.turn-timeout", o.Agents.CLI.TurnTimeout,
		"Upper bound on a single CLI turn.")
	fs.StringVar(&o.Agents.CLI.PermissionMode, fullPrefix+".agents.cli.permission-mode", o.Agents.CLI.PermissionMode,
		"How the CLI handles tool permission prompts: stdio (ask the user) | skip (bypass).")
	fs.StringSliceVar(&o.Agents.CLI.AllowedTools, fullPrefix+".agents.cli.allowed-tools", o.Agents.CLI.AllowedTools,
		"Tool allow-list for the CLI backend; empty allows the CLI's own default set.")
	fs.StringSliceVar(&o.Agents.CLI.DisallowedTools, fullPrefix+".agents.cli.disallowed-tools", o.Agents.CLI.DisallowedTools,
		"Tools removed from the CLI backend's tool set.")
	fs.StringVar(&o.Agents.CLI.Model, fullPrefix+".agents.cli.model", o.Agents.CLI.Model,
		"Model the CLI backend runs; empty uses the CLI's own default.")
	fs.IntVar(&o.Agents.CLI.MaxLineBytes, fullPrefix+".agents.cli.max-line-bytes", o.Agents.CLI.MaxLineBytes,
		"Maximum size of a single line of the CLI event stream.")

	fs.StringVar(&o.I18n.Language, fullPrefix+".i18n.language", o.I18n.Language,
		"Language replies are written in: zh | zh-TW | en. Untranslated strings are sent as written.")

	fs.BoolVar(&o.Login.AllowLocal, fullPrefix+".login.allow-local", o.Login.AllowLocal,
		"Enable password login (provider=local) against chat.user.")
	fs.BoolVar(&o.Login.AllowRegistration, fullPrefix+".login.allow-registration", o.Login.AllowRegistration,
		"Enable POST /v1/auth/register (open account registration).")
	fs.BoolVar(&o.Login.BootstrapAdmin, fullPrefix+".login.bootstrap-admin", o.Login.BootstrapAdmin,
		"Grant the first account created while the user table is empty the administrator role.")
	fs.BoolVar(&o.Login.AllowLark, fullPrefix+".login.allow-lark", o.Login.AllowLark,
		"Enable legacy provider=lark on /v1/auth/login. UNSAFE: the lark_token is not verified.")
	fs.BoolVar(&o.Login.AllowAPIKey, fullPrefix+".login.allow-apikey", o.Login.AllowAPIKey,
		"Enable provider=apikey on /v1/auth/login. UNSAFE: any non-empty api_key is accepted.")
	fs.DurationVar(&o.Login.TokenTTL, fullPrefix+".login.token-ttl", o.Login.TokenTTL,
		"Lifetime of a token issued by the login endpoints.")

	o.Dissolve.AddFlags(fs, fullPrefix+".dissolve")
}
