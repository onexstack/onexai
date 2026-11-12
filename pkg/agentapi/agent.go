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

// Package agentapi defines the backend-neutral AI agent API: the event stream a
// run produces, the request shape, the IAgentBiz service consumed by commands
// and the IM pipeline, and the swappable Engine seam that performs the actual
// model/agent execution (HTTP OpenAI-compatible today, eino/adk optionally).
package agentapi

import (
	"context"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/pkg/media"
)

// EventType enumerates the kinds of events an agent run emits.
type EventType int

const (
	// EventProgress carries the current tool-execution log (Steps).
	EventProgress EventType = iota
	// EventReasoning carries an incremental chunk of reasoning text (Delta).
	EventReasoning
	// EventAnswer carries an incremental chunk of the answer (Delta).
	EventAnswer
	// EventToolStep carries a single updated tool step (Steps[0]).
	EventToolStep
	// EventDone is the terminal success event; Final holds the full answer.
	EventDone
	// EventFailed is a terminal error event; Err holds the cause.
	EventFailed
	// EventStopped is a terminal event emitted when the user stopped the run.
	EventStopped
	// EventInterrupted signals the run paused awaiting human approval.
	EventInterrupted
	// EventImage carries generated image references. It is not terminal: a run
	// may produce several images and go on working, so a renderer shows each
	// one as it arrives rather than treating it as a result.
	EventImage
	// EventSuspended is the terminal event of a stream that ended because the run
	// paused for approval. It is distinct from EventDone/EventFailed/EventStopped
	// because the run has not finished: the work so far is not to be presented as
	// a result, and the run will continue under a later resume call. A renderer
	// that treated the closed stream as an ending would finalize a card that is
	// still waiting on an answer.
	EventSuspended
	// EventUsage carries the run's accumulated token usage so far (Usage).
	EventUsage
	// EventSources carries the documents a retrieval returned (Sources). Like
	// EventImage it is not terminal: a run may retrieve more than once — and one
	// retrieval may touch several bases — so a renderer shows each report as it
	// arrives instead of treating it as a result.
	//
	// Appended rather than placed beside EventImage, where it reads better: the
	// values are positional, and inserting one would renumber every constant
	// after it.
	EventSources
)

// ToolStep describes one tool invocation in the progress log.
type ToolStep struct {
	// ID is the model's tool-call id. It is stable across an approval pause and
	// resume, so a renderer updates a row in place rather than appending a
	// duplicate, and it is how a later event carries this step's tokens.
	ID     string
	Name   string
	Status string // "pending" | "ok" | "fail"
	// Duration is the measured wall-clock time the invocation took.
	Duration time.Duration
	// Tokens is how many prompt tokens the invocation's result added to the
	// context, derived from the model calls around it rather than measured: an
	// OpenAI-compatible API reports usage per model call and has no per-tool
	// granularity. It is 0 until the following call reports its usage, and stays
	// 0 when the upstream reports none. Tools invoked in the same parallel batch
	// share one value.
	Tokens int
	// Result is what the tool returned, truncated to keep it off the critical
	// path of a stream that carries it to a renderer. It exists so that what a
	// tool did is available to the caller as a fact rather than only as the
	// model's description of it — a run that claims to have triggered something
	// can be checked against the trigger's own output. Empty while the call is
	// pending, and when the tool returned nothing.
	Result string
}

// Usage is the token usage an agent run has accumulated.
//
// The totals cover every model call the run made, including each round of a tool
// loop, so prompt tokens are counted once per round: the total is what the run
// consumed, not the size of its final context.
type Usage struct {
	// Calls is the number of model calls the totals cover.
	Calls int
	// PromptTokens is the accumulated prompt token count.
	PromptTokens int
	// CompletionTokens is the accumulated completion token count.
	CompletionTokens int
	// TotalTokens is the accumulated total, prompt plus completion.
	TotalTokens int
	// ReasoningTokens is the accumulated reasoning token count.
	ReasoningTokens int
	// CacheHitTokens and CacheMissTokens split PromptTokens into the part the
	// upstream served from its prefix cache and the part it computed.
	//
	// They are carried to the caller because the caller is where a run is
	// accounted for, and the two halves are two different prices (DeepSeek:
	// 0.02 CNY/M cached against 1 CNY/M uncached). A caller that only sees the
	// prompt total cannot say what the run cost, nor whether the prefix it sends
	// is stable enough to keep being cached. Zero means the upstream did not
	// report the split; see usage.Call.
	CacheHitTokens  int
	CacheMissTokens int
	// CacheWriteTokens is the part of PromptTokens the upstream wrote into its
	// prefix cache. Providers that bill cache writes report it; the rest leave it
	// zero. See usage.Call.CacheWriteTokens.
	CacheWriteTokens int
}

// Interrupt kind values carried in Interrupt.Kind.
const (
	// InterruptKindPermission is a yes/no approval prompt (allow/deny/allow-all).
	InterruptKindPermission = "permission"
	// InterruptKindQuestion is an AskUserQuestion prompt with selectable options.
	InterruptKindQuestion = "question"
)

// Interrupt describes a human-approval pause (for EventInterrupted): the run
// halted awaiting a decision before it may proceed.
//
// It carries a list of requests rather than one, because a single assistant
// message can call several tools at once and the policy can gate more than one
// of them. eino merges those into one composite interrupt, and a run resumed
// with only some of them answered re-interrupts on the rest — so the whole group
// is presented and answered together, and the group is what the caller sees.
type Interrupt struct {
	// RequestID identifies this suspension, not the run: a run that suspends
	// twice has two. An answer must name it so a decision made against a card
	// the run has already moved past is refused rather than applied to whatever
	// the run is waiting on now.
	RequestID string
	// Requests are the pending calls, one per interrupt point.
	Requests []InterruptRequest
}

// AskQuestionInfo is the user-facing payload an ask_user_question tool raises
// when it interrupts for an answer. It is carried as the interrupt's Info so the
// engine can surface the question and its options to the chat.
type AskQuestionInfo struct {
	Question    string   `json:"question"`
	Options     []string `json:"options,omitempty"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
}

// The interrupt payloads are registered for gob because adk persists them.
//
// An interrupt's Info rides along with its state: adk's runner gob-encodes the
// checkpoint, and the interrupt signal inside it holds the Info as an interface
// value, which gob can only encode for a registered type. An unregistered one
// fails the save — and the save happens before the interrupt is emitted, so the
// run reports a failure instead of pausing, on the first call that needed a
// decision. Nothing about the type's own code hints at this; it is discovered by
// gating a real tool.
//
// The names are part of the stored format: an existing checkpoint names its
// types, so they may be added to but never renamed or reused. adk registers its
// own payloads the same way.
func init() {
	schema.RegisterName[*AskQuestionInfo]("onexai_ask_question_info")
	schema.RegisterName[*PermissionInfo]("onexai_permission_info")
}

// Event is a single item in an agent run's event stream. It is backend-neutral
// so it can be rendered to a Lark card, a TUI stream, or a buffered REST result.
type Event struct {
	Type  EventType
	Delta string     // for EventReasoning / EventAnswer
	Steps []ToolStep // for EventProgress / EventToolStep
	Final string     // for EventDone
	// OutputSchemaSHA256 is the digest of the canonical schema Final was
	// validated against. Empty means the run had no structured-output contract,
	// which is what tells a surface to publish the answer as text.
	OutputSchemaSHA256 string // for EventDone
	Err                error  // for EventFailed
	// Interrupt describes the approval pause for EventInterrupted.
	Interrupt *Interrupt
	// Usage is the run's accumulated token usage, carried by EventUsage and by
	// the terminal EventDone, where it is the authoritative total.
	Usage *Usage
	// Images are the generated image references, carried by EventImage.
	//
	// They are references rather than bytes for the reason the whole media
	// design exists: the caller that renders them resolves them, and a run that
	// produced a megabyte of PNG does not put it through the event channel that
	// carries every token of the answer.
	Images []media.Ref
	// Sources are the documents a retrieval returned, carried by EventSources.
	//
	// References rather than passages, for the same reason images are references
	// rather than bytes: the passages already reached the model as part of the
	// tool's own output, and repeating them here would put the retrieved text on
	// the wire twice.
	Sources []SourceRef
}

// Role values for conversation messages.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
)

// Message is a backend-neutral conversation message used for session history and
// engine input, decoupling the agent API from any specific LLM SDK type.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Images are the images attached to this message, as references.
	//
	// They are references rather than bytes because this struct is what the
	// session store writes: a data URL here would be a base64 payload in Redis
	// that every following turn of the conversation reads back in full. The
	// bytes are resolved once per turn, at the model edge, into a copy of these
	// messages that never reaches the store. See internal/agent/pkg/agentedge.
	//
	// A malformed or legacy entry decodes to a reference whose URL is the
	// original string, so history written before this type existed still loads.
	Images []media.Ref `json:"images,omitempty"`
}

// AskRequest is a single AI request.
//
// The shape mirrors AgentAskRequest in pkg/api/agent/v1: content, identity,
// selection, model, generation, execution policy and metering are separate
// groups. Source is internal: the handler knows which endpoint it served.
type AskRequest struct {
	SessionID  string
	RunID      string
	Input      Input
	Selection  Selection
	Model      string
	Generation GenerationConfig
	Subject    Principal
	Actor      *Principal
	Options    ExecutionOptions
	Metering   Metering
	Metadata   map[string]string

	// Lineage records that this run was delegated by another run. Nil is an
	// ordinary request. It travels into the run's capability token so a chain of
	// delegations can be bounded and traced without trusting the runtime.
	Lineage *Lineage

	// Source names the surface the request arrived on ("chat", "api", ...),
	// for the audit record. It is set by the handler.
	Source string

	// Capabilities, when non-nil, narrows this run to exactly the names it
	// carries. Nil is the selected agents' full effective set, which is what
	// every caller that does not set it gets.
	//
	// Only the management debug surface sets it. It cannot widen anything:
	// the names are intersected with what the selection already resolves to,
	// and a name outside that set is refused rather than honoured. That is why
	// it does not live on Selection beside the signed face's InvokedSkills —
	// there is no signed caller to give it to.
	Capabilities *CapabilitySelection

	// Composition, when non-nil, replaces the selected agents' instruction and
	// capability bindings for this run. The agents still supply the execution
	// subject, quota, runtime, memory settings, delegates, webhooks and the
	// approval floor, but nothing of their prompt/skills/MCPs/KBs/commands is
	// used unless the composition names it.
	//
	// It is mutually exclusive with Capabilities: one narrows an existing
	// declaration, the other replaces it.
	Composition *AgentComposition
}

// AgentComposition is the exact set of components one composed run uses.
//
// A name may be one the selected agent does not declare: composition replaces
// the declaration rather than narrowing it. The run validates each name, the
// namespace policy and the caller's authorization exactly as it would for an
// agent whose bindings these names were.
type AgentComposition struct {
	Prompts        []string `json:"prompts,omitempty"`
	Skills         []string `json:"skills,omitempty"`
	MCPs           []string `json:"mcps,omitempty"`
	KnowledgeBases []string `json:"knowledgeBases,omitempty"`
	CustomCommands []string `json:"customCommands,omitempty"`
	Model          string   `json:"model,omitempty"`
	// Namespace is the referrer namespace of this composed run. Bare entries
	// resolve against it, and a cross-namespace reference is judged as coming
	// from an Agent in it. Empty means the deployment default namespace.
	//
	// A hostless run uses the console's current business namespace. A hosted
	// run's referrer is the host agent, so a disagreeing value is refused.
	Namespace string `json:"namespace,omitempty"`
}

// CapabilitySelection is a run's explicit capability subset.
//
// Nil (the pointer, not a field) means "not narrowed": the run uses the full
// effective set. A non-nil value means exactly these names; an empty or nil
// slice inside it means none of that kind. The pointer is what keeps "narrowed
// to nothing" distinguishable from "not narrowed".
type CapabilitySelection struct {
	Skills         []string `json:"skills,omitempty"`
	MCPs           []string `json:"mcps,omitempty"`
	KnowledgeBases []string `json:"knowledgeBases,omitempty"`
	CustomCommands []string `json:"customCommands,omitempty"`
}

// Lineage is where a delegated run came from.
type Lineage struct {
	// RootRunID is the run at the top of the delegation chain.
	RootRunID string
	// ParentRunID is the run that delegated this one.
	ParentRunID string
	// Depth is how many delegations down this run is: the first delegate is 1.
	Depth int
}

// Input is one turn's content. History stays in the runtime's session.
type Input struct {
	Text string
	// Images travel as references and become bytes at the model edge.
	Images []media.Ref
	// Documents travel as references too, but become *text* before the run
	// starts: a document is prompt material, and a model that is sent bytes it
	// cannot read answers about a file it never saw.
	Documents []media.Ref
}

// Selection names the agent set and the caller's explicit skill choices.
type Selection struct {
	Agents        []string
	InvokedSkills []string
}

// Kinds a Principal can have. They mirror the authorization subsystem's subject
// kinds; an empty Kind is the historical "user", which is what every surface
// predating this field reports.
const (
	PrincipalKindUser    = "user"
	PrincipalKindService = "serviceaccount"
	PrincipalKindGroup   = "group"
)

// Principal is an identity.
type Principal struct {
	ID   string
	Name string
	// Kind says what the identity is, because the same ID means different things
	// as a person and as a service: a rule that grants an operator says nothing
	// about a gateway signing with a key of the same name. Empty keeps the
	// pre-existing behaviour and is read as a user.
	Kind string
}

// GenerationConfig carries per-run generation parameters.
type GenerationConfig struct {
	Reasoning       *bool
	Temperature     *float64
	MaxOutputTokens *int
	TopP            *float64
	ProviderOptions map[string]string
}

// ExecutionOptions carries run policy.
type ExecutionOptions struct {
	Approval Approval
	Deadline time.Duration
}

// ApprovalChannel says whether a run can pause for a human.
type ApprovalChannel int

const (
	// ApprovalChannelUnspecified keeps the historical behaviour: interactive.
	ApprovalChannelUnspecified ApprovalChannel = iota
	// ApprovalChannelInteractive can pause and wait for an answer.
	ApprovalChannelInteractive
	// ApprovalChannelNone cannot answer; gated tools follow Fallback.
	ApprovalChannelNone
)

// Approval describes whether a run can pause for a human.
type Approval struct {
	Channel  ApprovalChannel
	Fallback string
}

// Metering says who writes a run's ledger rows.
type Metering int

const (
	// MeteringUnspecified keeps the historical behaviour: the runtime reports.
	MeteringUnspecified Metering = iota
	// MeteringCaller means the caller writes the ledger itself.
	MeteringCaller
	// MeteringRuntime means the runtime reports through the usage route.
	MeteringRuntime
	// MeteringNone means nobody meters this run (platform-owned work).
	MeteringNone
)

// Reports answers whether the runtime reports this run's usage.
func (m Metering) Reports() bool {
	return m == MeteringUnspecified || m == MeteringRuntime
}

// ReportsUsage answers whether a run's usage is this side's to report.
//
// It reads the metering mode rather than a pair of booleans: the tri-state
// lives on the wire as an enum, and the default (unspecified) means "report
// it", which keeps a caller that never mentions the field working.
func ReportsUsage(req *AskRequest) bool {
	return req.Metering.Reports()
}

// ApproveRequest answers a suspended run.
type ApproveRequest struct {
	// RunID is the suspended run, which is also its checkpoint ID.
	RunID string
	// RequestID is the suspension being answered, as carried by the interrupt
	// event. It must match the one the run is currently waiting on.
	RequestID string
	// Decision is DecisionAllow, DecisionDeny or DecisionAllowAll, and applies
	// to every permission request in the suspension.
	Decision string
	// Answers supplies the text for question interrupts, keyed by the interrupt
	// address it answers. A permission request takes its answer from Decision
	// instead, so an address appears here only when its kind is
	// InterruptKindQuestion.
	Answers map[string]string
	// Reason optionally explains a refusal. It is handed to the model in the
	// refused tool's result, so the model can say why the call did not run
	// rather than leaving the asker with a bare no.
	Reason string
}

// IAgentBiz is the agent business-logic service. Ask performs one turn and
// returns a backend-neutral event stream (the channel is closed when the run
// terminates). Stop requests cancellation of an in-flight run by RunID.
type IAgentBiz interface {
	Ask(ctx context.Context, req *AskRequest) (<-chan Event, error)
	Stop(ctx context.Context, runID string) error
	// Approve answers a suspended run and returns the event stream of the
	// resumed run, which the caller pumps exactly as it pumps Ask's. It is a
	// second run rather than a channel back into the first: the pod that paused
	// the run may be gone by the time a human answers, so the continuation is
	// rebuilt from the checkpoint and may execute anywhere.
	Approve(ctx context.Context, req *ApproveRequest) (<-chan Event, error)
	// Clear resets the conversation memory for the given session key.
	Clear(ctx context.Context, sessionKey string) error
	// CheckAgentAccess answers whether a subject may use an agent.
	//
	// It exists so a caller that needs to know *before* it records something —
	// the chat service's /agent command writes a declaration it must be able to
	// keep — can ask the same question the run path asks, without reimplementing
	// the policy. The run path does not use it: it authorizes inline, where it
	// already has the resolved subject and the resources.
	CheckAgentAccess(ctx context.Context, subjectID, agentName string) (bool, string, error)
}

// RunOptions are per-run knobs handed to an Engine.
type RunOptions struct {
	Reasoning bool
	Agents    []string
	// Model is the effective model endpoint name (agent.model.name) resolved
	// for this run. Empty means the deployment default.
	Model string
	// Owned reports that Model is a user-configured endpoint: its tokens are
	// not charged against the platform's token budget.
	Owned bool
	// Generation carries per-run generation parameters. Zero values mean "not
	// set", so the deployment/model defaults apply.
	Temperature     *float64
	MaxOutputTokens *int
	TopP            *float64
	ProviderOptions map[string]string
	// Instruction is the per-run system-prompt override, resolved from the
	// selected agent's persona. Empty means use the engine's default instruction.
	Instruction string
	// InstructionSet makes Instruction authoritative even when it is empty. A
	// composed run uses it to say "these prompts are the whole agent prompt",
	// which must not fall back to the deployment's default persona. The
	// engine's own framework instruction is a separate layer and still applies.
	InstructionSet bool
	// MCPs names the MCP servers whose tools this run may use: the union of the
	// selected agents' AgentM.MCPs. Empty means the run gets no MCP tools, never
	// all of them. Built-in tools are available regardless, and are the engine's
	// business rather than this list's.
	MCPs []string
	// Skills names the skill records the selected agents declared: the union of
	// their AgentM.Skills. Empty means the run gets no skill tool, which is what
	// an agent that declares none has always had.
	Skills []string
	// KnowledgeBases names the knowledge-base records the selected agents
	// declared: the union of their AgentM.KnowledgeBases. Empty means the run
	// gets no retrieval tool.
	KnowledgeBases []string
	// Approval holds the tool-name patterns this run's resources declared as
	// needing a human's approval. It is a plain list of patterns rather than a
	// policy so that this package stays free of the enforcement machinery; the
	// engine unions it with the tool names that opt out of gating and builds the
	// policy it enforces. Empty means nothing is gated.
	Approval []string
	// ApprovalFallback is what a gated call does when this run cannot be paused
	// for an answer, which is the case for a caller with no channel to put a
	// question back to — the OpenAI-compatible endpoint. Empty means the run can
	// be paused, which is the case for every surface that can show a human a
	// card. See ApprovalFallbackDeny and ApprovalFallbackError.
	ApprovalFallback string
	// Delegation is the identity this run's delegated tool calls speak for, or
	// nil when the run may not delegate. It is also what decides whether the
	// delegation tool is part of the graph.
	Delegation *DelegationContext
	// MemoryEnabled says whether this run may save and read long-term memory.
	// It is what decides whether the memory tools are part of the graph.
	MemoryEnabled bool
	// RunID identifies the run for stop/approve signaling.
	RunID string
	// OnStopReady hands the caller a function that stops this run at its next
	// safe point. It is called once, before the run's first event, so the caller
	// can register the right stop with whatever registry its surface uses.
	//
	// It is a plain func rather than the engine's own cancel type so this
	// package stays free of eino: the caller decides whether a stop is graceful
	// and how it is addressed across pods, and the engine only decides where the
	// safe point is. Nil keeps the caller's own hard cancel as the only stop.
	OnStopReady func(stop func())
	// SenderID and SessionKey are the run's identity, carried so a finished run's
	// usage can be reported back to the platform that owns the ledger.
	//
	// They are on the run options rather than read from the context because the
	// report is made by the engine, after the answer is delivered, from a
	// goroutine that no longer holds the request. SenderID is the asker's
	// subject — the platform account for a speaker OneX has bound, otherwise the
	// chat identity — and an empty one means the runtime cannot say who spent the
	// tokens; see usagereport.Client.Report, which then leaves the ledger alone.
	SenderID   string
	SessionKey string
	// ReportUsage says whether this run's usage is this side's to report.
	//
	// It is already resolved by the time it gets here — the asker either said so
	// or said nothing, and saying nothing means "report it" (see
	// AskRequest.ReportUsageSet). A run whose usage the platform meters itself
	// carries false, because the platform has already written those ledger rows
	// in the same transaction as the message or comment the run produced;
	// reporting it again would charge the same run twice against every ceiling
	// that reads the ledger.
	//
	// The zero value is false, which is why both places that build these options
	// set it explicitly rather than letting it default: the value is not a
	// property of the run that happens to be absent, it is a decision about who
	// meters it.
	ReportUsage bool
	// BypassRunnerCache builds this run's runner fresh instead of reusing the
	// cached one.
	//
	// The cache is keyed by agent set and its build function is handed the
	// names alone, so a cached runner is always built from the agents' full
	// effective capabilities. A run narrowed to a subset therefore cannot use
	// it: the fingerprint can tell the cache to rebuild, but the rebuild would
	// read the full set again. Bypassing is the smallest correct answer, and a
	// narrowed run is a debug run, where the build cost does not matter.
	BypassRunnerCache bool
	// Unattended marks a run nobody is watching: a scheduled occurrence, a
	// manual trigger of one, or any other dispatch with no human on the other
	// end. Its tool set drops ask_user_question — an interrupt raised there
	// parks the run until the question expires, and no one is coming to answer
	// it. It also bypasses the runner cache, whose build function cannot see
	// this flag.
	Unattended bool
}

// ApprovalFallback values for RunOptions.ApprovalFallback.
const (
	// ApprovalFallbackDeny refuses a gated call instead of pausing for it. The
	// refusal is handed to the model as an ordinary tool result, so the turn
	// still ends with an answer that says why the work did not happen. It is the
	// default for a caller that cannot answer.
	ApprovalFallbackDeny = "deny"
	// ApprovalFallbackError fails the call. It is for a caller that would rather
	// be told the request needed a decision than receive a model's account of a
	// tool it silently declined to run.
	ApprovalFallbackError = "error"
)

// Engine is the swappable execution core. Given the full message history it
// streams events for one turn and closes the channel when finished.
//
// The service's implementation is the eino/adk one (tools and agents); the
// interface is what keeps every caller — HTTP, chat, A2A — unaware of that.
type Engine interface {
	Run(ctx context.Context, messages []Message, opts RunOptions) (<-chan Event, error)
	// Name identifies the engine implementation (for logs/metrics).
	Name() string
}

// modelNameKey is the context key carrying the effective model endpoint name
// for one run. The engine's stable model router reads it per Generate/Stream
// call, which is what keeps a cached runner from capturing the first run's
// model.
type modelNameKey struct{}

// WithModelName attaches the effective model endpoint name to ctx.
func WithModelName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, modelNameKey{}, name)
}

// ModelNameFromContext returns the effective model endpoint name, or "".
func ModelNameFromContext(ctx context.Context) string {
	name, _ := ctx.Value(modelNameKey{}).(string)
	return name
}

// runIDKey is the context key carrying a run's ID.
//
// It is what a component that writes per-run files keys them by: the checkpoint
// ID identifies the run across pods and across the pause, so a resumed half
// finds what the first half wrote.
type runIDKey struct{}

// WithRunID attaches the run's ID to ctx.
func WithRunID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, runIDKey{}, id)
}

// RunIDFromContext returns the run's ID, or "" when the context carries none.
func RunIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(runIDKey{}).(string)
	return id
}

// MemoryContext is who a run's explicit memory calls are for.
//
// It travels on the run context rather than in the tool's arguments for the
// same reason delegation does: the model picks what to remember, not who it is
// remembering it for.
type MemoryContext struct {
	// Agent is the agent whose memory is read and written.
	Agent string
	// Subject is the person the memory belongs to.
	Subject string
}

// memoryKey is the context key. It is an unexported struct type so no other
// package can collide with it.
type memoryKey struct{}

// WithMemoryContext attaches the memory identity to a run's context.
func WithMemoryContext(ctx context.Context, memory MemoryContext) context.Context {
	return context.WithValue(ctx, memoryKey{}, memory)
}

// MemoryContextFrom returns the memory identity, if the run has one.
func MemoryContextFrom(ctx context.Context) (MemoryContext, bool) {
	memory, ok := ctx.Value(memoryKey{}).(MemoryContext)
	return memory, ok
}

// DelegationContext is who a run's delegated tool calls speak for.
//
// It travels on the run context rather than in the tool's arguments because the
// model must not be able to choose its own identity, and because the tool
// instance is shared: one graph serves many runs, so anything run-specific has
// to arrive with the call.
type DelegationContext struct {
	// Agents names the agents this run may call. Empty means it may call none,
	// which is what keeps a delegation tool built into a graph the same shape as
	// an ordinary tool with no authority behind it.
	Agents []string
	// ParentSession is this run's conversation, so a delegate's conversation is
	// derived from it and repeated calls from one conversation share context.
	ParentSession string
	// Subject is the caller the delegate runs as. Delegation never escalates.
	Subject string
	// SubjectKind distinguishes a person from a service with the same name.
	SubjectKind string
	// RootRunID is the run at the top of the chain, and Depth is how many
	// delegations down this run is. The delegate's parent is this run, which the
	// caller knows from its own context.
	RootRunID string
	Depth     int
}

// delegationKey is the context key. It is an unexported struct type so no other
// package can collide with it.
type delegationKey struct{}

// WithDelegation attaches the delegation identity to a run's context.
func WithDelegation(ctx context.Context, delegation DelegationContext) context.Context {
	return context.WithValue(ctx, delegationKey{}, delegation)
}

// DelegationFromContext returns the delegation identity, if the run has one.
func DelegationFromContext(ctx context.Context) (DelegationContext, bool) {
	delegation, ok := ctx.Value(delegationKey{}).(DelegationContext)
	return delegation, ok
}
