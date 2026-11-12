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

// Package agent implements the AI agent business logic (IAgentBiz): it
// orchestrates conversation sessions, per-conversation concurrency limiting,
// run cancellation, and timeouts around a swappable Engine, exposing a
// backend-neutral event stream. Presentation (Lark card / TUI / REST) is
// handled separately by the agentstream package.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/google/wire"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/agent/biz/v1/asset"
	"github.com/onexstack/onexai/internal/agent/capability"
	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/a2ainvoke"
	"github.com/onexstack/onexai/internal/agent/pkg/agentauthz"
	"github.com/onexstack/onexai/internal/agent/pkg/agentedge"
	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	"github.com/onexstack/onexai/internal/agent/pkg/interactionlog"
	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/pkg/memory"
	"github.com/onexstack/onexai/internal/agent/pkg/metrics"
	"github.com/onexstack/onexai/internal/agent/pkg/ratelimit"
	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	"github.com/onexstack/onexai/internal/agent/pkg/runtime"
	"github.com/onexstack/onexai/internal/agent/pkg/settingstore"
	"github.com/onexstack/onexai/internal/agent/pkg/webhook"
	"github.com/onexstack/onexai/internal/agent/pkg/webhookclient"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/contextx"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
	"github.com/onexstack/onexai/pkg/apis/agent/validation/outputschema"
	"github.com/onexstack/onexai/pkg/media"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// ProviderSet binds the concrete AgentBiz to the agentapi.IAgentBiz interface.
var ProviderSet = wire.NewSet(New, wire.Bind(new(agentapi.IAgentBiz), new(*AgentBiz)))

// ErrConversationBusy is returned (user-facing) when a conversation already has
// the maximum number of concurrent AI runs in flight.
var ErrConversationBusy = errors.New("上一条还在处理中，请稍候再试")

// ErrQuotaExceeded is returned when the asker has used their daily allowance.
// It is a refusal with a time on it, and the handler answers it with 429 rather
// than 403 so a caller can tell "not allowed" from "not right now".
var ErrQuotaExceeded = errors.New("今日请求次数已达上限")

// ErrNotPending is returned (user-facing) when an approval answers a pause the
// run is not waiting on: already answered, expired, or naming an earlier pause
// of the same run. The remedy is the same in every case — ask the run again
// rather than click again — so the cases share one error, and the logs carry
// which one it was.
var ErrNotPending = errors.New("该请求已处理或已失效，请重新发起")

// RunRefusedError is a run-lifecycle hook refusing a run for a reason that is
// not about allowance: a policy, an entitlement, a maintenance window. It
// carries the hook's own message because that message is what the person whose
// run was refused is shown — it is written by whoever administers the hook,
// which is the only party that knows the business reason.
//
// It is a type rather than a sentinel because the message varies per hook. The
// handler answers it with 403, so a caller can tell a refusal ("you may not")
// from the allowance refusals ("not right now", which share ErrQuotaExceeded
// and its 429).
type RunRefusedError struct {
	// Message is shown to the asker.
	Message string
	// Hook names the hook that refused, for the log. It is deliberately not
	// shown: which internal endpoint said no is an operational detail, and the
	// message is supposed to be the business reason on its own.
	Hook string
}

// Error implements error.
func (e *RunRefusedError) Error() string { return e.Message }

// Deps are the AgentBiz dependencies (assembled in providers, injected once).
type Deps struct {
	Engine agentapi.Engine
	// Runtimes selects a port by the agent's runtime profile. Nil keeps the
	// legacy Engine path.
	Runtimes *runtime.Registry
	// Runs is the durable run/session store. Nil keeps the historical
	// best-effort behaviour of not recording runs.
	Runs runstore.Store
	// CapabilityIssuer mints the per-run token a remote runtime presents to the
	// capability gateway. Nil disables remote capability calls.
	CapabilityIssuer *capability.Issuer
	// EnableTracing opens a root run span and propagates it to remote runtimes.
	EnableTracing bool
	// Memory is the long-term memory subsystem. Nil disables memory reads even
	// when an agent enables it.
	Memory *memory.Service
	// ApprovalTTL is how long a pending approval remains answerable. Zero uses
	// 24 hours.
	ApprovalTTL time.Duration
	// PortEnabled routes Ask/Approve through Runtimes. It exists so the new
	// path can be compared against the old one before it becomes the default.
	PortEnabled          bool
	Sessions             mem.SessionStore
	Stops                runstop.Registry
	Checkpoints          checkpoint.Store
	MaxConcurrentPerConv int
	RunTimeout           time.Duration
	// Resources resolves selected agent names to their personas (system prompts).
	// May be nil when no resource backend is configured (agent selection then
	// falls back to the engine's default instruction).
	Resources store.AIStore
	// ModelAPIKeyEncryptionKey opens a model row whose API key was sealed at
	// rest. It is needed by the memory extraction path, which builds its own
	// chat model from a Model row. Empty leaves legacy plaintext rows usable
	// and reports an error for sealed ones.
	ModelAPIKeyEncryptionKey string
	// A2A runs a selection that names a single external A2A agent. Nil leaves
	// such a selection to the engine, which cannot execute it.
	A2A *a2ainvoke.Service
	// Images resolves image references into model-ready data URLs at the last
	// step before the engine runs. Nil leaves every reference unresolved, which
	// the engines then skip — the behaviour of a deployment with no media
	// store, where an image cannot be read at all.
	Images *agentedge.Inliner
	// Assets stores images that arrive as bytes rather than as an upload — the
	// base64 payloads a session written before references still holds. Nil
	// leaves those in place, which is the behaviour every turn had before them.
	Assets asset.AssetBiz
	// MaxContextTokens is the prompt budget for one turn. History above it is
	// compacted before the run starts. Zero disables compaction for this layer
	// (the engine's in-run guard still applies).
	MaxContextTokens int
	// ContextPreserveTokens is how much of the newest history compaction keeps
	// verbatim. Zero means MaxContextTokens/3.
	ContextPreserveTokens int
	// Summarize condenses a stretch of history into one archival paragraph. A
	// nil Summarize disables summarization, leaving compaction to prune.
	Summarize func(ctx context.Context, msgs []agentapi.Message) (string, error)
	// CountTokens measures a history. Nil falls back to the real tokenizer.
	CountTokens func(msgs []agentapi.Message) (int, error)
	// Root is the application-lifetime context. Runs derive from it (not from
	// the per-request context), so a run survives the request returning but is
	// cancelled when the server shuts down. Defaults to context.Background().
	Root context.Context
	// DefaultReasoning is the operator's answer for a caller that never stated a
	// preference. It is consulted only when AskRequest.ReasoningSet is false: a
	// caller that said "off" keeps it off, however this is configured.
	DefaultReasoning bool
	// RequireApproval is the server-wide list of tool-name patterns that need a
	// human's approval, applied to every run whatever agents it selects. It is
	// one of three declaration sites, the others being the MCP servers and the
	// agents a run selects; see resolveApproval.
	RequireApproval []string
	// CompatApprovalMode is what the OpenAI-compatible endpoint does with a tool
	// whose approval its caller cannot give: "deny" refuses it and lets the model
	// explain, "error" fails the request, and "allow" runs it ungated. Empty is
	// treated as "deny". Only a caller with no answer channel consults it, so it
	// changes nothing for the IM and card surfaces.
	CompatApprovalMode string
	// Authz is the process-wide authorization coordinator, consulted before a
	// run starts (agent-level) and again on resume. Nil disables every
	// agent-level check, which is what a deployment without authz has.
	Authz *authz.Coordinator
	// AgentAuthz resolves an Agent's own authorization Webhooks. When an Agent
	// names them, they replace Authz for that Agent even if Authz is nil.
	AgentAuthz *agentauthz.Resolver
	// Interactions records one audit row per finished turn. A nil writer
	// records nothing, which is what a deployment without an audit store has.
	Interactions *interactionlog.Writer
	// Quota bounds how much one sender may ask in a day. A nil limiter allows
	// everything, which is what a deployment with no quota has.
	Quota *ratelimit.Limiter
	// TokenQuota bounds what one sender's asking may cost in a day, which is a
	// different ceiling from Quota's request count: one question answered from a
	// knowledge base can cost what a hundred greetings do. A nil limiter allows
	// everything.
	//
	// It is consulted here rather than in the engine because the refusal has to
	// land before a run starts — the point of a spend ceiling is not to pay for
	// the run that exceeds it — while the charge happens in relay, where the
	// run's totals reach this layer. See ratelimit.TokenLimiter for why those
	// are two operations.
	TokenQuota *ratelimit.TokenLimiter
	// Settings is the runtime-changeable settings snapshot: values an operator
	// can edit without a redeploy. Nil is a deployment with no settings backend,
	// in which case every lookup reads as unset.
	//
	// Nothing in this layer reads it today. It is kept as a dependency rather
	// than reached for through the package-level accessor so that a future
	// consumer is a reason Wire has to build the store — a dependency Wire
	// cannot see is one whose provider it silently drops.
	Settings *settingstore.Store
	// Webhooks delivers the run-lifecycle hooks the selected agents declared
	// (and the deployment's defaults for a run whose agents declared none). A
	// nil dispatcher calls nothing, which is what a deployment with no hooks
	// has.
	//
	// It is consulted here rather than in the engine for the same reason the
	// token ceiling is: an onStart hook's whole purpose is to refuse a run
	// before it costs anything, and a refusal that arrived after the engine had
	// started could only be a failed event. The onEnd half rides along in relay,
	// where the run's totals reach this layer.
	Webhooks *webhook.Dispatcher
}

// AgentBiz is the concrete IAgentBiz implementation.
type AgentBiz struct {
	deps Deps
	root context.Context
	// resolver turns an agent selection into the run's spec. It is derived from
	// deps (Resources + RequireApproval) rather than stored there, so there is
	// one source of truth for what a selection resolves from.
	resolver Resolver

	mu   sync.Mutex
	sems map[string]*semEntry // per-session concurrency slots
}

var _ agentapi.IAgentBiz = (*AgentBiz)(nil)

// New creates an AgentBiz. Sensible defaults are applied for zero-value deps.
func New(deps Deps) *AgentBiz {
	// The production value comes from agent.max-concurrent-per-conversation
	// (pkg/options, default 3). This fallback is for a caller that builds the biz
	// directly without the option — the tests do — and stays single-flight so
	// those callers keep the serialising behaviour they were written against.
	if deps.MaxConcurrentPerConv <= 0 {
		deps.MaxConcurrentPerConv = 1
	}
	if deps.RunTimeout <= 0 {
		deps.RunTimeout = 5 * time.Minute
	}
	root := deps.Root
	if root == nil {
		root = context.Background()
	}
	return &AgentBiz{
		deps:     deps,
		root:     root,
		resolver: NewResolver(deps.Resources, deps.RequireApproval),
		sems:     make(map[string]*semEntry),
	}
}

// semEntry is one session's in-flight allowance. refs counts the callers
// holding or about to hold this entry — not the slots in use — so the entry can
// be removed the moment nobody is interested in it. Without that count the map
// grew by one channel per session this process had ever seen, which is a leak
// shaped exactly like the busiest deployments.
type semEntry struct {
	ch   chan struct{}
	refs int
}

// acquire takes the per-conversation slot, or reports that it is busy. The
// returned release is idempotent: a caller that hands the run to a goroutine
// and also defers a release must not give the slot back twice.
func (b *AgentBiz) acquire(key string) (release func(), ok bool) {
	b.mu.Lock()
	e, found := b.sems[key]
	if !found {
		e = &semEntry{ch: make(chan struct{}, b.deps.MaxConcurrentPerConv)}
		b.sems[key] = e
	}
	e.refs++
	b.mu.Unlock()

	drop := func() {
		b.mu.Lock()
		e.refs--
		if e.refs <= 0 {
			delete(b.sems, key)
		}
		b.mu.Unlock()
	}

	select {
	case e.ch <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-e.ch
				drop()
			})
		}, true
	default:
		drop()
		return nil, false
	}
}

// withImageInliner publishes the model edge's resolver on the run's context.
//
// The engine reads it from there rather than holding it, because the handlers
// that resolve images are built from the agent set alone and reused across runs
// — a field would make them a function of the run. Both entry points publish it,
// Ask and Approve, since a resumed run needs it as much as a fresh one.
func (b *AgentBiz) withImageInliner(ctx context.Context) context.Context {
	if b.deps.Images == nil {
		return ctx
	}
	return agentedge.WithInliner(ctx, b.deps.Images)
}

// runContext builds the cancellable, timeout-bounded context one run executes
// in, and carries the run's identity into it.
//
// The context descends from the app-lifetime root rather than the request, so a
// run survives the caller returning but is cancelled on server shutdown. That
// also means nothing from the request context survives into it — including the
// asker's identity — so the three values a run needs are copied across by hand:
// the user ID (what MCP tools attribute their work to), the agent names (what a
// per-agent MCP server scopes its tools by), and the authorization subject
// (what authorization judges). Building them in one place is what keeps a run's
// identity from meaning different things depending on which entry point started
// it.
//
// The returned subject is the same value carried in the context. Authorization
// needs it in hand, not just in the context, so returning it keeps the caller
// from computing a second one that could disagree.
func (b *AgentBiz) runContext(
	deadline time.Duration, userID, userName, kind string, agents []string,
) (context.Context, context.CancelFunc, authz.Subject) {
	timeout := b.deps.RunTimeout
	if d := deadline; d > 0 && d < timeout {
		timeout = d
	}
	ctx, cancel := context.WithTimeout(b.root, timeout)
	ctx = contextx.WithUserID(ctx, userID)
	ctx = contextx.WithAgentNames(ctx, agents)
	subject := b.subjectForRun(ctx, userID, userName, kind)
	ctx = authz.ContextWithSubject(ctx, subject)
	return ctx, cancel, subject
}

// subjectForRun builds the caller's subject and attaches the user groups it
// belongs to.
//
// The group codes are read from the console-managed membership table (cached
// in-process). A read failure attaches no groups rather than failing the run:
// the policy is fail-closed, so a missing group cannot grant anything — it can
// only withhold a group-based grant, which is the safe direction and is logged
// for the operator.
//
// Only a user subject inherits group grants. A service account or an on-call
// group identity is not a person with memberships, and collapsing one into a
// user would silently hand it whatever that user was granted.
func (b *AgentBiz) subjectForRun(ctx context.Context, userID, userName, kind string) authz.Subject {
	subject := subjectForRun(userID, userName, kind)
	if subject.Kind != authz.SubjectKindUser || b.deps.Resources == nil {
		return subject
	}
	codes, err := b.deps.Resources.UserGroups().GroupCodesFor(ctx, subject.UserID)
	if err != nil {
		slog.WarnContext(ctx, "authz: could not resolve user groups; continuing with the user subject only",
			"user_id", subject.UserID, "error", err)
		return subject
	}
	if len(codes) == 0 {
		return subject
	}
	if subject.Extra == nil {
		subject.Extra = make(map[string]string, 1)
	}
	// The Extra value is a comma-separated list by the authz package's
	// convention; the policy splits it and unions it with on-call groups.
	subject.Extra["group_codes"] = strings.Join(codes, ",")
	return subject
}

// runSourceScheduled is the source the batch job controller stamps on the runs
// it dispatches. It is the signal that nobody is on the other end: the run was
// started by a CronJob occurrence (or a manual trigger of one), not by a person
// in a conversation.
const runSourceScheduled = "scheduled-run"

// askRunOptions assembles the options for a fresh run. Everything it reads has
// exactly one source, so it is built here rather than through a mirrored input
// struct: the mirror only added a second place for a field to be forgotten.
func askRunOptions(
	req *agentapi.AskRequest, admitted admittedRun, lease *runLease, reasoning bool,
) agentapi.RunOptions {
	unattended := req.Source == runSourceScheduled
	return agentapi.RunOptions{
		Reasoning:       reasoning,
		Model:           admitted.effectiveModel,
		Owned:           admitted.owned,
		Temperature:     req.Generation.Temperature,
		MaxOutputTokens: req.Generation.MaxOutputTokens,
		TopP:            req.Generation.TopP,
		ProviderOptions: req.Generation.ProviderOptions,
		Agents:          req.Selection.Agents,
		Instruction:     admitted.instruction,
		InstructionSet:  admitted.sel.spec.InstructionSet,
		MCPs:            admitted.mcps,
		Skills:          admitted.skills,
		KnowledgeBases:  admitted.kbs,
		Approval:        admitted.plan.patterns,
		// ReportUsage is already resolved: an asker that never mentioned the field
		// is the Lark path, whose runs nobody else meters, so "said nothing" means
		// report. See agentapi.ReportsUsage.
		ApprovalFallback: admitted.plan.fallback,
		RunID:            req.RunID,
		OnStopReady:      lease.stopReady(req.RunID),
		ReportUsage:      agentapi.ReportsUsage(req),
		// SenderID and SessionKey are carried for the ledger, not for the model:
		// the engine reports the run's totals back to the platform that owns them.
		// An empty SenderID is a run nobody could be named for, and the report is
		// then skipped rather than written to a row no one can read.
		SenderID:   req.Subject.ID,
		SessionKey: req.SessionID,
		// Nil unless the selection names delegates: the tool is added to the graph
		// only when it does.
		Delegation: delegationFor(req, admitted.sel),
		// Memory tools are part of the graph only when the agent asked for memory.
		MemoryEnabled: admitted.sel.spec.MemoryEnabled,
		// A narrowed run's runner cannot come from the cache: the cache builds
		// from the agent names and would hand back the full capability set.
		// An unattended run bypasses it for the same reason: its tool set is
		// narrower than the cached one, and the cache's build function is handed
		// the agent names alone.
		BypassRunnerCache: admitted.sel.narrowed || unattended,
		Unattended:        unattended,
	}
}

// resumeRunOptions assembles the options for the resumed half from what the
// pause recorded. Generation settings stay zero: a resume does not re-sample
// them.
func resumeRunOptions(
	req *agentapi.ApproveRequest, meta agentapi.RunMeta, sel resolvedSelection, lease *runLease,
) agentapi.RunOptions {
	skills := recordedOrResolved(meta.Skills, func() []string { return sel.spec.Skills })
	kbs := recordedOrResolved(meta.KnowledgeBases, func() []string { return sel.spec.KnowledgeBases })
	if sel.narrowed {
		// The recorded lists and the run's spec differ in exactly one case that
		// matters: a capability revoked while the pause waited. sel.spec is the
		// intersection, so it is the one to use — preferring the recorded list
		// here would hand the resumed half a name the agent no longer has.
		skills, kbs = sel.spec.Skills, sel.spec.KnowledgeBases
	}
	return agentapi.RunOptions{
		Reasoning:         meta.Reasoning,
		Model:             meta.Model,
		Owned:             meta.Owned,
		Agents:            meta.Agents,
		Instruction:       sel.spec.Instruction,
		InstructionSet:    sel.spec.InstructionSet,
		MCPs:              sel.spec.MCPs,
		Skills:            skills,
		KnowledgeBases:    kbs,
		Approval:          sel.spec.Approval,
		ApprovalFallback:  meta.ApprovalFallback,
		RunID:             req.RunID,
		OnStopReady:       lease.stopReady(req.RunID),
		SenderID:          meta.UserID,
		SessionKey:        meta.SessionID,
		ReportUsage:       meta.Metering.Reports(),
		Delegation:        resumeDelegation(meta, sel),
		BypassRunnerCache: sel.narrowed,
	}
}

// prepareHistory loads the prior turns, drains legacy inline images, inlines
// documents, and bounds the history before the engine sees it.
func (b *AgentBiz) prepareHistory(
	ctx context.Context, req *agentapi.AskRequest,
) ([]agentapi.Message, agentapi.Message) {
	history, err := b.deps.Sessions.Load(ctx, req.SessionID)
	if err != nil {
		slog.WarnContext(ctx, "could not load session history", "session", req.SessionID, "error", err)
		history = nil
	}
	// A session written before images were references holds base64 data URLs,
	// and loading one is the only moment anything sees them again. Moving them
	// into the store here is what drains the old shape rather than leaving it
	// to be re-read on every turn until the session expires.
	history = b.migrateInlineImages(ctx, req, history)
	// Documents become part of the question before anything is recorded: a
	// document is prompt material, so the history stores what the model will
	// actually read rather than a reference it could never open.
	req.Input.Text = b.inlineDocuments(ctx, req.Input.Text, req.Input.Documents)
	userMessage := agentapi.Message{Role: agentapi.RoleUser, Content: req.Input.Text, Images: req.Input.Images}

	// Bound the history before the engine sees it. Compaction persists what it
	// produces, so an over-budget session is condensed once rather than on
	// every turn. It runs on the request context, not the run context, so its
	// summarization calls are not billed to the run's token reporting.
	history = b.shrinkHistory(ctx, req.SessionID, history, userMessage)
	return history, userMessage
}

// admittedRun is the result of admission: the run context, the resolved
// selection, and the values derived from it before the engine is handed the run.
type admittedRun struct {
	ctx            context.Context
	sel            resolvedSelection
	effectiveModel string
	owned          bool
	plan           approvalPlan
	instruction    string
	mcps           []string
	skills         []string
	kbs            []string
}

// admitAndResolve authorizes the caller, resolves the selection, and applies
// the admission gate and per-turn memory prefetch.
//
// The run's identity and its bound are built together; see runContext for why
// the context descends from the app-lifetime root rather than the request. It is
// bound only after history preparation, so the run's timeout does not pay for
// compaction.
func (b *AgentBiz) admitAndResolve(
	ctx context.Context, lease *runLease, id runIdentity, req *agentapi.AskRequest, requested requestedModel,
) (admittedRun, error) {
	runCtx, sel, err := b.admitRun(ctx, lease, id)
	if err != nil {
		return admittedRun{}, err
	}
	effectiveModel, owned, err := b.resolveRunModel(ctx, requested, sel, req.Subject.ID)
	if err != nil {
		return admittedRun{}, err
	}
	plan := b.approvalPlan(ctx, sel.spec.Approval, req)
	instruction := sel.spec.Instruction
	mcps, skills, kbs := sel.spec.MCPs, sel.spec.Skills, sel.spec.KnowledgeBases

	// The admission gate: the run-lifecycle hooks this selection declared, asked
	// before the engine is handed the run — the point of an onStart hook being
	// that a refusal costs nothing.
	//
	// It sits here rather than beside the quota checks, which run before the
	// per-conversation slot is taken, because the hook names come off the agent
	// set loaded just above. Moving it up would mean loading that set twice to
	// release a slot a few milliseconds sooner; the slot is released on the
	// refusal path either way.
	if err := b.beforeAgent(ctx, webhookRunForRequest(req), sel.spec); err != nil {
		return admittedRun{}, err
	}

	// A skill the asker invoked has to be one this selection declares: the
	// caller's menu and this run read the same declaration, so a name outside it
	// means one of the two is looking at something that has changed. Refused
	// rather than dropped — see applyInvokedSkills.
	instruction, err = applyInvokedSkills(instruction, skills, req.Selection.InvokedSkills)
	if err != nil {
		return admittedRun{}, err
	}
	// Prefetch is per-turn context, not part of the pinned revision: it is
	// derived from this message and must not make every turn a new revision.
	if b.deps.Memory != nil && sel.spec.MemoryEnabled && sel.spec.MemoryName != "" {
		if snippet, merr := b.deps.Memory.Prefetch(ctx, sel.spec.MemoryName, firstAgent(sel), req.Subject.ID,
			req.SessionID, req.Input.Text, 0); merr != nil {
			slog.WarnContext(ctx, "memory prefetch failed", "run", req.RunID, "error", merr)
		} else if snippet != "" {
			instruction = snippet + "\n\n" + instruction
		}
	}
	return admittedRun{
		ctx: runCtx, sel: sel, effectiveModel: effectiveModel, owned: owned,
		plan: plan, instruction: instruction, mcps: mcps, skills: skills, kbs: kbs,
	}, nil
}

// Ask performs one AI turn and returns a backend-neutral event stream.
func (b *AgentBiz) Ask(ctx context.Context, req *agentapi.AskRequest) (<-chan agentapi.Event, error) {
	if req.RunID == "" {
		req.RunID = uuid.NewString()
	}
	// Measured from acceptance, not from the first model call: the audit record
	// answers "how long did the caller wait", and that includes everything this
	// service did before the model was involved.
	startedAt := time.Now()
	reasoning := ptr.FromOr(req.Generation.Reasoning, b.deps.DefaultReasoning)
	// A composed run names its model inside the composition; the handler also
	// copies it onto Model, but doing it here keeps direct callers honest.
	if req.Composition != nil && req.Composition.Model != "" {
		req.Model = req.Composition.Model
	}
	// A hostless composed run's model is resolved before admission, so it
	// cannot borrow applyComposition's normalization. Resolve it against the
	// referrer here: a bare name must mean that namespace, not whichever
	// namespace the store happens to scan first.
	//
	// A hosted composition is different: its referrer is the host agent, which
	// is not loaded yet. A bare model there goes through the store's
	// unique-match rule instead of being pinned to the deployment default.
	if req.Composition != nil && len(req.Selection.Agents) == 0 {
		req.Model = model.CanonicalEntry(sharing.Normalize(req.Composition.Namespace), req.Model)
	}
	if req.Composition != nil && req.Capabilities != nil {
		return nil, &CompositionError{
			Message: "a run cannot both narrow and replace an agent's capabilities",
		}
	}
	// A requested model may be user-owned, in which case its tokens do not
	// spend the platform's token budget. Resolve it before that ceiling so a
	// user who has exhausted the platform budget can still run on their key.
	// A composed run resolves strictly: it must not silently fall back to a
	// model the person did not pick.
	requested, err := b.resolveRequestedModel(ctx, req.Model, req.Subject.ID, req.Composition != nil)
	if err != nil {
		return nil, err
	}

	if err := b.chargeQuota(ctx, req.Subject.ID); err != nil {
		return nil, err
	}
	// Checked here, charged in relay: a spend ceiling has to refuse before the
	// money is spent, and relay is where the run's token total arrives. A run
	// on a user-owned endpoint spends none of the platform's tokens, so it is
	// not charged against that ceiling.
	if !requested.owned {
		if err := b.checkTokenQuota(ctx, req.Subject.ID); err != nil {
			return nil, err
		}
	}

	// Per-conversation concurrency gate (non-blocking). The slot is held for the
	// rest of the request, including the preparation below; the run's own clock
	// does not start until bind.
	lease, err := b.leaseRun(req.SessionID)
	if err != nil {
		return nil, err
	}
	// Every failure between here and the hand-off releases the slot. One guard
	// instead of a call at each return is what keeps a newly added admission
	// step from leaking the conversation slot: a leaked slot answers that
	// session with ErrConversationBusy until the process restarts.
	defer lease.unwind()

	// Build the message history: prior turns + this user turn.
	history, userMessage := b.prepareHistory(ctx, req)
	messages := append(append([]agentapi.Message{}, history...), userMessage)

	// The run's identity and its bound are built together; admission also
	// authorizes this caller and resolves the selection once, so the instruction,
	// the three resource scopes and the approval patterns all describe the same
	// agents.
	admitted, err := b.admitAndResolve(ctx, lease, runIdentity{
		RunID:        req.RunID,
		SessionID:    req.SessionID,
		Deadline:     req.Options.Deadline,
		SubjectID:    req.Subject.ID,
		SubjectName:  req.Subject.Name,
		SubjectKind:  req.Subject.Kind,
		Agents:       req.Selection.Agents,
		Capabilities: req.Capabilities,
		Composition:  req.Composition,
	}, req, requested)
	if err != nil {
		return nil, err
	}
	runCtx, sel := admitted.ctx, admitted.sel
	effectiveModel, owned := admitted.effectiveModel, admitted.owned
	plan, instruction := admitted.plan, admitted.instruction
	mcps, skills, kbs := admitted.mcps, admitted.skills, admitted.kbs

	// What this run is about to be. Same sentence answered two ways in two
	// conversations is what a previous investigation had to explain by reading
	// this file, because nothing recorded it: an agent selection decides the
	// instruction and the whole tool set, and two sessions can resolve to
	// different ones. mcp_count of zero is the fact worth knowing — it means the
	// model was handed no tools at all, which is not a model that chose not to
	// call one.
	//
	// No message bodies, here or anywhere: question and answer are measured, the
	// instruction only by size, and nothing from configuration is logged at all.
	// See the note on persist.
	slog.InfoContext(ctx, "agent run started",
		"run_id", req.RunID,
		"session", req.SessionID,
		"agents", req.Selection.Agents,
		"mcps", mcps,
		"mcp_count", len(mcps),
		"approval_patterns", plan.patterns,
		"has_instruction", instruction != "",
		"instruction_bytes", len(instruction),
		"history_messages", len(history),
		"reasoning", reasoning,
		"composed", req.Composition != nil,
		"hostless", req.Composition != nil && len(req.Selection.Agents) == 0,
		"composed_prompt_count", composedPromptCount(req.Composition))

	// The turn's ordinal, used by auto-saved memory: an interval of "every 3
	// turns" has to count turns of this conversation, and the history is the
	// only durable count of them.
	turn := userTurns(history) + 1
	// The images are resolved at the model edge, not here. Everything the engine
	// is handed carries references — they are what the session store writes, what
	// the next turn loads back, and what a paused run checkpoints — and only the
	// copy a model call is given ever holds bytes. Resolving them here instead
	// would put a data URL into the state adk checkpoints, which is the base64
	// payload in Redis this design exists to avoid; see imageResolveMiddleware.
	runOptions := askRunOptions(req, admitted, lease, reasoning)

	// An external A2A agent is run by the peer, not by the engine. Everything
	// before this point — admission, quota, the lease, history — is the same
	// for both kinds, and everything after it is the same relay that persists
	// the turn, so routing here is the whole difference.
	runtimeKind := b.runtimeKindFor(sel)
	engineEvents, external, err := b.askExternal(runCtx, req, sel, lease)
	if err != nil {
		return nil, err
	}
	if !external {
		engineEvents, err = b.dispatch(
			runCtx, sel,
			func(ctx context.Context) (<-chan agentapi.Event, error) {
				return b.deps.Engine.Run(ctx, messages, runOptions)
			},
			func(ctx context.Context) (<-chan agentapi.Event, error) {
				return b.runViaPort(ctx, req, sel, messages, runOptions)
			},
		)
		if err != nil {
			return nil, err
		}
	} else {
		runtimeKind = "a2a"
	}
	b.startRun(runCtx, req, sel, runtimeKind, instruction, effectiveModel)

	var actorID, actorName string
	if req.Actor != nil {
		actorID, actorName = req.Actor.ID, req.Actor.Name
	}

	// Ownership of the slot, the stop registration and the cancel moves to the
	// relay goroutine, whose defers release each exactly once. From here on the
	// guard above must not fire.
	return lease.handoff(engineEvents, suspend{
		RunID:  req.RunID,
		Memory: memoryPlanFor(sel.spec, firstAgent(sel), firstAgentNamespace(sel), req.Subject.ID, req.SessionID, req.RunID, turn),
		RunMeta: agentapi.RunMeta{
			Agents:             req.Selection.Agents,
			Model:              effectiveModel,
			Owned:              owned,
			Deadline:           req.Options.Deadline,
			SessionID:          req.SessionID,
			UserMessage:        userMessage,
			UserID:             req.Subject.ID,
			Reasoning:          reasoning,
			Metering:           req.Metering,
			ApprovalFallback:   plan.fallback,
			Skills:             skills,
			KnowledgeBases:     kbs,
			Capabilities:       req.Capabilities,
			Composition:        req.Composition,
			Source:             req.Source,
			SenderName:         req.Subject.Name,
			ActorID:            actorID,
			ActorName:          actorName,
			RuntimeKind:        runtimeKind,
			OutputSchema:       sel.spec.OutputSchema,
			OutputSchemaStrict: sel.spec.OutputSchemaStrict,
			SubjectKind:        req.Subject.Kind,
			Lineage:            req.Lineage,
			Turn:               turn,
			StartedAt:          unixNanosOrZero(startedAt),
		},
	}), nil
}

// Approve answers a run paused for approval and returns the event stream of the
// resumed run, which the caller pumps exactly as it pumps Ask's.
//
// It is a second run rather than a signal into the first: the pod that paused the
// run may be gone by the time a human answers, so the continuation is rebuilt
// from the stored checkpoint and may execute anywhere. The price of that is
// that this pod must reconstruct the run — its agent selection, and therefore
// its instruction, tools and policy — from what the pause recorded, since the
// checkpoint holds execution state and not the selection that produced it.
func (b *AgentBiz) Approve(ctx context.Context, req *agentapi.ApproveRequest) (<-chan agentapi.Event, error) {
	resumable, err := b.resumableEngine()
	if err != nil {
		return nil, err
	}

	meta, found, err := b.deps.Checkpoints.GetMeta(ctx, req.RunID)
	if err != nil {
		return nil, fmt.Errorf("load suspended run %s: %w", req.RunID, err)
	}
	if !found {
		slog.InfoContext(ctx, "approval for a run with no stored checkpoint", "run", req.RunID)
		return nil, ErrNotPending
	}
	// The request id ties the answer to the pause it was made against. One run
	// can pause repeatedly, and a card answered after the run moved on must be
	// refused rather than applied to whatever it is waiting on now.
	if meta.RequestID == "" || meta.RequestID != req.RequestID {
		slog.InfoContext(ctx, "approval does not match the run's pending pause",
			"run", req.RunID, "answered", req.RequestID, "pending", meta.RequestID)
		return nil, ErrNotPending
	}

	// Take the conversation's slot before consuming the answer, so a busy
	// conversation leaves the pause answerable rather than burning it: claiming
	// is one-shot, and a claim spent on a request that then fails would leave the
	// human with a card that can no longer be answered.
	lease, err := b.leaseRun(meta.SessionID)
	if err != nil {
		return nil, err
	}
	// Same guard as Ask: every failure below this line releases exactly once, and
	// the hand-off to the relay goroutine owns the release afterwards.
	defer lease.unwind()

	// The run's identity and its bound are built together. The resumed half runs
	// as the same agents the original did, so its MCP calls advertise the same
	// set and see the same tools rather than a default surface.
	//
	// It is re-authorized rather than inheriting the first half's decision,
	// because the rules may have changed while the run waited and "the first
	// half was allowed" is not an answer about the tools the second half will
	// call. Its kind is not carried by the checkpoint, so it re-enters as a
	// user: the pause is answered by a human through a surface that reports
	// users, and widening the stored metadata is a schema change this resume
	// does not need.
	//
	// admitRun does that authorization before the claim below, because a claim
	// is one-shot: a refusal after claiming would leave the human a card that
	// can no longer be answered.
	runCtx, sel, err := b.admitRun(ctx, lease, runIdentity{
		RunID:                req.RunID,
		SessionID:            meta.SessionID,
		Deadline:             meta.Deadline,
		SubjectID:            meta.UserID,
		Agents:               meta.Agents,
		Capabilities:         meta.Capabilities,
		CapabilitiesRecorded: true,
		Composition:          meta.Composition,
		CompositionRecorded:  true,
	})
	if err != nil {
		return nil, err
	}

	// The same admission gate the original ask passed, because the resumed half
	// spends money too. It runs before the claim for the same reason
	// authorization does.
	if err := b.beforeAgent(ctx, webhookRunForMeta(meta, req.RunID), sel.spec); err != nil {
		return nil, err
	}

	if err := b.claim(ctx, req); err != nil {
		return nil, err
	}
	b.decideApproval(ctx, req)
	b.markRunRunning(runCtx, req.RunID)

	allowAll := meta.AllowAll || req.Decision == agentapi.DecisionAllowAll

	metrics.M.ObserveApproval(req.Decision, "")

	// The resumed half delegates on the same terms the interrupted one did: same
	// delegates, same identity, same place in the chain. Restarting the chain at
	// depth zero would let a cycle escape the depth bound one pause at a time.
	//
	// It spends the same account's money, so it carries the same identity the
	// original ask did. Without SenderID a runtime-metered run reported nothing
	// for the continuation, and the ledger then showed only the half that
	// paused.
	resumeOptions := resumeRunOptions(req, meta, sel, lease)

	engineEvents, err := b.dispatch(
		runCtx, sel,
		func(ctx context.Context) (<-chan agentapi.Event, error) {
			return resumable.Resume(ctx, agentapi.ResumeOptions{
				Run:       resumeOptions,
				RequestID: req.RequestID,
				Decision:  req.Decision,
				Answers:   req.Answers,
				Reason:    req.Reason,
				Pending:   meta.Pending,
				AllowAll:  allowAll,
			})
		},
		func(ctx context.Context) (<-chan agentapi.Event, error) {
			return b.resumeViaPort(ctx, req, sel, meta.SessionID, meta.Deadline, resumeOptions)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("resume run %s: %w", req.RunID, err)
	}

	// The resumed half is the recorded run, with the answer applied. Rebuilding
	// the identity field by field is what let one of them be forgotten; the
	// checkpoint record is the identity.
	resumed := meta
	resumed.AllowAll = allowAll

	// Ownership of the slot, the stop registration and the cancel moves to the
	// relay goroutine, whose defers release each exactly once. From here on the
	// guard above must not fire.
	return lease.handoff(engineEvents, suspend{
		RunMeta: resumed,
		RunID:   req.RunID,
		Memory:  memoryPlanFor(sel.spec, firstAgent(sel), firstAgentNamespace(sel), resumed.UserID, resumed.SessionID, req.RunID, resumed.Turn),
	}), nil
}

// claim consumes the pause being answered, atomically across pods.
//
// Answering is not idempotent: it resumes the run and executes the tools it
// paused on. A double click therefore must not resume twice, and the claim is
// what decides which of the two clicks that was.
func (b *AgentBiz) claim(ctx context.Context, req *agentapi.ApproveRequest) error {
	claimed, err := b.deps.Checkpoints.Claim(ctx, req.RunID, req.RequestID)
	if err != nil {
		return fmt.Errorf("claim approval for run %s: %w", req.RunID, err)
	}
	if !claimed {
		slog.InfoContext(ctx, "approval already answered", "run", req.RunID, "request", req.RequestID)
		return ErrNotPending
	}
	return nil
}

// Stop cancels an in-flight run by ID (locally and across pods when Redis-backed).
//
// A run that is suspended rather than running has nothing here to cancel: it is
// parked on a checkpoint, and dropping that checkpoint is what ends it. Both are
// attempted, because one request means both things to the person who made it: a
// run that is still generating is cancelled, and a run parked on a decision is
// withdrawn. Leaving the checkpoint behind would let an answer sent before the
// stop arrive afterwards, execute the very tools the user stopped, and continue a
// turn on a card they have already closed.
func (b *AgentBiz) Stop(ctx context.Context, runID string) error {
	stopErr := error(nil)
	if b.deps.Stops != nil {
		stopErr = b.deps.Stops.RequestStop(ctx, runID)
	}
	var delErr error
	if b.deps.Checkpoints != nil {
		// Deleting a checkpoint that does not exist is the ordinary case here —
		// most runs never suspend — and is documented as a no-op.
		if err := b.deps.Checkpoints.Delete(ctx, runID); err != nil {
			delErr = fmt.Errorf("drop the suspension of run %s: %w", runID, err)
		}
	}
	if stopErr != nil {
		return stopErr
	}
	return delErr
}

// suspend is the run identity a pause must record, and the state a resume reads
// back to rebuild the run. Its fields are those Ask holds and Approve cannot
// recover from the checkpoint, which records execution state rather than the
// selection that produced it.
// suspend is the run identity a pause records and a resume reads back. RunMeta
// is embedded because it is exactly that record: the pause-time fields
// (RequestID, Pending, ToolTrail) are filled in by meta, and everything else is
// already decided when the run starts. Keeping two parallel shapes here was how
// a field added to one of them went missing from the other.
type suspend struct {
	agentapi.RunMeta
	RunID string
	// Memory is the auto-save plan, nil when this run does not save one. It is
	// resolved at admission because the agent can be edited mid-run, and a turn
	// must be saved under the settings it ran under.
	Memory *memoryPlan
}

// meta describes the run for storage beside its checkpoint.
//
// trail is the record observed so far, taken as an argument rather than read off
// the receiver because a pause is recorded from inside the loop that is
// observing it: the copy of suspend this method was reached through predates
// every tool the run has called.
func (s suspend) meta(it *agentapi.Interrupt, trail []string) agentapi.RunMeta {
	m := s.RunMeta
	m.RequestID = it.RequestID
	m.ToolTrail = trail
	// A new slice every time: appending to the copy would accumulate the
	// pending addresses of every earlier pause, and a resumed run that pauses
	// twice would answer targets it never reached.
	m.Pending = make([]agentapi.PendingAddress, 0, len(it.Requests))
	for _, r := range it.Requests {
		m.Pending = append(m.Pending, agentapi.PendingAddress{
			Address:  r.Address,
			Kind:     r.Kind,
			ToolName: r.ToolName,
		})
	}
	return m
}

// relayState is the bookkeeping one run's event loop accumulates. It is a
// struct rather than a stack of locals so the loop body (applyEvent) and the
// exit path (settle) can stay separate without a closure capturing a dozen
// variables.
type relayState struct {
	sp    suspend
	out   chan<- agentapi.Event
	trail *toolTrail

	answer     string
	status     string
	errMessage string
	suspended  bool

	// spent is the run's cumulative token total, taken from the last
	// usage-bearing event. The events carry running totals rather than per-call
	// deltas, so the last one seen is the whole run's cost — summing them would
	// multiply it by the number of rounds.
	spent int64
	// usageTotals is that event's totals, kept whole so the onEnd report can
	// carry the same five numbers the ledger does rather than just their sum.
	usageTotals *agentapi.Usage
	// heldTerminal is the run's last event, delivered only after the run row is
	// written; see the delivery site.
	heldTerminal *agentapi.Event
	validator    *outputschema.Validator
}

// newRelayState starts a run's bookkeeping. The tool trail and the output
// validator are resolved once, before the first event.
func newRelayState(ctx context.Context, sp suspend, out chan<- agentapi.Event) *relayState {
	st := &relayState{
		sp:  sp,
		out: out,
		// The record of what this run's tools did. It is written into the
		// conversation when the turn ends, which is what keeps the assistant's
		// account of its own work from being the only thing a later turn can read.
		trail: newToolTrail(sp.ToolTrail),
	}
	if sp.OutputSchema != "" {
		compiled, err := outputschema.NewValidator([]byte(sp.OutputSchema))
		if err != nil {
			slog.WarnContext(ctx, "could not compile the output schema; the run will not be validated",
				"run", sp.RunID, "error", err)
		} else {
			st.validator = compiled
		}
	}
	return st
}

// applyEvent folds one engine event into the state. The returned event is what
// the caller may see; ok is false when there is nothing to deliver — a partial
// structured answer, or a terminal event held until the run row exists.
func (b *AgentBiz) applyEvent(ctx context.Context, st *relayState, ev agentapi.Event) (agentapi.Event, bool) {
	sp := st.sp
	switch ev.Type {
	case agentapi.EventUsage:
		if ev.Usage != nil {
			st.spent = int64(ev.Usage.PromptTokens) + int64(ev.Usage.CompletionTokens)
			st.usageTotals = ev.Usage
		}
	case agentapi.EventInterrupted:
		// Recorded before the pause reaches the caller. The answer can only
		// follow a pause a human has seen, and it may be served by a pod that
		// has nothing but this metadata.
		b.recordSuspend(ctx, sp, ev.Interrupt, st.trail.record())
		b.markRunSuspended(ctx, sp.RunID)
	case agentapi.EventSuspended:
		st.suspended = true
	case agentapi.EventToolStep:
		st.trail.observe(ev.Steps)
	case agentapi.EventDone:
		if st.validator != nil {
			if err := st.validator.ValidateJSON(ev.Final); err != nil {
				st.status = interactionlog.StatusError
				st.errMessage = "the answer does not match the output schema"
				ev = agentapi.Event{Type: agentapi.EventFailed, Err: fmt.Errorf("answer does not match the output schema: %w", err)}
				break
			}
		}
		st.answer = ev.Final
		// Recorded after validation, so a run whose answer failed its schema is
		// not remembered as if it had answered.
		b.autoSaveMemory(ctx, sp.Memory, userQuestion(sp.UserMessage), st.answer)
		// The digest travels with the answer so a surface can publish the
		// structured form and name the contract it satisfies, without
		// recompiling the schema it does not hold.
		if st.validator != nil {
			ev.OutputSchemaSHA256 = st.validator.Digest()
		}
		st.status = interactionlog.StatusSuccess
		if ev.Usage != nil {
			st.spent = int64(ev.Usage.PromptTokens) + int64(ev.Usage.CompletionTokens)
			st.usageTotals = ev.Usage
		}
	case agentapi.EventStopped:
		st.status = interactionlog.StatusStopped
	case agentapi.EventFailed:
		st.status = interactionlog.StatusError
		if ev.Err != nil {
			st.errMessage = ev.Err.Error()
		}
	}
	// A structured answer is published only after it is complete and valid;
	// forwarding the partial JSON would hand a renderer an invalid document.
	if st.validator != nil && ev.Type == agentapi.EventAnswer {
		return agentapi.Event{}, false
	}
	// The terminal event is held back until the run row is settled. A caller
	// that reads "completed" and immediately queries the run must find it:
	// publishing the event first makes the answer visible before the record it
	// describes, which is the window a resume or an audit would land in and see
	// nothing.
	if ev.Type == agentapi.EventDone || ev.Type == agentapi.EventFailed || ev.Type == agentapi.EventStopped {
		terminal := ev
		st.heldTerminal = &terminal
		return agentapi.Event{}, false
	}
	return ev, true
}

// settle finishes a run that did not pause: it settles the row, publishes the
// held terminal event, and then writes the conversation, the audit and the
// onEnd report.
func (b *AgentBiz) settle(ctx context.Context, st *relayState) {
	sp := st.sp
	b.release(ctx, sp.RunID)
	b.finishRun(ctx, sp.RunID, st.answer, st.status, st.errMessage, st.usageTotals)
	// Now that the record exists, the caller may be told the run ended.
	if st.heldTerminal != nil {
		deliver(ctx, st.out, *st.heldTerminal)
	}
	b.persist(ctx, sp, st.answer, st.trail.record())
	b.audit(ctx, sp, st.answer, st.status, st.errMessage)
	// The onEnd report is the last thing and the only one allowed to be slow: it
	// runs in its own goroutine, so an endpoint that takes its whole timeout
	// delays nothing the caller is waiting on.
	b.afterAgent(ctx, webhookRunForMeta(sp.RunMeta, sp.RunID),
		b.resolver.Resolve(ctx, sp.Agents), st.status, webhookUsage(st.usageTotals))
}

// relay forwards one engine stream to the caller, recording the run if it pauses
// and releasing it if it finishes. Ask and Approve share it because a resumed run
// ends exactly as a fresh one does.
func (b *AgentBiz) relay(runCtx context.Context, engineEvents <-chan agentapi.Event, out chan<- agentapi.Event, sp suspend) {
	defer endRunSpan(runCtx)
	st := newRelayState(runCtx, sp, out)
	// Deferred so every exit path charges: the loop also returns early when the
	// caller has gone away, and a run that was answered but not read still cost
	// money. The closure reads st.spent at exit, so it sees the last total.
	defer func() {
		if !sp.Owned {
			b.chargeTokens(runCtx, sp.UserID, st.spent)
		}
	}()
	for ev := range engineEvents {
		ev, ok := b.applyEvent(runCtx, st, ev)
		if !ok {
			continue
		}
		if !deliver(runCtx, out, ev) {
			return
		}
	}

	if st.suspended {
		// The run only paused. Its checkpoint is precisely the state the resume
		// picks up, so nothing of the run is finished here — not the checkpoint,
		// and not the exchange, which is not an exchange yet.
		return
	}
	b.settle(runCtx, st)
}

// deliver hands one event to the caller.
//
// It tries the channel first: a run being cancelled still owes the caller that
// is reading its terminal event. Only a full buffer — a reader that is not
// draining — puts us in the select, where a cancelled run means nobody is
// coming and this goroutine must not park forever holding the conversation's
// slot.
func deliver(ctx context.Context, out chan<- agentapi.Event, event agentapi.Event) bool {
	select {
	case out <- event:
		return true
	default:
	}
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

// userTurns counts the user messages in a conversation's history.
//
// It is the turn ordinal's basis rather than a counter in the session row: the
// history is the durable record, so a turn count derived from it survives a
// restart and cannot drift from what the conversation actually contains.
func userTurns(history []agentapi.Message) int {
	turns := 0
	for _, message := range history {
		if message.Role == "user" {
			turns++
		}
	}
	return turns
}

// userQuestion reads the question out of the recorded user turn.
func userQuestion(message agentapi.Message) string { return message.Content }

// audit records the finished turn. It runs for every terminal outcome,
// including the failed and stopped ones the conversation memory deliberately
// does not keep: an audit that only lists the turns that worked cannot answer
// what was asked of the system, only what it agreed to do.
func (b *AgentBiz) audit(ctx context.Context, sp suspend, answer, status, errMessage string) {
	if !b.deps.Interactions.Enabled() && metrics.M == nil {
		return
	}
	if status == "" {
		// A stream that ended without a terminal event: the caller saw it close,
		// so the turn is over either way.
		status = interactionlog.StatusError
		if errMessage == "" {
			errMessage = "the run ended without reporting an outcome"
		}
	}
	var elapsed time.Duration
	if started := startedAtFrom(sp.StartedAt); !started.IsZero() {
		elapsed = time.Since(started)
	}
	b.observeRun(status, elapsed)
	// The row names the party that started the turn, which is the run's own
	// subject unless a surface delegated it (see AskRequest.ActorID). Naming the
	// subject there would record a delegated run — the management surface's
	// debug run, which executes as the agent's owner — against a user who never
	// asked for it.
	senderID, senderName := sp.UserID, sp.SenderName
	if sp.ActorID != "" || sp.ActorName != "" {
		senderID, senderName = sp.ActorID, sp.ActorName
	}
	b.deps.Interactions.Write(ctx, interactionlog.Entry{
		Source:       sp.Source,
		SessionKey:   sp.SessionID,
		SenderID:     senderID,
		SenderName:   senderName,
		AgentNames:   sp.Agents,
		UserInput:    sp.UserMessage.Content,
		FinalAnswer:  answer,
		Status:       status,
		ErrorMessage: errMessage,
		Duration:     elapsed,
	})
}

// observeRun records the turn's outcome. It is called from audit, which is the
// one place a turn's terminal status and duration are both known.
func (b *AgentBiz) observeRun(status string, elapsed time.Duration) {
	engine := "unknown"
	if b.deps.Engine != nil {
		engine = b.deps.Engine.Name()
	}
	metrics.M.ObserveRun(status, engine, elapsed)
}

// recordSuspend stores what a resume will need, beside the checkpoint the runner
// has already written. A run it cannot record is one no answer can continue, so
// the failure is logged as an error rather than swallowed: the human will find
// the card unanswerable, and this is the only place that says why.
func (b *AgentBiz) recordSuspend(ctx context.Context, sp suspend, it *agentapi.Interrupt, trail []string) {
	if b.deps.Checkpoints == nil || it == nil {
		return
	}
	if err := b.deps.Checkpoints.PutMeta(ctx, sp.RunID, sp.meta(it, trail)); err != nil {
		slog.ErrorContext(ctx, "could not record suspended run; its approval cannot be resumed",
			"run", sp.RunID, "request", it.RequestID, "error", err)
		return
	}
	b.recordApprovals(ctx, sp, it)
	slog.InfoContext(ctx, "agent run suspended for approval",
		"run", sp.RunID, "request", it.RequestID, "pending", len(it.Requests))
}

// release drops a finished run's checkpoint, which is what makes a second answer
// to its last pause fail instead of re-running it. It is the only cleanup a run
// gets besides the store's TTL.
func (b *AgentBiz) release(ctx context.Context, runID string) {
	if b.deps.Checkpoints == nil || runID == "" {
		return
	}
	if err := b.deps.Checkpoints.Delete(ctx, runID); err != nil {
		slog.WarnContext(ctx, "could not release finished run's checkpoint", "run", runID, "error", err)
	}
}

// persist appends the finished exchange to the session so the next turn has
// memory. The run context is used so persistence is not cut when the caller
// returns before the run completes.
//
// The record of the run's tool calls is appended after the answer, and on every
// turn. Without it the assistant's own prose is the only account of what the run
// did, and a turn that claimed a result it never produced leaves that claim in
// the conversation as a fact the next turn reads back. Placing it after the
// answer puts it where the two can be compared: the claim, then the record of
// what was actually called.
//
// It is a user message because that is the only role that reaches the model —
// system-role history is dropped before the next request is built, the same
// reason the history summary is a user message (see summarizeOldest). The
// annotation says what it is, so it can never be read as something the user
// typed.
func (b *AgentBiz) persist(ctx context.Context, sp suspend, answer string, trail []string) {
	if answer == "" || sp.SessionID == "" || b.deps.Sessions == nil {
		return
	}
	if err := b.deps.Sessions.Append(ctx, sp.SessionID, sp.UserMessage); err != nil {
		slog.WarnContext(ctx, "could not persist user message", "session", sp.SessionID, "error", err)
	}
	assistant := agentapi.Message{Role: agentapi.RoleAssistant, Content: answer}
	if err := b.deps.Sessions.Append(ctx, sp.SessionID, assistant); err != nil {
		slog.WarnContext(ctx, "could not persist assistant message", "session", sp.SessionID, "error", err)
	}
	note := agentapi.Message{Role: agentapi.RoleUser, Content: renderToolTrail(trail)}
	if err := b.deps.Sessions.Append(ctx, sp.SessionID, note); err != nil {
		slog.WarnContext(ctx, "could not persist the tool-call record", "session", sp.SessionID, "error", err)
	}
	// Recorded beside the history rather than derived from it: a session whose
	// history was just trimmed to nothing is still a session that was used, and
	// deriving the time from the history would call it idle.
	if err := b.deps.Sessions.SetLastActive(ctx, sp.SessionID, time.Now()); err != nil {
		slog.WarnContext(ctx, "could not record the session's last-active time", "session", sp.SessionID, "error", err)
	}

	// What this turn left in the conversation's memory, and the line that makes
	// "why did the same question go two ways" a query rather than an
	// investigation: run_id joins this to the run-start line above and to the
	// caller's own log, and tools is the answer the record gave — empty when the
	// run called nothing, which is the case worth being able to find.
	//
	// The answer is measured, never quoted. It is the user's reply, it is the one
	// field here most likely to contain something they would not expect to find
	// in a log, and its length is all an operator needs to tell one turn from
	// another. Nothing from configuration is logged at all — no model, no
	// endpoint, no key.
	slog.InfoContext(ctx, "agent turn persisted",
		"run_id", sp.RunID,
		"session", sp.SessionID,
		"tools", trail,
		"answer_bytes", len(answer))
}

// unixNanosOrZero renders a start time for storage, keeping the zero time as
// zero rather than as the epoch.
func unixNanosOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// startedAtFrom restores the moment a suspended run's caller started waiting.
// A checkpoint written before that field existed carries zero, which would
// otherwise be read as 1970 and record a turn that took half a century.
func startedAtFrom(unixNanos int64) time.Time {
	if unixNanos <= 0 {
		return time.Time{}
	}
	return time.Unix(0, unixNanos)
}

// allowance is the shared shape of the two daily ceilings: the request count
// and the token spend. Their Limit and Allow use int and int64 respectively,
// so the helper is generic over the counter rather than forcing one of them to
// convert.
type allowance[N ~int | ~int64] interface {
	Enabled() bool
	Allow(ctx context.Context, senderID string) (bool, N, error)
	Limit() N
}

// enforceAllowance applies one daily ceiling: skip when disabled, fail open
// when the limiter cannot answer, and refuse with ErrQuotaExceeded when it has
// no room.
//
// Failing open is deliberate. The ratelimit package documents why: a quota that
// fails closed turns a cache outage into an outage of the product.
//
// The refusal is wrapped rather than returned bare, so errors.Is still
// identifies it while the message names the allowance that was reached.
func enforceAllowance[N ~int | ~int64](
	ctx context.Context, senderID string, a allowance[N],
	refused, unavailable, granted string,
	describe func(N) string,
) error {
	if !a.Enabled() {
		return nil
	}
	allowed, remaining, err := a.Allow(ctx, senderID)
	if err != nil {
		slog.WarnContext(ctx, unavailable, "error", err)
		return nil
	}
	if !allowed {
		slog.InfoContext(ctx, refused, "sender", senderID, "limit", a.Limit())
		return fmt.Errorf("%w: %s", ErrQuotaExceeded, describe(a.Limit()))
	}
	slog.DebugContext(ctx, granted, "sender", senderID, "remaining", remaining)
	return nil
}

// chargeQuota counts one request against the sender's daily allowance.
//
// There is no role exemption today: the limit is deployment-wide and applies to
// every sender, SRE included. If a role ever needs a different allowance, it
// belongs in the quota policy rather than in a hard-coded check here.
func (b *AgentBiz) chargeQuota(ctx context.Context, senderID string) error {
	return enforceAllowance(ctx, senderID, b.deps.Quota,
		"agent ask refused: daily quota reached",
		"could not count a request against the daily quota; allowing it",
		"daily quota charged",
		ratelimit.Describe)
}

// checkTokenQuota refuses a run whose sender has already spent the day's token
// ceiling.
//
// There is no role exemption here either; see chargeQuota.
//
// A refusal reuses ErrQuotaExceeded rather than a sentinel of its own. The
// distinction the handler draws is "not allowed" (403) against "not right now"
// (429), and a spent daily allowance is the second whatever it is counted in —
// a new sentinel would need a new mapping and would answer 500 until someone
// added it.
func (b *AgentBiz) checkTokenQuota(ctx context.Context, senderID string) error {
	return enforceAllowance(ctx, senderID, b.deps.TokenQuota,
		"agent ask refused: daily token ceiling reached",
		"could not read the day's token spend; allowing the run",
		"daily token ceiling has room",
		ratelimit.DescribeTokens)
}

// chargeTokens records what a finished run cost against its sender's daily
// ceiling.
//
// It is called once per run from relay, with the last cumulative total the run
// reported. A failure only logs: the run has already been answered, and a
// bookkeeping error is not a reason to fail it — the next run's check simply
// sees a total that is too low, which is the fail-open direction the package
// documents.
func (b *AgentBiz) chargeTokens(ctx context.Context, senderID string, tokens int64) {
	if !b.deps.TokenQuota.Enabled() || tokens <= 0 {
		return
	}
	// A context of its own: the run's is cancelled the moment it ends (a stopped
	// run's before relay even exits), and charging on it would fail for exactly
	// the runs that were stopped early — the ones whose spend is easiest to lose
	// track of.
	chargeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenChargeTimeout)
	defer cancel()
	if err := b.deps.TokenQuota.Add(chargeCtx, senderID, tokens); err != nil {
		slog.WarnContext(ctx, "could not charge the run's tokens to the daily ceiling", "error", err)
	}
}

// webhookRun is the per-run half of what a hook is told, gathered from
// whichever call site is asking: a fresh run has the request, a finished or
// resumed one has the run metadata. Everything here is either the server's own
// decision (Surface) or the caller's authenticated assertion (Subject) — never
// something a receiver supplied.
type webhookRun struct {
	Agents    []string
	Surface   string
	Subject   webhookclient.Subject
	SessionID string
	RunID     string
}

// webhookRunForRequest gathers the hook inputs from an incoming ask.
func webhookRunForRequest(req *agentapi.AskRequest) webhookRun {
	return webhookRun{
		Agents:  req.Selection.Agents,
		Surface: req.Source,
		Subject: webhookclient.Subject{
			// A resolved identity is authoritative: a delegated run speaks for
			// whoever delegated it, and the surface it arrived on says nothing
			// about that.
			Kind: subjectKindOr(req.Subject.Kind, req.Source),
			ID:   req.Subject.ID,
			Name: req.Subject.Name,
		},
		SessionID: req.SessionID,
		RunID:     req.RunID,
	}
}

// webhookRunForMeta gathers the hook inputs from a recorded run: the resumed
// half that Approve is about to start, or the run relay has just finished.
// runID is a parameter because RunMeta is the checkpoint's record and does not
// carry one.
//
// The subject kind comes from the recorded surface, not RunMeta.SubjectKind:
// that is the identity both existing call sites reported, and a hook that saw a
// different kind after a pause would be looking at a different principal.
func webhookRunForMeta(meta agentapi.RunMeta, runID string) webhookRun {
	return webhookRun{
		Agents:  meta.Agents,
		Surface: meta.Source,
		Subject: webhookclient.Subject{
			Kind: webhookSubjectKind(meta.Source),
			ID:   meta.UserID,
			Name: meta.SenderName,
		},
		SessionID: meta.SessionID,
		RunID:     runID,
	}
}

// webhookSubjectKind reports how a hook should read the asker. A2A is the one
// surface whose asker is a service rather than a person — its sender is a fixed
// constant, not an account — so it is the one that is not reported as a user.
func webhookSubjectKind(surface string) string {
	if surface == sourceA2A {
		return "serviceaccount"
	}
	return "user"
}

// subjectKindOr prefers an identity's own kind over the one its surface implies.
func subjectKindOr(kind, surface string) string {
	if kind != "" {
		return kind
	}
	return webhookSubjectKind(surface)
}

// beforeAgent asks the run-lifecycle hooks whether this run may proceed.
//
// A dispatcher with no source and no default (no hooks configured anywhere)
// allows everything, which is the state of a deployment that has not adopted
// them. A run the hooks cannot be asked about at all — the hook table is not
// answering — is allowed too: that is the same fail-open judgement the
// dispatcher makes about an unreachable endpoint, applied one level up.
func (b *AgentBiz) beforeAgent(ctx context.Context, run webhookRun, spec RunSpec) error {
	if b.deps.Webhooks == nil || !b.deps.Webhooks.Enabled() {
		return nil
	}
	decision, err := b.deps.Webhooks.BeforeAgent(ctx, b.webhookInvocation(run, spec))
	if err != nil {
		slog.WarnContext(ctx, "webhook: the run-lifecycle hooks could not be asked; allowing the run", "error", err)
		return nil
	}
	if decision.Allowed {
		return nil
	}
	slog.InfoContext(ctx, "webhook: a run-lifecycle hook refused the run",
		"hook", decision.Hook, "code", decision.Code, "run_id", run.RunID, "surface", run.Surface)
	if isAllowanceCode(decision.Code) {
		return fmt.Errorf("%w: %s", ErrQuotaExceeded, decision.Message)
	}
	return &RunRefusedError{Message: decision.Message, Hook: decision.Hook}
}

// afterAgent reports a finished run to the hooks that subscribe to onEnd.
//
// It runs on a context of its own and in a goroutine of its own: the run's
// context is cancelled the moment it ends, so reporting on it would fail for
// exactly the runs whose usage is easiest to lose track of — and the answer has
// already been delivered, so nothing here may delay the caller's return.
func (b *AgentBiz) afterAgent(ctx context.Context, run webhookRun, spec RunSpec, status string, usage webhookclient.Usage) {
	if b.deps.Webhooks == nil || !b.deps.Webhooks.Enabled() {
		return
	}
	result := webhook.InvocationResult{
		Invocation: b.webhookInvocation(run, spec),
		Status:     status,
		Usage:      usage,
	}
	go b.deps.Webhooks.AfterAgent(context.WithoutCancel(ctx), result)
}

// webhookInvocation describes one run to the hooks.
func (b *AgentBiz) webhookInvocation(run webhookRun, spec RunSpec) webhook.Invocation {
	// The run's headline agent is the first one it selected; an empty selection
	// is the engine's built-in assistant, which has no name.
	agent := ""
	if len(spec.Agents) > 0 {
		agent = spec.Agents[0]
	}
	return webhook.Invocation{
		HookNames:  spec.Webhooks,
		Agent:      agent,
		Agents:     run.Agents,
		Surface:    run.Surface,
		Subject:    run.Subject,
		SessionKey: run.SessionID,
		RunID:      run.RunID,
	}
}

// sourceA2A is the surface name the A2A executor stamps on its asks. It has no
// constant of its own in that package (the value is written at the one place
// that sets it), so it is named here for the one reader that has to tell it
// from a person's request.
const sourceA2A = "a2a"

// webhookUsage converts a run's totals into the payload's usage block. Five
// numbers rather than their sum, because they are the same five the platform's
// ledger keeps, and a receiver comparing the two should see one vocabulary.
func webhookUsage(u *agentapi.Usage) webhookclient.Usage {
	if u == nil {
		return webhookclient.Usage{}
	}
	return webhookclient.Usage{
		PromptTokens:     int64(u.PromptTokens),
		CompletionTokens: int64(u.CompletionTokens),
		Calls:            int64(u.Calls),
		CacheHitTokens:   int64(u.CacheHitTokens),
		CacheMissTokens:  int64(u.CacheMissTokens),
	}
}

// isAllowanceCode reports whether a hook's code names a spending condition
// rather than a permission. Those keep ErrQuotaExceeded and its 429 — "not
// right now" — so a client can tell them from a refusal it should not retry.
// An unnamed code is treated as an allowance one: a hook that did not say why
// is likelier to be a budget gate, and 429 is the safer of the two defaults
// because it leaves the caller free to try again.
func isAllowanceCode(code string) bool {
	switch code {
	case "", "quotaExceeded", "tierRateLimited", "tokenBudgetExceeded":
		return true
	}
	return false
}

// tokenChargeTimeout bounds one token charge. Short, because it is a Redis
// INCRBY on the run's critical path out of relay, and because a charge that
// cannot land in this long is one the next run's check will under-count — the
// fail-open direction.
const tokenChargeTimeout = 3 * time.Second

// Clear resets the conversation memory for the given session key.
func (b *AgentBiz) Clear(ctx context.Context, sessionKey string) error {
	if b.deps.Sessions == nil {
		return nil
	}
	return b.deps.Sessions.Clear(ctx, sessionKey)
}

// CheckAgentAccess answers whether subjectID may use agentName.
//
// # Why it constructs the subject rather than taking one
//
// The callers that need this question ask it about a person, and building the
// Subject is a decision about which field carries the identity — the same
// decision subjectForRun makes for a run. Making it here keeps that decision in
// one package; a caller that built its own Subject could set a different field
// and get a different answer for the same person.
//
// # A nil Coordinator allows
//
// That is the Coordinator's own documented behaviour when authorization is not
// wired (a deployment with no OPA), and repeating the check here would make this
// method disagree with the gate it is meant to preview.
func (b *AgentBiz) CheckAgentAccess(ctx context.Context, subjectID, agentName string) (bool, string, error) {
	if agentName == "" {
		return false, "agent name is empty", nil
	}
	subj := b.subjectForRun(ctx, subjectID, subjectID, "")
	decision, reason, err := b.deps.Authz.AuthorizeAgent(ctx, subj, agentName)
	if err != nil {
		return false, "", err
	}
	// Only an explicit Allow permits. NoOpinion means no rule spoke, and the run
	// path fails closed on it — a preview that answered true there would promise
	// something the gate then refuses.
	return decision == authz.DecisionAllow, reason, nil
}

// approvalPlan is what one request's approval policy resolves to: the patterns
// that are gated, and what a gated call does when this caller has no way to
// answer — empty when it can.
type approvalPlan struct {
	patterns []string
	fallback string
}

// approvalPlanFor resolves the request's approval policy.
//
// A caller with no answer channel — the OpenAI-compatible endpoint — must never
// be left holding a question: there is no card to click and no approve call to
// make, so a run that paused for it would end with a question nobody sees and an
// empty answer for the caller. What such a request does instead is
// agent.compat-approval-mode: "allow" gates nothing, "deny" refuses and lets the
// model explain, "error" fails the request. Only a caller that says it cannot
// answer consults this, so nothing here changes the behaviour of the surfaces
// that can.
func (b *AgentBiz) approvalPlan(ctx context.Context, patterns []string, req *agentapi.AskRequest) approvalPlan {
	if req.Options.Approval.Channel != agentapi.ApprovalChannelNone {
		return approvalPlan{patterns: patterns}
	}

	mode := b.deps.CompatApprovalMode
	if req.Options.Approval.Fallback != "" {
		mode = req.Options.Approval.Fallback
	}
	switch mode {
	case "":
		mode = agentapi.ApprovalFallbackDeny
	case customoptions.CompatApprovalAllow:
		return approvalPlan{}
	case agentapi.ApprovalFallbackDeny, agentapi.ApprovalFallbackError:
	default:
		// The options validation refuses anything else at startup, so this is a
		// mode set programmatically. A gate that cannot ask falls back to refusing
		// rather than to running unapproved.
		slog.WarnContext(ctx, "unknown compat approval mode; gated tools will be refused",
			"mode", mode)
		mode = agentapi.ApprovalFallbackDeny
	}
	return approvalPlan{patterns: patterns, fallback: mode}
}

// maxDocumentChars bounds what attached documents may add to one question.
const maxDocumentChars = 512 << 10

// inlineDocuments turns attached documents into text on the question.
//
// A document that cannot be read leaves a note in its place rather than being
// dropped: a model that is told a file could not be read answers differently
// from one that never learns a file was attached.
func (b *AgentBiz) inlineDocuments(ctx context.Context, question string, documents []media.Ref) string {
	if len(documents) == 0 {
		return question
	}
	var builder strings.Builder
	builder.WriteString(question)
	budget := maxDocumentChars
	for _, document := range documents {
		if budget <= 0 {
			builder.WriteString("\n\n[more documents were attached than fit in one request]")
			break
		}
		// Without an inliner the documents cannot be read at all, which is a
		// deployment fact the model should be told rather than left to guess.
		var text string
		ok := false
		if b.deps.Images != nil {
			text, ok = b.deps.Images.ReadDocument(ctx, document)
		}
		name := document.Name
		if name == "" {
			name = "attachment"
		}
		if !ok {
			builder.WriteString(fmt.Sprintf("\n\n<document name=%q>could not be read</document>", name))
			continue
		}
		if len(text) > budget {
			text = text[:budget]
		}
		budget -= len(text)
		builder.WriteString(fmt.Sprintf("\n\n<document name=%q>\n%s\n</document>", name, text))
	}
	return builder.String()
}
