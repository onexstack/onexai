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

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/agentauthz"
	"github.com/onexstack/onexai/internal/agent/pkg/agentbuilder"
	"github.com/onexstack/onexai/internal/agent/pkg/approval"
	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	"github.com/onexstack/onexai/internal/agent/pkg/modelguard"
	"github.com/onexstack/onexai/internal/agent/pkg/runnercache"
	agenttools "github.com/onexstack/onexai/internal/agent/pkg/tools"
	"github.com/onexstack/onexai/internal/agent/pkg/usage"
	"github.com/onexstack/onexai/internal/agent/pkg/usagereport"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/safego"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// einoEngine runs turns through an eino/adk ChatModelAgent + Runner. Skills are
// not yet registered; tools are, and their scope is resolved per run because
// the agents selected for a turn decide which MCP servers are in play. Model
// calls go through the OpenAI-compatible chatmodel.
type einoEngine struct {
	instruction string
	chatModel   *chatmodel.OpenAIChatModel
	// modelRouter is the stable model the graph holds; it resolves the run's
	// effective endpoint from ctx on every call.
	modelRouter einomodel.ToolCallingChatModel
	// resolveChatModel builds a model for an endpoint name. Nil leaves every
	// run on the deployment model.
	resolveChatModel func(ctx context.Context, name string) (einomodel.ToolCallingChatModel, error)
	tools            ToolsFunc
	// checkpoints carries a run that paused for approval across requests, and so
	// across pods: the call that answers it is a different request, served by
	// whichever replica the load balancer picks.
	checkpoints checkpoint.Store
	// reserved are the tool names approval must never gate. See EngineConfig.
	reserved    []string
	toolTimeout time.Duration
	// authz is the process-wide authorization Coordinator, and the two lookups
	// feed its per-run tool attribution / resource extraction. See EngineConfig.
	authz               *authz.Coordinator
	agentAuthz          *agentauthz.Resolver
	toolOrigins         func(mcps, agents []string) map[string]string
	mcpResourceMappings func(mcps []string) map[string]string
	// maxContextTokens bounds a run's prompt; maxToolOutputChars bounds a
	// single tool output. See contextguard.go and summarize.go.
	maxContextTokens int
	// modelGuard bounds and protects model endpoint calls. Nil gates nothing.
	modelGuard         *modelguard.Guard
	maxToolOutputChars int
	// builder assembles the deep-agent graph. It is never nil: a caller that
	// supplies none gets one with no optional subsystems. See
	// EngineConfig.Builder.
	builder *agentbuilder.Builder
	// resolveSkills and resolveKnowledgeBases turn the run's resource names
	// into the records the builder hands to the skill and knowledge-base
	// subsystems. Nil disables the corresponding capability.
	resolveSkills         func(context.Context, []string) []*model.SkillM
	resolveKnowledgeBases func(context.Context, []string) []*model.KnowledgeBaseM
	// runners caches a built graph per agent set, when caching is enabled. See
	// EngineConfig.RunnerTTL.
	runners *runnercache.Cache
	// resolveRun is how the cache's build function gets a spec from agent names.
	resolveRun func(context.Context, []string) RunSpec
	// pinTTL is how long a paused run holds its graph unrebuilt.
	pinTTL time.Duration
	// reporter sends a finished run's usage to the platform's ledger, or nil when
	// this deployment reports nowhere. See EngineConfig.UsageReporter.
	reporter usagereport.Reporter
	// delegation is the agent-as-tool tool, or nil when this deployment does not
	// delegate. See EngineConfig.DelegationTool.
	delegation tool.BaseTool
	// callTools builds the tools for Agent.spec.calls. See EngineConfig.CallTools.
	callTools func(context.Context, []string) []tool.BaseTool
	// memoryTools are the explicit memory tools. See EngineConfig.MemoryTools.
	memoryTools []tool.BaseTool
}

// newEinoEngine builds the eino engine. NewEngine has already rejected the case
// where there is no model to reason with, so this always returns an engine.
func newEinoEngine(cfg EngineConfig, instruction string) agentapi.Engine {
	cm := cfg.ChatModel
	if cm == nil {
		// No shared model was supplied, so build one here. A supplied model is
		// assumed to be fully configured already: applying the retry policy to it
		// again would stack two fallback chains on the same client.
		cm = chatmodel.NewOpenAIChatModel(cfg.BaseURL, cfg.APIKey, cfg.Model)
		cm = cm.WithRetry(cfg.FallbackModel, cfg.MaxRetries, cfg.Backoff)
	}
	router := cfg.ModelRouter
	if router == nil {
		router = NewRunModelRouter(cm, cfg.ResolveChatModel)
	}
	// Every run goes through the deep agent. A caller that supplies no builder
	// gets one with the deployment's defaults and no optional subsystems, which
	// is the shape the tests use; the server always supplies its own.
	//
	// The model is the router rather than the raw client, so a run that names a
	// model endpoint still resolves it per call even on this path.
	builder := cfg.Builder
	if builder == nil {
		builder = agentbuilder.NewBuilder(agentbuilder.Deps{ChatModel: router}, agentbuilder.Options{})
	}
	e := &einoEngine{
		instruction:           instruction,
		chatModel:             cm,
		modelRouter:           router,
		resolveChatModel:      cfg.ResolveChatModel,
		tools:                 cfg.Tools,
		checkpoints:           cfg.Checkpoints,
		reserved:              cfg.Reserved,
		toolTimeout:           cfg.ToolTimeout,
		maxContextTokens:      cfg.MaxContextTokens,
		modelGuard:            cfg.ModelGuard,
		maxToolOutputChars:    cfg.MaxToolOutputChars,
		authz:                 cfg.Authz,
		agentAuthz:            cfg.AgentAuthz,
		toolOrigins:           cfg.ToolOrigins,
		mcpResourceMappings:   cfg.MCPResourceMappings,
		builder:               builder,
		resolveSkills:         cfg.ResolveSkills,
		resolveKnowledgeBases: cfg.ResolveKnowledgeBases,
		resolveRun:            cfg.ResolveRun,
		pinTTL:                cfg.PinTTL,
		reporter:              cfg.UsageReporter,
		delegation:            cfg.DelegationTool,
		callTools:             cfg.CallTools,
		memoryTools:           cfg.MemoryTools,
	}
	if cfg.RunnerTTL > 0 && cfg.ResolveRun != nil {
		e.runners = runnercache.New(e.buildCached, cfg.RunnerTTL)
	}
	return e
}

// buildCached builds a runner from an agent selection alone, which is what the
// runner cache needs and what its correctness rests on: every input to the
// graph is derived from the selection, and everything that differs between two
// runs of the same selection travels on the run's context instead.
func (e *einoEngine) buildCached(ctx context.Context, agentNames []string) (*adk.Runner, []string, error) {
	spec := e.resolveRun(ctx, agentNames)
	groups := e.handlerGroups(ctx, nil, approval.Policy{}, false, "", nil)
	return e.buildRunner(ctx, spec, e.toolsForSpec(ctx, spec), groups)
}

// toolsFor resolves the tool set of a run. A nil resolver disables tools
// entirely rather than yielding the built-ins alone, which is what the caller
// that supplied it asked for.
func (e *einoEngine) toolsFor(mcps, agents []string) []tool.BaseTool {
	if e.tools == nil {
		return nil
	}
	return e.tools(mcps, agents)
}

// toolsForSpec is the run's tool set, including the delegation tool when the
// agent declared delegates.
//
// The delegation tool is filtered per run rather than offered to everyone: a
// tool the model can see but never use invites a call that fails, and an agent
// that names no delegate has no authority to spend a turn discovering that.
func (e *einoEngine) toolsForSpec(ctx context.Context, spec RunSpec) []tool.BaseTool {
	tools := e.toolsFor(spec.MCPs, spec.Agents)
	if spec.Unattended {
		// Nobody is watching this run, so a tool that stops to ask a human can
		// only park it until the question expires. Removing it is better than a
		// stub refusal: the model never sees a tool it cannot use.
		tools = withoutTool(ctx, tools, agenttools.AskUserQuestionTool)
	}
	var callTools []tool.BaseTool
	if e.callTools != nil && len(spec.Calls) > 0 {
		callTools = e.callTools(ctx, spec.Calls)
	}
	extra := len(callTools)
	if e.delegation != nil && len(spec.Delegates) > 0 {
		extra++
	}
	if spec.MemoryEnabled {
		extra += len(e.memoryTools)
	}
	if extra == 0 {
		return tools
	}
	out := append(make([]tool.BaseTool, 0, len(tools)+extra), tools...)
	out = append(out, callTools...)
	if e.delegation != nil && len(spec.Delegates) > 0 {
		out = append(out, e.delegation)
	}
	if spec.MemoryEnabled {
		out = append(out, e.memoryTools...)
	}
	return out
}

// withoutTool returns tools minus the first entry named name.
//
// Info is not free for MCP tools (it rebuilds the schema from the wire form),
// so this is called only for unattended runs, and it stops at the first match:
// the built-ins are resolved first, which is where ask_user_question lives.
func withoutTool(ctx context.Context, tools []tool.BaseTool, name string) []tool.BaseTool {
	for i, t := range tools {
		if t == nil {
			continue
		}
		info, err := t.Info(ctx)
		if err != nil || info == nil || info.Name != name {
			continue
		}
		out := make([]tool.BaseTool, 0, len(tools)-1)
		out = append(out, tools[:i]...)
		return append(out, tools[i+1:]...)
	}
	return tools
}

func (e *einoEngine) Name() string { return "eino" }

// reportUsage hands a finished run's usage to the platform's ledger, when this
// deployment reports at all — and when this run is one of ours to report.
//
// It is called next to reportRunUsage because the two answer the same question
// for different readers: the metric is for this process's dashboards, the report
// is for the platform's ledger. Only the second survives a pod restart, which is
// why a run that OneX never saw (a Lark conversation) needs it and a run OneX
// started does not.
//
// # Why the third condition is not redundant with the other two
//
// The platform meters the runs it starts itself: it writes the ledger rows in
// the same transaction as the message or the comment the run produced. So for
// those runs this report is a **second** entry for a run already on the books —
// the same rows again, agreeing on every column but the id, which doubles the
// charge against every ceiling that sums the ledger. `opts.ReportUsage` is the
// caller saying which kind of run this is; see RunOptions.ReportUsage for why
// it is resolved to a plain bool before it gets here, and the ask contract for
// why an asker that says nothing is asking to be reported (the Lark path).
//
// A run that made no model call is not reported: it spent nothing, and a row of
// zeros would be a ledger entry for a conversation that answered nothing. It is
// the same reason the caller checks Calls rather than reporting unconditionally.
func (e *einoEngine) reportUsage(ctx context.Context, opts agentapi.RunOptions, t usage.Totals) {
	if e.reporter == nil || t.Calls == 0 || !opts.ReportUsage {
		return
	}
	e.reporter.Report(ctx, usagereport.Record{
		UserID:         opts.SenderID,
		SessionKey:     opts.SessionKey,
		RunID:          opts.RunID,
		KnowledgeBases: len(opts.KnowledgeBases),
		Usage:          t,
	})
}

// runStream is the per-run scaffolding a fresh run and a resumed half share:
// the output channel, its emitter, the usage tracker, and the context carrying
// the run's sinks and id.
type runStream struct {
	out     chan agentapi.Event
	runCtx  context.Context
	tracker *usage.Tracker
	em      *runEmitter
	runID   string
}

// openStream builds the channel, emitter, sinks and run id for one run.
//
// generateID is true for a fresh run, whose missing id is filled in here, and
// false for a resumed half, which must keep the id the paused half wrote so
// per-run files remain reachable.
func (e *einoEngine) openStream(ctx context.Context, runID string, generateID bool) *runStream {
	out := make(chan agentapi.Event, 32)
	runCtx, tracker := usageContext(ctx)
	em := newRunEmitter(out).stopWhen(runCtx.Done())
	// The emitter is the run's event channel, so it is also what a tool draws
	// into: attaching it here is what lets generate_image report a picture out
	// of band rather than describing one in words.
	runCtx = agentapi.WithImageSink(runCtx, em)
	// The same channel, for the same reason: a retrieval reports the documents
	// it read out of band, so whoever renders the answer can show a reader where
	// it came from instead of asking the model to name its sources — a thing the
	// model has no reliable way to do and no reason to be trusted with.
	runCtx = agentapi.WithSourceSink(runCtx, em)
	if runID == "" && generateID {
		runID = uuid.NewString()
	}
	// The run's ID is on the context before the graph is built: components that
	// write per-run files key them by it, and a resumed half has to find what
	// the first half wrote.
	runCtx = agentapi.WithRunID(runCtx, runID)
	return &runStream{out: out, runCtx: runCtx, tracker: tracker, em: em, runID: runID}
}

// stopOption installs the run's cancel handle, so a stop interrupts at a
// boundary and leaves a checkpoint behind instead of tearing the call down
// where it is.
func (e *einoEngine) stopOption(onStopReady func(func())) adk.AgentRunOption {
	opt, cancel := adk.WithCancel()
	if onStopReady != nil {
		onStopReady(func() { e.gracefulStop(cancel) })
	}
	return opt
}

// finishHalf reports one half of a run's usage and, when the half did not
// pause, publishes its terminal event.
//
// A paused half has already spent its tokens. Reporting now matters for the
// same reason a finished run reports: the ledger is what every ceiling on the
// platform sums, and a run that paused on a group-chat surface (the only one
// that meters itself here) would otherwise contribute nothing for the work it
// did. The resumed half reports its own totals, so the two rows are disjoint
// halves of one run.
//
// unpin releases the graph a completed resumed half no longer needs; the half
// that paused keeps it, because it is the only graph that can resolve the
// interrupt the human will answer.
func (e *einoEngine) finishHalf(s *runStream, opts agentapi.RunOptions, answer string, suspended, unpin bool) {
	if suspended {
		e.runners.Pin(opts.Agents, e.pinTTL)
		reportRunUsage(s.tracker.Totals())
		e.reportUsage(s.runCtx, opts, s.tracker.Totals())
		return
	}
	if unpin {
		e.runners.Unpin(opts.Agents)
	}
	reportRunUsage(s.tracker.Totals())
	e.reportUsage(s.runCtx, opts, s.tracker.Totals())
	s.em.finish(agentapi.Event{Type: agentapi.EventDone, Final: answer, Usage: toRunUsage(s.tracker.Totals())})
}

// Run builds a fresh adk agent+runner per turn and maps adk events onto the
// backend-neutral event stream.
func (e *einoEngine) Run(ctx context.Context, messages []agentapi.Message, opts agentapi.RunOptions) (<-chan agentapi.Event, error) {
	s := e.openStream(ctx, opts.RunID, true)

	runCtx, runner, _, err := e.prepare(s.runCtx, opts, false, s.em)
	if err != nil {
		return nil, err
	}
	s.runCtx = runCtx
	cancelOpt := e.stopOption(opts.OnStopReady)

	safego.Go(s.runCtx, "agent run", func() {
		defer close(s.out)
		iter := runner.Run(s.runCtx, toSchemaMessages(e.instruction, messages),
			adk.WithCheckPointID(s.runID), cancelOpt)
		answer, suspended := e.consume(s.runCtx, iter, s.em, s.tracker, opts)
		e.finishHalf(s, opts, answer, suspended, false)
	})

	return s.out, nil
}

// Resume continues a run that paused for approval, answering every interrupt
// point it recorded with one decision.
//
// The run is rebuilt from the options the pause recorded rather than from
// anything the caller sends beyond the decision: the checkpoint names its
// interrupt points by address, and an agent assembled with a different tool set
// or instruction would have nothing at those addresses to resume. That is also
// why this is a second run rather than a channel back into the first — the pod
// that paused it may be gone.
func (e *einoEngine) Resume(ctx context.Context, opts agentapi.ResumeOptions) (<-chan agentapi.Event, error) {
	s := e.openStream(ctx, opts.Run.RunID, false)

	runCtx, runner, toolNames, err := e.prepare(s.runCtx, opts.Run, opts.AllowAll, s.em)
	if err != nil {
		return nil, err
	}
	s.runCtx = runCtx
	if err := checkPendingTools(opts, toolNames); err != nil {
		return nil, err
	}
	targets := resumeTargets(opts)
	// A resumed half is stoppable on the same terms as the half that paused.
	cancelOpt := e.stopOption(opts.Run.OnStopReady)

	safego.Go(s.runCtx, "agent resume", func() {
		defer close(s.out)
		iter, err := runner.ResumeWithParams(s.runCtx, opts.Run.RunID, &adk.ResumeParams{Targets: targets}, cancelOpt)
		if err != nil {
			s.em.finish(agentapi.Event{Type: agentapi.EventFailed,
				Err: fmt.Errorf("resume run %s: %w", opts.Run.RunID, err)})
			return
		}
		answer, suspended := e.consume(s.runCtx, iter, s.em, s.tracker, opts.Run)
		e.finishHalf(s, opts.Run, answer, suspended, true)
	})

	return s.out, nil
}

// checkPendingTools refuses a resume whose approved tool is no longer part of
// the graph.
//
// eino addresses a paused call by the tool it paused on. If a deploy, a policy
// change or a different agent selection removed that tool, the tool node treats
// the call as unknown — onexai answers unknown calls with a text result so a
// hallucinated name does not fail a whole turn — and the model then finishes
// the conversation as if the work had been done. The run would report Done
// having executed nothing, which is the one outcome an approval gate exists to
// prevent. Checking here, before the resume starts, turns that silence into an
// error the approver can act on.
func checkPendingTools(opts agentapi.ResumeOptions, toolNames []string) error {
	if len(opts.Pending) == 0 {
		return nil
	}
	for _, pending := range opts.Pending {
		if pending.ToolName == "" || slices.Contains(toolNames, pending.ToolName) {
			continue
		}
		return fmt.Errorf("resume run %s: the approved tool %q is not in this agent's tool set (%s)",
			opts.Run.RunID, pending.ToolName, strings.Join(toolNames, ", "))
	}
	return nil
}

// prepare assembles the per-run pieces Run and Resume share: the agent
// configuration, its handler chain, and the runner the run is driven through.
//
// Rebuilding the chain identically on resume is what makes the checkpoint's
// addresses resolve, so every input here comes from the run's own recorded
// options and none from the instance.
// It returns the context it derived, not only the runner: the per-run values it
// attaches there (model endpoint, generation parameters) are what the caller
// must drive the run with. Returning the runner alone would discard them, and
// the router would then see no model name and quietly answer with the platform
// default — a fallback indistinguishable from "the user has not configured a
// model".
func (e *einoEngine) prepare(
	runCtx context.Context, opts agentapi.RunOptions, allowAll bool, em *runEmitter,
) (context.Context, *adk.Runner, []string, error) {
	// Resolve and attach the run's endpoint before anything reads the graph.
	// The router reads this name on every model call, so a cached runner built
	// for another run uses this run's model instead of the one it was built
	// with. Resolving here also fails a broken endpoint before the stream
	// starts, which is where the caller can still get a clean status.
	if opts.Model != "" && e.resolveChatModel != nil {
		if _, err := e.resolveChatModel(runCtx, opts.Model); err != nil {
			return runCtx, nil, nil, err
		}
	}
	runCtx = agentapi.WithModelName(runCtx, opts.Model)
	if opts.MaxOutputTokens != nil {
		runCtx = chatmodel.WithMaxOutputTokens(runCtx, *opts.MaxOutputTokens)
	}
	runCtx = chatmodel.WithGeneration(runCtx, chatmodel.Generation{
		Temperature:     opts.Temperature,
		TopP:            opts.TopP,
		ProviderOptions: opts.ProviderOptions,
	})
	// The delegation identity travels on the context, not in the tool's
	// arguments: the graph is shared between runs, and the model must not be
	// able to name the identity its delegated call runs as.
	if opts.Delegation != nil {
		runCtx = agentapi.WithDelegation(runCtx, *opts.Delegation)
	}
	// Same reasoning for memory: the model chooses what to remember, not whose
	// memory it is.
	if opts.MemoryEnabled && len(opts.Agents) > 0 && opts.SenderID != "" {
		runCtx = agentapi.WithMemoryContext(runCtx, agentapi.MemoryContext{
			Agent:   opts.Agents[0],
			Subject: opts.SenderID,
		})
	}

	policy := approval.New(opts.Approval, e.reserved)
	spec := specFromOptions(opts, e.instruction)
	// The per-request spec carries no agent resources. Merge the resolved call
	// set so the uncached path offers the same A2A dispatcher as the cached
	// path; without this, a run whose runner cache is cold never sees calls.
	if e.resolveRun != nil && len(opts.Agents) > 0 {
		resolved := e.resolveRun(runCtx, opts.Agents)
		spec.Calls = resolved.Calls
		if len(resolved.Delegates) > 0 {
			spec.Delegates = append(spec.Delegates, resolved.Delegates...)
		}
	}

	// A run that can pause for a decision cannot run without a checkpoint store.
	// The runner only checkpoints when it has a store, so without one an
	// interrupt parks the run in a state nothing can resume: the human is asked
	// for an answer, and their answer comes back as no-longer-pending. Two things
	// can interrupt — a tool the policy gates, and a tool whose own contract is
	// to consult a human, which is always in the built-in set — so this covers
	// having tools at all, not only having a policy. Failing here says so on the
	// first turn, which is where an operator can still act on it.
	//
	// The tool set is resolved only to answer this question, and only when the
	// answer could be no: resolving it on the normal path would be work the
	// runner cache is about to make redundant.
	if e.checkpoints == nil {
		count := len(e.toolsForSpec(runCtx, spec))
		if count > 0 || !policy.Empty() {
			return runCtx, nil, nil, fmt.Errorf("this run can pause for a decision (%d tools, approval policy: %s) but no "+
				"checkpoint store is configured, so a pause could not be resumed", count, policy)
		}
	}

	// One handler covers every tool of this run's tool node — built-ins and MCP
	// tools alike — reporting each call's start, end and measured duration; the
	// approval handler then gates the ones the policy names. Tracing sits outside
	// approval so the interrupt it raises is reported through the same path as any
	// other failure, leaving that step pending for the resume to complete.
	// Truncation is registered ahead of summarization because adk applies handlers
	// in order, and the summarizer should see the already-trimmed context rather
	// than spending its call on an oversized one.
	// The authorization gate is built here, not held on the engine: the tool ->
	// MCP attribution and the resource mappings it needs are properties of this
	// run's selected servers. A nil gate (authz disabled, or a nil lookup) makes
	// the approval handler behave exactly as it did before authz existed.
	gate := newAuthzGate(e.authz, e.agentAuthz, opts.Agents, e.toolOriginsFor(opts.MCPs, opts.Agents),
		authzResourceMappings(e.mcpResourceMappings, opts.MCPs), e.reserved)

	// One builder for the chain, shared with the cached path. Two copies of this
	// shape were a standing invitation for the two paths to differ in the order
	// or the set of handlers, which is a difference nobody would look for.
	groups := e.handlerGroups(runCtx, em, policy, allowAll, opts.ApprovalFallback, gate)

	// The run's own state goes on its context, where the handlers above read it.
	// It is attached here rather than captured in their fields so the handlers
	// are a function of the agent set alone and can be built once and reused.
	runCtx = withRunState(runCtx, &runState{
		emitter:  em,
		policy:   policy,
		allowAll: allowAll,
		fallback: opts.ApprovalFallback,
		gate:     gate,
	})

	// With caching on, the graph is fetched rather than built. The handlers it
	// holds read their state from runCtx, attached just above, so a graph built
	// for an earlier run behaves correctly for this one.
	//
	// A narrowed run skips the cache: its build function receives the agent
	// names alone, so a cached graph always carries the full capability set.
	// The fingerprint would force a rebuild and the rebuild would read the full
	// set again — silently undoing the narrowing. A debug run can afford the
	// build.
	if e.runners.Enabled() && !opts.BypassRunnerCache {
		runner, tools, _, err := e.runners.GetWithTools(runCtx, opts.Agents, runFingerprint(opts))
		if err != nil {
			return runCtx, nil, nil, err
		}
		return runCtx, runner, tools, nil
	}
	// Tools are resolved per run: opts.MCPs carries the MCP servers the selected
	// agents declared, and an empty selection yields the built-in tools alone.
	runner, tools, err := e.buildRunner(runCtx, spec, e.toolsForSpec(runCtx, spec), groups)
	return runCtx, runner, tools, err
}

// runFingerprint digests everything the built graph depends on that is not
// already in the cache key. The key must stay the agent set — a paused run
// addresses its checkpoint by position in the graph and has to find the same
// entry — so capability changes are detected here instead.
func runFingerprint(opts agentapi.RunOptions) string {
	h := sha256.New()
	writeList := func(label string, values []string) {
		sorted := append([]string(nil), values...)
		slices.Sort(sorted)
		h.Write([]byte(label))
		h.Write([]byte{0})
		for _, value := range sorted {
			h.Write([]byte(value))
			h.Write([]byte{0})
		}
	}

	writeList("agents", opts.Agents)
	writeList("skills", opts.Skills)
	writeList("mcps", opts.MCPs)
	writeList("knowledgeBases", opts.KnowledgeBases)
	writeList("approval", opts.Approval)
	if opts.Delegation != nil {
		writeList("delegates", opts.Delegation.Agents)
	}
	h.Write([]byte(opts.Instruction))
	if opts.InstructionSet {
		h.Write([]byte{0})
		h.Write([]byte("instruction-set"))
	}
	if opts.Unattended {
		h.Write([]byte{0})
		h.Write([]byte("unattended"))
	}

	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// specFromOptions is the spec a run's own options describe. It exists so the
// uncached path and the cached one build from the same shape.
func specFromOptions(opts agentapi.RunOptions, fallbackInstruction string) RunSpec {
	instruction := fallbackInstruction
	if opts.InstructionSet || opts.Instruction != "" {
		instruction = opts.Instruction
	}
	spec := RunSpec{
		Agents:         opts.Agents,
		Instruction:    instruction,
		InstructionSet: opts.InstructionSet || opts.Instruction != "",
		MCPs:           opts.MCPs,
		Skills:         opts.Skills,
		KnowledgeBases: opts.KnowledgeBases,
		Approval:       opts.Approval,
	}
	if opts.Delegation != nil {
		spec.Delegates = opts.Delegation.Agents
	}
	spec.MemoryEnabled = opts.MemoryEnabled
	spec.Unattended = opts.Unattended
	return spec
}

// buildRunner assembles a runner from a spec. Run options and the cache's build
// function both end up here, which is what keeps a cached runner and a fresh
// one identical by construction.
func (e *einoEngine) buildRunner(
	ctx context.Context, spec RunSpec, runTools []tool.BaseTool,
	groups agentbuilder.HandlerGroups,
) (*adk.Runner, []string, error) {
	instruction := e.instruction
	if spec.InstructionSet || spec.Instruction != "" {
		instruction = spec.Instruction
	}
	// Decided here, once, from the spec and the engine's own capabilities —
	// not passed in by each caller. The cached and uncached paths used to
	// derive it differently (one counted an enabled authorizer, the other the
	// request's resolved policy), which is a graph built with serial tool calls
	// on one path and parallel on the other. Erring towards true only costs
	// concurrency: a run that cannot pause is not harmed by running its tools
	// one at a time, while a run that can pause is harmed by not doing so.
	approvalActive := len(spec.Approval) > 0 || e.authz != nil
	return e.buildDeepRunner(ctx, spec, instruction, runTools, groups, approvalActive)
}

// modelHandlers is the chain every runner wraps the model call with.
//
// It lives here rather than in the builder because it reads this engine's
// per-run limits and the run's own state; the builder only decides where in the
// chain it goes.
func (e *einoEngine) modelHandlers() []adk.ChatModelAgentMiddleware {
	handlers := make([]adk.ChatModelAgentMiddleware, 0, 3)
	// First, so it wraps the real model: an error raised by one of the handlers
	// below is this service's own (an image reference that cannot be resolved),
	// and counting it as an endpoint failure would open the breaker for a
	// provider that is healthy.
	if e.modelGuard.Enabled() {
		handlers = append(handlers, e.modelGuard.Middleware())
	}
	return append(handlers,
		newContextGuardHandler(e.maxToolOutputChars),
		// Resolves image references inside the model call rather than in the
		// state, so a paused run's checkpoint holds references and not base64.
		newImageResolveMiddleware(),
	)
}

// handlerGroups builds the handler chain. The per-run values become the
// handlers' own defaults, used only when the context carries no run state — so
// the cached path may pass zero values and still get working handlers.
func (e *einoEngine) handlerGroups(
	ctx context.Context, em *runEmitter, policy approval.Policy, allowAll bool,
	fallback string, gate *authzGate,
) agentbuilder.HandlerGroups {
	return agentbuilder.HandlerGroups{
		ToolHandlers: []adk.ChatModelAgentMiddleware{
			newToolTraceHandler(em, e.toolTimeout),
			newApprovalHandler(policy, allowAll, fallback).withAuthz(gate),
		},
		ModelHandlers:  e.modelHandlers(),
		SummaryHandler: newSummarizationHandler(ctx, e.chatModel, e.maxContextTokens),
	}
}

// buildDeepRunner assembles the run through pkg/agentbuilder: the deep agent
// graph, with the run's skills and knowledge bases attached.
//
// The builder owns the handler order — it inserts the tool-error conversion
// ahead of the caller's tool handlers and the skill and prompt middleware
// ahead of the summarizer — so the groups are handed over as groups rather
// than as one pre-ordered list.
func (e *einoEngine) buildDeepRunner(
	runCtx context.Context, spec RunSpec, instruction string,
	runTools []tool.BaseTool, groups agentbuilder.HandlerGroups, approvalActive bool,
) (*adk.Runner, []string, error) {
	result, err := e.builder.Build(runCtx, agentbuilder.Request{
		AgentNames:     spec.Agents,
		Instruction:    instruction,
		Skills:         e.skillsFor(runCtx, spec.Skills),
		KnowledgeBases: e.knowledgeBasesFor(runCtx, spec.KnowledgeBases),
		BaseTools:      runTools,
		MCPTools:       e.mcpToolNames(spec.MCPs, spec.Agents),
		Handlers:       groups,
		CheckPoints:    e.checkpoints,
		ApprovalActive: approvalActive,
	})
	if err != nil {
		return nil, nil, err
	}
	slog.DebugContext(runCtx, "deep agent built",
		"agent_key", result.AgentKey,
		"tools", result.ToolNames,
		"skills_dir", result.IsolatedSkillsDir)
	return result.Runner, result.ToolNames, nil
}

// mcpToolNames lists the model-facing names of the tools this run's MCP servers
// published.
//
// Tool search needs the distinction because only MCP tools may be hidden: the
// built-ins (one of which exists to consult a human) and the tools the builder
// adds stay visible. The origin map is the same one authorization uses to
// attribute a call to a server, so the two cannot disagree about what is MCP.
func (e *einoEngine) mcpToolNames(mcps, agents []string) []string {
	origins := e.toolOriginsFor(mcps, agents)
	if len(origins) == 0 {
		return nil
	}
	names := make([]string, 0, len(origins))
	for name := range origins {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// skillsFor resolves the run's skill names into records, or nil when the skill
// subsystem is not wired.
func (e *einoEngine) skillsFor(ctx context.Context, names []string) []*model.SkillM {
	if e.resolveSkills == nil || len(names) == 0 {
		return nil
	}
	resolved := e.resolveSkills(ctx, names)
	// Only "hosted" skills are loaded into the prompt. A "guide" skill has no
	// body by design — its contract is what a local client receives — and a
	// "tool" skill runs an entrypoint rather than being read. Loading either
	// here would put the body back into the model context, which is the thing
	// the mode exists to prevent. An empty mode is a row written before the
	// field existed and keeps the old behaviour.
	out := make([]*model.SkillM, 0, len(resolved))
	for _, s := range resolved {
		if s != nil && (s.Mode == "" || s.Mode == "hosted") {
			out = append(out, s)
		}
	}
	return out
}

// knowledgeBasesFor resolves the run's knowledge-base names into records, or
// nil when the retrieval subsystem is not wired.
func (e *einoEngine) knowledgeBasesFor(ctx context.Context, names []string) []*model.KnowledgeBaseM {
	if e.resolveKnowledgeBases == nil || len(names) == 0 {
		return nil
	}
	return e.resolveKnowledgeBases(ctx, names)
}

// stopGrace is how long a stop waits for the run to reach a boundary before
// eino escalates to an immediate cancel. It bounds the user-visible delay of a
// stop during a long tool batch; the escalated cancel still saves a checkpoint
// first, so escalating costs the pause, not the recoverability.
const stopGrace = 2 * time.Second

// gracefulStop asks a run to stop at its next safe point instead of tearing it
// down mid-call.
//
// Cancelling the run's context — what a stop used to do — aborts the model
// stream or the tool call wherever it happens to be, and the checkpoint save
// that makes a cancel recoverable then fails because that context is already
// done. eino's cancel signal is the other way round: it interrupts between
// calls, saves what it interrupted, and reports the stop through CancelError.
// Both stop points are requested, because a run streaming prose has no tool call
// to wait for — it stops when the current model call ends, and the grace period
// bounds that wait.
func (e *einoEngine) gracefulStop(cancel adk.AgentCancelFunc) {
	handle, ok := cancel(
		adk.WithAgentCancelMode(adk.CancelAfterChatModel|adk.CancelAfterToolCalls),
		adk.WithAgentCancelTimeout(stopGrace),
	)
	if !ok || handle == nil {
		// The run already ended; there is nothing left to stop.
		return
	}
	// The caller is an HTTP handler that has just answered "stopping". It must
	// not block on a run that is on its way out, so the outcome is awaited here
	// and deliberately not acted on: consume reports the stop either way.
	go func() { _ = handle.Wait() }()
}

// consume drives one adk iterator to its end, mapping its events onto the
// neutral stream. Run and Resume share it because a resumed run emits exactly
// what a fresh one does: the same answers, tool steps, usage and, if it pauses
// again, the same interrupt.
//
// It reports the accumulated answer and whether the run suspended. A suspend is
// not an ending — the checkpoint is kept and a later resume continues it — but it
// does end this stream, so the terminal event it emits is EventSuspended rather
// than Done.
func (e *einoEngine) consume(
	runCtx context.Context, iter *adk.AsyncIterator[*adk.AgentEvent],
	em *runEmitter, tracker *usage.Tracker, opts agentapi.RunOptions,
) (answer string, suspended bool) {
	// Reasoning is surfaced only when the run asked for it. The caller's only
	// reason to send false is that the card it built has no reasoning panel,
	// and a delta pushed at an element that does not exist fails the cardkit
	// call — which degrades the whole preview, answer text included. The
	// adapter carries reasoning regardless; this is the layer that decides
	// whether it is rendered.
	reasoning := opts.Reasoning

	for {
		event, ok := iter.Next()
		if !ok {
			return answer, false
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			// A stop requested through the engine's own cancel signal arrives as
			// CancelError rather than as an error on the context: the context is
			// still alive at that point, which is exactly what lets the runner
			// save the checkpoint the cancel interrupted. Both paths end the
			// stream as a stop; neither is a failure the user should see.
			var cancelErr *adk.CancelError
			if errors.As(event.Err, &cancelErr) || runCtx.Err() != nil {
				em.finish(agentapi.Event{Type: agentapi.EventStopped})
			} else {
				em.finish(agentapi.Event{Type: agentapi.EventFailed, Err: event.Err})
			}
			return answer, false
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			// The runner has already stored the checkpoint: it saves before it
			// emits the interrupt, which is why a pause survives the process that
			// took it. What is left is to describe the pause and end the stream.
			it, err := interruptFromAdk(event.Action.Interrupted)
			if err != nil {
				em.finish(agentapi.Event{Type: agentapi.EventFailed, Err: err})
				return answer, false
			}
			em.send(agentapi.Event{Type: agentapi.EventInterrupted, Interrupt: it})
			em.finish(agentapi.Event{Type: agentapi.EventSuspended})
			return answer, true
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		mv := event.Output.MessageOutput
		if mv.Role == schema.Tool {
			// The tool-trace handler already reported this invocation, with its
			// duration, when it actually ran. A step emitted here would be a
			// duplicate row with no metadata; the message still must not be
			// mistaken for the answer.
			em.usageIfChanged(tracker)
			continue
		}
		emitMessage(em, mv, reasoning, &answer)

		// The model adapter records a call's usage before it hands the message
		// on, so a new round's usage is visible by the time its output lands
		// here — which is exactly when the tool batch that round requested can
		// be attributed its share of the context.
		em.usageIfChanged(tracker)
	}
}

// emitMessage streams one assistant message onto the event channel, appending
// its text to answer. Reasoning is its own event and never joins the answer:
// Final is the answer the card closes on, and a thinking chunk folded into it
// would be rendered twice.
func emitMessage(em *runEmitter, mv *adk.MessageVariant, reasoning bool, answer *string) {
	if mv.IsStreaming && mv.MessageStream != nil {
		for {
			chunk, err := mv.MessageStream.Recv()
			if err != nil {
				// io.EOF is the normal end. Any other error is reported by the
				// runner as the node's own failure (event.Err), so there is
				// nothing to do here but stop reading: emitting a terminal event
				// would preempt the one carrying the node's context.
				return
			}
			if chunk == nil {
				continue
			}
			if reasoning && chunk.ReasoningContent != "" {
				em.send(agentapi.Event{Type: agentapi.EventReasoning, Delta: chunk.ReasoningContent})
			}
			if chunk.Content != "" {
				*answer += chunk.Content
				em.send(agentapi.Event{Type: agentapi.EventAnswer, Delta: chunk.Content})
			}
		}
	}

	if mv.Message == nil {
		return
	}
	if reasoning && mv.Message.ReasoningContent != "" {
		em.send(agentapi.Event{Type: agentapi.EventReasoning, Delta: mv.Message.ReasoningContent})
	}
	if mv.Message.Content != "" {
		*answer += mv.Message.Content
		em.send(agentapi.Event{Type: agentapi.EventAnswer, Delta: mv.Message.Content})
	}
}

// resumeTargets builds eino's per-address resume data from the recorded
// suspension.
//
// Every root cause is answered in one call on purpose. The tool node re-runs as
// a whole on resume, so a group answered in part would replay the tools that had
// already been approved and re-interrupt on the rest — the same calls, twice.
func resumeTargets(opts agentapi.ResumeOptions) map[string]any {
	targets := make(map[string]any, len(opts.Pending))
	for _, p := range opts.Pending {
		if p.Address == "" {
			continue
		}
		if p.Kind == agentapi.InterruptKindQuestion {
			// A question's answer is the resume data itself: the tool returns it
			// to the model. An unanswered question is left out rather than resumed
			// with an empty string, so the tool asks again instead of reporting
			// that a human answered nothing.
			if ans, ok := opts.Answers[p.Address]; ok {
				targets[p.Address] = ans
			}
			continue
		}
		// A permission address always gets an answer, allow or deny: eino hands
		// an untargeted leaf isResumeTarget=false, and the middleware would then
		// re-interrupt it — leaving the run paused with a human convinced they had
		// just answered it.
		targets[p.Address] = &agentapi.ApprovalResult{
			Approved: opts.Decision != agentapi.DecisionDeny,
			AllowAll: opts.AllowAll,
			Reason:   opts.Reason,
		}
	}
	return targets
}

// usageContext installs the run's token tracker. Usage is collected through the
// context because the model adapter records each call's usage as it makes it, so
// the tracker must be in place before the agent is built for every call to see
// it.
func usageContext(ctx context.Context) (context.Context, *usage.Tracker) {
	tracker := usage.New()
	return usage.WithTracker(ctx, tracker), tracker
}

// toSchemaMessages converts neutral history to adk/schema messages. The system
// instruction is supplied via the agent config, so system-role history entries
// are skipped here to avoid duplication.
func toSchemaMessages(_ string, msgs []agentapi.Message) []*schema.Message {
	out := make([]*schema.Message, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case agentapi.RoleAssistant:
			out = append(out, schema.AssistantMessage(m.Content, nil))
		case agentapi.RoleSystem:
			// Skipped: provided as the agent Instruction.
		default:
			out = append(out, toUserSchemaMessage(m))
		}
	}
	return out
}

// toUserSchemaMessage builds a user schema.Message, attaching multimodal image
// inputs via UserInputMultiContent when the message carries images.
//
// The URL written here is the reference's compact form — "asset:<id>",
// "media:<key>", or the URL it holds — and not a resolved data URL. The
// resolution happens in imageResolveMiddleware, inside the model call, because
// anything written to a message here becomes part of the agent's state and is
// therefore checkpointed on a pause. A reference is a few bytes; the data URL it
// stands for is the base64 payload this whole design keeps out of that state.
func toUserSchemaMessage(m agentapi.Message) *schema.Message {
	message := schema.UserMessage(m.Content)
	if len(m.Images) == 0 {
		return message
	}

	parts := make([]schema.MessageInputPart, 0, len(m.Images)+1)
	if m.Content != "" {
		parts = append(parts, schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: m.Content})
	}
	for _, img := range m.Images {
		url := img.String()
		if url == "" {
			// A reference that names neither an asset, a key nor a URL is not one
			// anything can resolve; there is nothing to put in the part.
			slog.Warn("[eino] skipping an image reference that names nothing",
				"asset_id", img.AssetID, "object_key", img.ObjectKey)
			continue
		}
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{URL: &url},
			},
		})
	}
	message.UserInputMultiContent = parts
	return message
}

// errNoInterruptPoint reports an interrupt with no root cause, which the runner
// cannot have produced for a tool call: the middleware raises one per gated
// call, and the runner marks each as a root cause. A pause with no address is a
// pause nothing can target, so the run fails rather than suspending into a state
// that no approval can continue.
var errNoInterruptPoint = errors.New("agent run interrupted without a root-cause interrupt point")

// interruptFromAdk converts an adk interrupt into the backend-neutral
// description of a suspension: one request per interrupt point the run is
// waiting on, because one assistant message can call several tools at once and
// the policy can gate more than one of them.
func interruptFromAdk(info *adk.InterruptInfo) (*agentapi.Interrupt, error) {
	out := &agentapi.Interrupt{RequestID: uuid.NewString()}
	if info == nil {
		return nil, errNoInterruptPoint
	}
	for _, ictx := range info.InterruptContexts {
		if ictx == nil || !ictx.IsRootCause {
			continue
		}
		out.Requests = append(out.Requests, requestFromAdk(ictx))
	}
	if len(out.Requests) == 0 {
		return nil, errNoInterruptPoint
	}
	return out, nil
}

// requestFromAdk describes one interrupt point, classifying it by the payload
// the interrupt carries: a question raised by ask_user_question is answered with
// text, everything else is a permission prompt answered with a decision.
func requestFromAdk(ictx *adk.InterruptCtx) agentapi.InterruptRequest {
	req := agentapi.InterruptRequest{
		Address:  ictx.ID,
		Kind:     agentapi.InterruptKindPermission,
		ToolName: toolNameOfAddress(ictx.Address),
	}
	switch info := ictx.Info.(type) {
	case *agentapi.AskQuestionInfo:
		req.Kind = agentapi.InterruptKindQuestion
		req.Question, req.Options, req.MultiSelect = info.Question, info.Options, info.MultiSelect
	case *agentapi.PermissionInfo:
		req.Question = info.String()
		// The call's own arguments, beside the prompt that described them. The
		// prompt already mentions them in prose; this is the same thing in the
		// form a card can lay out, and it is what the person approving reads.
		req.Arguments = info.Arguments
	case nil:
		// Fall through to the generic prompt below.
	default:
		req.Question = fmt.Sprintf("%v", info)
	}
	if req.Question == "" {
		req.Question = fmt.Sprintf("工具 %s 请求执行，是否允许？", req.ToolName)
	}
	return req
}

// toolNameOfAddress reads the tool segment out of an interrupt address, which is
// what the caller shows as the tool awaiting a decision. An address with no tool
// segment belongs to the agent itself rather than to one of its tools.
func toolNameOfAddress(addr adk.Address) string {
	name := ""
	for _, seg := range addr {
		if seg.Type == adk.AddressSegmentTool && seg.ID != "" {
			name = seg.ID
		}
	}
	if name == "" {
		return "agent"
	}
	return name
}
