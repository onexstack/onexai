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
	"fmt"
	"strings"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/prompttmpl"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/apis/agent/validation/outputschema"
	"github.com/onexstack/onexai/pkg/declclient/declstore"
)

// agentSet is an agent selection with its records loaded once.
//
// Everything a run needs to know about a selection — the instruction, the MCP
// servers, the skills, the knowledge bases, the approval patterns — is a
// function of the same records. Reading them five times per selection was
// wasted work that only happened to be cheap because the store serves from a
// snapshot; this makes it cheap by construction, and removes the two copies of
// the walk (Ask's and the Resolver's) that could drift apart.
type agentSet struct {
	// names are the selected agent names, in the order they were given.
	names []string
	// records are the ones that resolved, in selection order.
	records []*model.AgentM
}

// loadAgentSet resolves the selected names to records. A name that resolves to
// nothing is skipped rather than failing: an agent deleted while a conversation
// referenced it should not break the conversation.
func loadAgentSet(ctx context.Context, resources store.AIStore, names []string) (agentSet, error) {
	set := agentSet{names: names}
	if resources == nil || len(names) == 0 {
		return set, nil
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		// An agent reference is (namespace, name). A qualified entry names its
		// own namespace; a bare one is resolved by the store, which refuses a
		// name that exists in more than one namespace rather than guessing.
		ref := model.SplitRef(name)
		opts := where.F("name", ref.Name)
		if ref.Namespace != "" {
			opts = opts.F("namespace", ref.Namespace)
		}
		a, err := resources.Agents().Get(ctx, opts)
		if err != nil {
			// An ambiguity is the caller's to fix (name a namespace); a missing
			// or unreadable record is skipped, as it always was: the controller
			// has already reported it and the run continues without it.
			if errors.Is(err, declstore.ErrAmbiguousKey) {
				return set, err
			}
			continue
		}
		if a != nil {
			set.records = append(set.records, a)
		}
	}
	return set, nil
}

// union collects one per-agent resource list across the selection, first-seen
// order preserved.
//
// The three per-agent scopes differ only in which field they read, so they
// share this walk: a change to the union cannot then land on one scope and miss
// the others.
// models returns the model endpoints the selection declares, in selection
// order, skipping blank names. It is what the run's endpoint resolution reads
// after an explicitly requested model has been ruled out.
// outputSchema returns the one structured-output contract the selection
// shares. Two agents with different schemas cannot describe one answer, so the
// conflict is refused rather than resolved by picking one.
func (s agentSet) outputSchema() (string, bool, error) {
	var canonical string
	var strict bool
	for _, record := range s.records {
		if record == nil {
			continue
		}
		schema, recordStrict := record.EffectiveOutputSchema()
		if schema == "" {
			continue
		}
		compiled, _, err := outputschema.Compile([]byte(schema))
		if err != nil {
			return "", false, errno.ErrInvalidArgument.WithMessage(
				"agent %q names an invalid output schema", record.Name)
		}
		if canonical == "" {
			canonical = string(compiled)
			strict = recordStrict
			continue
		}
		if canonical != string(compiled) {
			return "", false, errno.ErrInvalidArgument.WithMessage("agents in one run name different output schemas")
		}
	}
	return canonical, strict, nil
}

// memoryConfig returns the selection's memory setting. Two agents with
// different embedding models cannot share one memory space, so that conflict is
// refused.
// memorySettings is the selection's memory configuration.
type memorySettings struct {
	Name           string
	Enabled        bool
	EmbeddingModel string
	AutoSaveEvery  int32
	TTLDays        int32
}

// memorySettings resolves the memory configuration of the selection.
//
// It is one walk because every answer comes from the same records and must
// agree: a run that read the model from one agent and the interval from another
// would save to a setting nobody wrote.
func (s agentSet) memorySettings() (memorySettings, error) {
	var settings memorySettings
	for _, record := range s.records {
		if record == nil {
			continue
		}
		memory := record.EffectiveMemory()
		if !memory.Enabled {
			continue
		}
		settings.Enabled = true
		if settings.Name == "" {
			settings.Name = memory.Name
		}
		if settings.AutoSaveEvery == 0 {
			settings.AutoSaveEvery = memory.AutoSaveEvery
			settings.TTLDays = memory.TTLDays
		}
		if settings.EmbeddingModel == "" {
			settings.EmbeddingModel = memory.EmbeddingModel
			continue
		}
		if memory.EmbeddingModel != "" && memory.EmbeddingModel != settings.EmbeddingModel {
			return memorySettings{}, errno.ErrInvalidArgument.WithMessage(
				"agents in one run name different memory embedding models")
		}
		if memory.Name != "" && settings.Name != memory.Name {
			return memorySettings{}, errno.ErrInvalidArgument.WithMessage(
				"agents in one run name different memories")
		}
	}
	return settings, nil
}

// withRuntimeProfile returns a copy of the set whose records all name profile.
// It is how a session's pinned runtime overrides the agents' current spec
// without mutating the store's cached records.
func (s agentSet) withRuntimeProfile(profile string) agentSet {
	if profile == "" {
		return s
	}
	out := s
	out.records = make([]*model.AgentM, len(s.records))
	for i, record := range s.records {
		if record == nil {
			continue
		}
		clone := *record
		clone.RuntimeProfile = profile
		out.records[i] = &clone
	}
	return out
}

// runtimeProfile returns the runtime profile the selection requires. An empty
// result means the deployment default. A selection that names two different
// profiles is refused rather than silently picking one: the agents would share
// one run and one event stream, which only one runtime can produce.
func (s agentSet) runtimeProfile() (string, error) {
	var profile string
	for _, a := range s.records {
		if a == nil || strings.TrimSpace(a.RuntimeProfile) == "" {
			continue
		}
		if profile == "" {
			profile = a.RuntimeProfile
			continue
		}
		if profile != a.RuntimeProfile {
			return "", errno.ErrInvalidArgument.WithMessage(
				"agents in one run name different runtime profiles: %q and %q", profile, a.RuntimeProfile)
		}
	}
	return profile, nil
}

func (s agentSet) models() []agentModel {
	var out []agentModel
	for _, a := range s.records {
		if a == nil {
			continue
		}
		name := a.EffectiveModel()
		if strings.TrimSpace(name) == "" {
			continue
		}
		out = append(out, agentModel{agent: a.Name, name: name})
	}
	return out
}

// modelRefs renders the selection's model endpoints as names, preserving
// selection order and dropping duplicates.
func modelRefs(models []agentModel) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, m := range models {
		if m.name == "" {
			continue
		}
		if _, dup := seen[m.name]; dup {
			continue
		}
		seen[m.name] = struct{}{}
		out = append(out, m.name)
	}
	return out
}

func (s agentSet) union(pick func(*model.AgentM) []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, a := range s.records {
		for _, resource := range pick(a) {
			if strings.TrimSpace(resource) == "" || seen[resource] {
				continue
			}
			seen[resource] = true
			out = append(out, resource)
		}
	}
	return out
}

// any reports whether any selected agent declares the flag. A capability that
// is a grant rather than a resource list is on if anyone in the selection asked
// for it: an agent added to a conversation must not silently lose a tool the
// run was told it had.
func (s agentSet) any(pick func(*model.AgentM) bool) bool {
	for _, a := range s.records {
		if pick(a) {
			return true
		}
	}
	return false
}

// instruction builds the per-run system-prompt override. Each agent contributes
// its aggregated Prompt when the controller has produced it, otherwise the
// first bound Prompt that resolves. Empty means the engine's default applies.
func (s agentSet) instruction(ctx context.Context, resources store.AIStore) (string, error) {
	parts := make([]string, 0, len(s.records))
	for _, a := range s.records {
		p, err := effectivePrompt(ctx, resources, a)
		if err != nil {
			return "", err
		}
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// effectivePrompt resolves one agent's persona. The aggregated Prompt wins when
// the controller has produced it; while it is being (re)created, or when the
// controller is unavailable, the bound prompt refs are tried in order.
//
// A missing, disabled or empty Prompt is skipped rather than ending the walk: a
// binding that cannot be used must not hide the ones after it.
func effectivePrompt(ctx context.Context, resources store.AIStore, a *model.AgentM) (string, error) {
	names := make([]string, 0, 1+len(a.PromptRefs))
	seen := make(map[string]bool, 1+len(a.PromptRefs))
	add := func(name string) {
		if name == "" || name == agentv1alpha1.BuiltinPrompt || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	// The derived Prompt lives in the agent's own namespace, so it is qualified
	// here rather than looked up by bare name.
	add(model.CanonicalRef(a.Namespace, a.AggregatedPrompt))
	for _, name := range a.PromptRefs {
		add(name)
	}

	for _, name := range names {
		if content, sources, ok := promptRecord(ctx, resources, name); ok {
			rendered, err := prompttmpl.Render(content, prompttmpl.Data{
				AgentName:      a.Name,
				AgentNamespace: a.Namespace,
				Description:    a.Description,
			}, sources)
			if err != nil {
				return "", errno.ErrInternal.WithMessage("the prompt of agent %q could not be rendered", a.Name)
			}
			return rendered, nil
		}
	}
	return "", nil
}

// promptRecord returns a prompt's content and data sources.
func promptRecord(ctx context.Context, resources store.AIStore, name string) (string, map[string]string, bool) {
	if name == "" || name == agentv1alpha1.BuiltinPrompt || resources == nil {
		return "", nil, false
	}
	p, err := resources.Prompts().Get(ctx, model.EntryFilter(name))
	if err != nil || p == nil || !p.Enabled() || p.Content == "" {
		return "", nil, false
	}
	return p.Content, p.DataSources, true
}

// approval collects the tool-name patterns a run must get a human's approval
// for, from the three places one can be declared: the server-wide default, the
// selected agents, and the MCP servers those agents may use.
//
// The sources are unioned, so nothing here can narrow anything else: an agent
// cannot release a tool its MCP server gates. That is what makes gating a
// server meaningful — the server's declaration travels with the tool wherever
// it is handed out.
//
// There is deliberately no exemption list. A tool not listed is not gated, and
// a deployment that wants every tool gated lists "*".
func (s agentSet) approval(ctx context.Context, resources store.AIStore, serverWide []string) []string {
	out := make([]string, 0, len(serverWide)+len(s.records))
	out = append(out, serverWide...)
	if resources == nil {
		return out
	}
	for _, a := range s.records {
		out = append(out, a.EffectiveRequireApproval()...)
		for _, mcpName := range a.EffectiveMCPs() {
			m, err := resources.MCPs().Get(ctx, model.EntryFilter(mcpName))
			if err != nil || m == nil {
				continue
			}
			out = append(out, m.RequireApproval...)
		}
	}
	return out
}

// promptContent resolves a Prompt name to its text. A missing, disabled or
// empty Prompt resolves to "", never to its name: a name is not a prompt.
func promptContent(ctx context.Context, resources store.AIStore, name string) string {
	if name == "" || name == agentv1alpha1.BuiltinPrompt {
		return ""
	}
	if resources == nil {
		return ""
	}
	p, err := resources.Prompts().Get(ctx, model.EntryFilter(name))
	if err != nil || p == nil || !p.Enabled() || p.Content == "" {
		return ""
	}
	return p.Content
}

// recordedOrResolved prefers the resource names a pause recorded over
// re-deriving them from the agent selection.
//
// Re-deriving is the fallback, not the default: an agent edited while a human
// was deciding would otherwise hand the resumed half a different tool set than
// the half that paused, and the checkpoint's interrupt addresses would no
// longer match the tools answering them. An empty recorded list means the
// checkpoint predates the fields, which is the one case re-deriving serves.
func recordedOrResolved(recorded []string, resolve func() []string) []string {
	if len(recorded) > 0 {
		return recorded
	}
	return resolve()
}

// applyInvokedSkills folds the skills the asker invoked explicitly into the
// run's instruction, and refuses a name the selection does not declare.
//
// A command is the asker saying "run this", so the run is told what was asked
// for rather than left to infer it from the question — which is the difference
// between a slash command and a sentence that happens to mention one.
//
// The refusal is the half that matters. The caller offering the command and the
// caller running it both read the same skill declarations, so a name outside
// them means one of the two is looking at something that has changed since.
// Answering anyway would present a command that silently does nothing, and the
// person who typed it would have no way to tell that from a model that chose
// not to use the skill.
//
// The invoked names are advisory from here on: they steer the run by naming
// what was asked for, and the skill's own instructions are what the middleware
// hands the model. Making the choice itself binding would need the skill
// middleware to take a selection, which it does not have.
func applyInvokedSkills(instruction string, declared, invoked []string) (string, error) {
	if len(invoked) == 0 {
		return instruction, nil
	}

	have := make(map[string]struct{}, len(declared))
	for _, name := range declared {
		have[name] = struct{}{}
	}
	for _, name := range invoked {
		if _, ok := have[name]; !ok {
			return "", errno.ErrInvalidArgument.WithMessage(
				"skill %q is not declared by this run's agents", name)
		}
	}

	directive := fmt.Sprintf(
		"本次请求由调用方显式调用了技能：%s。请直接使用该技能来完成用户的要求；"+
			"不要改选其他技能，也不要凭自己的判断替代它。",
		strings.Join(invoked, "、"),
	)
	if instruction == "" {
		return directive, nil
	}
	return directive + "\n\n" + instruction, nil
}
