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
	"errors"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/pkg/declclient/declstore"
)

// RunSpec is everything about a run that depends on which agents it selected,
// and nothing that depends on the request that selected them.
//
// The distinction is what makes a runner reusable: two runs of the same agent
// set resolve to the same spec, so the graph built from it serves both. The
// per-request half — the event sink, the run id, whether reasoning is shown —
// travels separately.
type RunSpec struct {
	// Agents are the names this spec was resolved for. They name the runner and
	// isolate its skills on disk, so they are part of the spec rather than an
	// argument beside it.
	Agents []string
	// Instruction is the resolved system prompt.
	Instruction string
	// InstructionSet makes Instruction authoritative even when it is empty. A
	// composed run sets it so "no prompts selected" does not fall back to the
	// deployment's default persona.
	InstructionSet bool
	// MCPs, Skills and KnowledgeBases are the resource names the agents declared.
	MCPs           []string
	Skills         []string
	KnowledgeBases []string
	// PromptRefs are the Prompt entries the instruction was rendered from, in
	// order. The instruction alone no longer says which namespaces they came
	// from, and the namespace gate has to answer that question for every one.
	PromptRefs []string
	// Models are the model endpoints the selection declares, as canonical
	// references. They are kept here for the namespace gate; which endpoint the
	// run actually uses is resolved separately.
	Models []string
	// Approval is the tool-name patterns the agents' resources declared.
	Approval []string
	// OutputSchema is the canonical structured-output contract. Empty means a
	// plain-text answer.
	OutputSchema string
	// OutputSchemaStrict asks the provider to constrain decoding.
	OutputSchemaStrict bool
	// MemoryName names the Memory space this run reads and writes.
	MemoryName string
	// MemoryEnabled turns long-term memory on for the run.
	MemoryEnabled bool
	// MemoryEmbeddingModel names the Model used for embeddings.
	MemoryEmbeddingModel string
	// MemoryAutoSaveEvery saves a turn's summary unprompted every N user turns.
	// Zero disables it; explicit save_memory calls are unaffected.
	MemoryAutoSaveEvery int32
	// MemoryTTLDays bounds an auto-saved memory. Zero uses the service default.
	MemoryTTLDays int32
	// CustomCommands names the user-defined commands the selection binds. They
	// are part of the run spec because a remote runtime is granted the command
	// scope from them.
	CustomCommands []string
	// Delegates names the agents the selection may call as tools. It is part of
	// the spec because it decides the capability scope a run is granted, which
	// is decided before the runtime is chosen.
	Delegates []string
	// Calls names the agents the selection may call as tools, including
	// external A2A agents. The runtime dispatches a call by the target's kind.
	Calls []string
	// SessionTools grants the session capability: the caller's own sessions can
	// be listed, read, continued and forked.
	SessionTools bool
	// Unattended marks a run nobody is watching, such as a scheduled
	// occurrence. Tools whose contract is to ask a human (ask_user_question)
	// are removed from its tool set: an interrupt raised there would park the
	// run until the question expires, and no one is coming to answer it.
	Unattended bool
	// Webhooks names the run-lifecycle hooks the agents declared, unioned across
	// the selection and passed through as names: the dispatcher resolves them
	// against the hook table at call time so a hook disabled a second ago is
	// gone from the next run, which a snapshot taken here would not be.
	//
	// Empty is meaningful and is not "no hooks": it means no agent declared
	// any, which is what lets the deployment's own defaults apply.
	Webhooks []string
}

// Resolver resolves an agent selection into a RunSpec. It is the single
// resolution path: Ask, Approve and the runner cache all resolve through it, so
// a cached runner and a freshly built one are built from the same values by
// construction rather than by three functions that happen to agree.
type Resolver struct {
	// Resources is the store the names are resolved against. May be nil, in
	// which case only the server-wide declarations apply.
	Resources store.AIStore
	// RequireApproval is the server-wide approval list, unioned with what the
	// agents' resources declare.
	RequireApproval []string
}

// NewResolver is the one derivation of a Resolver. The biz layer and the
// engine's runner cache each build a value from it, so a change to what a
// selection resolves from cannot land on one side only.
func NewResolver(resources store.AIStore, requireApproval []string) Resolver {
	return Resolver{Resources: resources, RequireApproval: requireApproval}
}

// resolvedSelection is an agent selection resolved once.
//
// spec is the graph contract the runner cache consumes. models are the
// endpoints its agents declare, which the spec deliberately omits: they decide
// which endpoint a run uses, not what its graph looks like. set is the loaded
// records, kept because the admission gate asks the same records the spec was
// derived from — re-loading them there is how two answers to one question start
// disagreeing.
type resolvedSelection struct {
	set    agentSet
	spec   RunSpec
	models []agentModel
	// narrowed records that the caller asked for a subset of the effective
	// capabilities. It is not derivable from the spec — an agent with no
	// capabilities and a run narrowed to none look identical there — and both
	// the runner-cache bypass and the resume path need to tell those apart.
	narrowed bool
	// err is a resolution failure that must refuse the run (a prompt template
	// that does not render, for example). Resolve ignores it for callers that
	// only need the spec; Ask and Approve check it.
	err error
}

// agentModel is one endpoint an agent declares, named with the agent for the
// fallback warning. It carries no policy.
type agentModel struct {
	agent string
	name  string
}

// resolveOptions says which host-derived parts of a resolution to skip.
//
// A composed run replaces the host's instruction and resource set, so resolving
// them first would be wasted work at best and a refusal at worst: a host prompt
// that does not render, or a host resource the caller is not authorized for,
// must not block a run that never uses either.
type resolveOptions struct {
	skipInstruction    bool
	skipTargetPolicies bool
}

// Resolve returns the spec for an agent selection. It is the shape the
// engine's runner cache needs (a function of the names alone).
func (r Resolver) Resolve(ctx context.Context, agents []string) RunSpec {
	return r.resolveSelection(ctx, agents, resolveOptions{}).spec
}

// resolveRunSelection resolves a run's selection once and finishes the parts of
// the spec that depend on the session rather than on the names: an existing
// session keeps the runtime profile it was created on, and the selected agents'
// output schemas are compiled into the spec.
//
// It exists so Ask and Approve cannot disagree about what a selection resolves
// to. resolveSelection deliberately leaves RunSpec.OutputSchema unset — the
// runner cache has no session to pin and no schema to compile — so any future
// per-run spec field belongs here, next to the schema, rather than in either
// entry point.
func (b *AgentBiz) resolveRunSelection(
	ctx context.Context, agents []string, sessionID string, opts resolveOptions,
) (resolvedSelection, error) {
	sel := b.resolver.resolveSelection(ctx, agents, opts)
	if pinned, ok := b.pinnedRuntime(ctx, sessionID); ok {
		sel.set = sel.set.withRuntimeProfile(pinned)
	}
	schema, schemaStrict, err := sel.set.outputSchema()
	if err != nil {
		return sel, err
	}
	sel.spec.OutputSchema, sel.spec.OutputSchemaStrict = schema, schemaStrict
	if sel.err != nil {
		return sel, sel.err
	}
	return sel, nil
}

// resolveSelection loads the selection once and reads every part of the run off
// that one load. Resolving each part separately would read the same agent
// records five times to answer one question about them.
func (r Resolver) resolveSelection(
	ctx context.Context, agents []string, opts resolveOptions,
) resolvedSelection {
	set, err := loadAgentSet(ctx, r.Resources, agents)
	if err != nil {
		return resolvedSelection{set: set, err: err}
	}
	var instruction string
	if !opts.skipInstruction {
		instruction, err = set.instruction(ctx, r.Resources)
	}
	memory, memoryErr := set.memorySettings()
	if err == nil && memoryErr != nil {
		err = memoryErr
	}
	// Calls split by target kind: a native target is an ordinary delegation and
	// uses the delegation tool, while an external A2A target gets the dispatcher
	// tool. Keeping them in one RunSpec field would make the engine add both
	// tools even when only one kind is present.
	calls := set.union(func(a *model.AgentM) []string { return a.EffectiveCalls() })
	nativeCalls, externalCalls, callErr := r.splitCalls(ctx, calls)
	if err == nil {
		err = callErr
	}
	delegates := set.union(func(a *model.AgentM) []string { return a.EffectiveDelegates() })
	if len(nativeCalls) > 0 {
		delegates = append(delegates, nativeCalls...)
	}

	spec := RunSpec{
		// The names travel with the spec because the builder names the runner
		// and isolates its skills on disk by them. Dropping them here would
		// make every cached agent set share one default key — and therefore
		// one skills directory.
		Agents:               agents,
		Instruction:          instruction,
		MemoryName:           memory.Name,
		MemoryEnabled:        memory.Enabled,
		MemoryEmbeddingModel: memory.EmbeddingModel,
		MemoryAutoSaveEvery:  memory.AutoSaveEvery,
		MemoryTTLDays:        memory.TTLDays,
		MCPs:                 set.union(func(a *model.AgentM) []string { return a.EffectiveMCPs() }),
		Skills:               set.union(func(a *model.AgentM) []string { return a.EffectiveSkills() }),
		KnowledgeBases:       set.union(func(a *model.AgentM) []string { return a.EffectiveKnowledgeBases() }),
		PromptRefs:           set.union(func(a *model.AgentM) []string { return a.PromptRefs }),
		Models:               modelRefs(set.models()),
		Approval:             set.approval(ctx, r.Resources, r.RequireApproval),
		CustomCommands:       set.union(func(a *model.AgentM) []string { return a.EffectiveCustomCommands() }),
		Delegates:            delegates,
		Calls:                externalCalls,
		SessionTools:         set.any(func(a *model.AgentM) bool { return a.EffectiveSessionTools() }),
		Webhooks:             set.union(func(a *model.AgentM) []string { return a.EffectiveWebhooks() }),
	}
	// Checked against the resolved sets rather than the declared ones: a
	// selector or an aggregated agent contributes capabilities the caller never
	// wrote down, and a boundary that only looked at what was named would be one
	// an edit could walk around.
	if err == nil && !opts.skipTargetPolicies {
		err = r.checkReferencePolicies(ctx, set.records, referrerNamespaces(set.records), spec, false)
	}
	return resolvedSelection{set: set, err: err, spec: spec, models: set.models()}
}

// splitCalls separates native delegation targets from external A2A targets.
// A target the store cannot load is dropped: it was already reported as a
// missing reference by the controller, and guessing its kind would offer a tool
// that cannot work.
func (r Resolver) splitCalls(ctx context.Context, names []string) (native, external []string, err error) {
	if len(names) == 0 {
		return nil, nil, nil
	}
	if r.Resources == nil {
		// No store to classify with. Treat them as native so the delegation tool
		// is at least offered; the runtime will report an unknown target.
		return names, nil, nil
	}
	for _, name := range names {
		// Calls are stored canonical ("namespace/name"); a bare legacy entry
		// resolves through the store's unique-match rule.
		ref := model.SplitRef(name)
		opts := where.F("name", ref.Name)
		if ref.Namespace != "" {
			opts = opts.F("namespace", ref.Namespace)
		}
		row, getErr := r.Resources.Agents().Get(ctx, opts)
		if getErr != nil {
			if errors.Is(getErr, declstore.ErrAmbiguousKey) {
				return nil, nil, getErr
			}
			continue
		}
		if row == nil {
			// The controller already reported it missing; guessing its kind
			// would offer a tool that cannot work.
			continue
		}
		if row.Kind == "external" {
			external = append(external, name)
		} else {
			native = append(native, name)
		}
	}
	return native, external, nil
}
