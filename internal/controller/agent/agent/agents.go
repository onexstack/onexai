// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// maxAgentSources bounds the union spec.agents and its selector may pull in.
// Explicit refs are capped by validation; this is the runtime guard for
// selector matches, mirroring maxPromptSources.
const maxAgentSources = 16

// maxAgentAggregationDepth bounds how deep agents may aggregate agents. It is
// a guard against a pathological chain, not a policy: a cycle is already a
// structural failure however shallow it is.
const maxAgentAggregationDepth = 8

// agentRef names one Agent in the root's namespace.
type agentRef struct {
	Namespace string
	Name      string
}

// agentSource is one Agent whose capability set is aggregated into the root.
type agentSource struct {
	Ref   agentRef
	Agent *agentv1alpha1.Agent
}

// agentSet is the resolved source union for one agent.
//
// A structural failure (a cycle, a depth or source-count cap) makes the whole
// union unusable rather than partially applied: capability inheritance also
// carries webhooks and approval patterns, so publishing half of it would
// silently weaken gating.
type agentSet struct {
	sources []agentSource
	missing []string
	cycle   bool
	tooDeep bool
	// tooMany reports that the source agents alone exceed maxAgentSources.
	tooMany bool
	// tooManyPrompts reports that the sources' prompt union exceeds
	// maxPromptSources. It is a separate flag because the cap and the thing
	// being capped are different: a condition that named the wrong one would
	// send an operator looking at the agent list for a prompt problem.
	tooManyPrompts bool
}

// usable reports whether the union may be applied at all.
func (s agentSet) usable() bool {
	return !s.cycle && !s.tooDeep && !s.tooMany && !s.tooManyPrompts
}

// resolveAgentSources applies "spec.agents ∪ agentSelector matches" and walks
// every source's own aggregation, transitively.
//
// The walk is breadth-first so a node reached by two paths is expanded at its
// shallowest depth. Missing and disabled sources are reported, never silently
// skipped, because a dropped source also drops the hooks and approval patterns
// it declared.
func (r *Reconciler) resolveAgentSources(ctx context.Context, root *agentv1alpha1.Agent) (agentSet, error) {
	set := agentSet{}

	adjacency := make(map[string][]string)
	rootKey := agentKey(root.Namespace, root.Name)
	visited := map[string]bool{rootKey: true}
	shallowest := map[string]int{rootKey: 0}

	queue := []*agentv1alpha1.Agent{root}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		refs, err := r.declaredAgents(ctx, current)
		if err != nil {
			return set, err
		}
		key := agentKey(current.Namespace, current.Name)
		for _, ref := range refs {
			childKey := agentKey(ref.Namespace, ref.Name)
			adjacency[key] = append(adjacency[key], childKey)
			if ref.Namespace != current.Namespace {
				// Validation rejects a cross-namespace aggregated agent. A
				// stored object that predates the rule is reported as missing
				// rather than resolved against a same-named object in the
				// wrong namespace.
				set.missing = append(set.missing, "Agent/"+ref.Namespace+"/"+ref.Name+" (cross-namespace reference)")
				continue
			}
			if childKey == rootKey {
				set.cycle = true
				continue
			}
			if visited[childKey] {
				continue
			}
			visited[childKey] = true

			// A resolved source counts toward the cap; a missing one does not,
			// because it contributes nothing either way.
			source := &agentv1alpha1.Agent{}
			if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, source); err != nil {
				if client.IgnoreNotFound(err) == nil {
					set.missing = append(set.missing, "Agent/"+ref.Name)
					continue
				}
				return set, err
			}
			if !source.Spec.Enabled {
				set.missing = append(set.missing, "Agent/"+ref.Name)
				continue
			}

			depth := shallowest[key] + 1
			if depth > maxAgentAggregationDepth {
				set.tooDeep = true
				continue
			}
			shallowest[childKey] = depth

			if len(set.sources) >= maxAgentSources {
				set.tooMany = true
				continue
			}
			set.sources = append(set.sources, agentSource{Ref: ref, Agent: source})
			queue = append(queue, source)
		}
	}

	if !set.cycle && hasAgentCycle(rootKey, adjacency) {
		set.cycle = true
	}
	set.missing = unionNames(set.missing)
	return set, nil
}

// declaredAgents returns the agents one agent aggregates: the named refs in
// declaration order, then selector matches sorted by name. The agent itself is
// never a candidate, so an empty selector cannot pull it into its own union.
func (r *Reconciler) declaredAgents(ctx context.Context, agent *agentv1alpha1.Agent) ([]agentRef, error) {
	seen := make(map[string]bool, len(agent.Spec.Agents))
	refs := make([]agentRef, 0, len(agent.Spec.Agents))

	for _, ref := range agent.Spec.Agents {
		// A cross-namespace ref keeps its namespace here so the walk can name
		// it in the missing list; see resolveAgentSources.
		namespace := ref.Namespace
		if namespace == "" {
			namespace = agent.Namespace
		}
		key := agentKey(namespace, ref.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		refs = append(refs, agentRef{Namespace: namespace, Name: ref.Name})
	}

	if agent.Spec.AgentSelector != nil {
		selector, err := metav1.LabelSelectorAsSelector(agent.Spec.AgentSelector)
		if err != nil {
			return nil, err
		}
		list := &agentv1alpha1.AgentList{}
		if err := r.List(ctx, list, client.InNamespace(agent.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
			return nil, err
		}
		matched := make([]string, 0, len(list.Items))
		for i := range list.Items {
			item := &list.Items[i]
			if !item.Spec.Enabled || item.Name == agent.Name {
				continue
			}
			matched = append(matched, item.Name)
		}
		sort.Strings(matched)
		for _, name := range matched {
			key := agentKey(agent.Namespace, name)
			if seen[key] {
				continue
			}
			seen[key] = true
			refs = append(refs, agentRef{Namespace: agent.Namespace, Name: name})
		}
	}

	return refs, nil
}

// hasAgentCycle reports whether the resolved graph contains a cycle. The graph
// is the adjacency of the objects the walk resolved; edges to missing objects
// are still present but lead nowhere, so they cannot form one.
func hasAgentCycle(root string, adjacency map[string][]string) bool {
	const (
		unvisited = iota
		inProgress
		done
	)
	color := make(map[string]int, len(adjacency))

	var visit func(string) bool
	visit = func(node string) bool {
		color[node] = inProgress
		for _, next := range adjacency[node] {
			switch color[next] {
			case inProgress:
				return true
			case unvisited:
				if visit(next) {
					return true
				}
			}
		}
		color[node] = done
		return false
	}

	return visit(root)
}

func agentKey(namespace, name string) string { return namespace + "/" + name }

// aggregatedCondition explains the union: applied, absent, or dropped.
func aggregatedCondition(agent *agentv1alpha1.Agent, set agentSet) metav1.Condition {
	condition := metav1.Condition{
		Type:               agentv1alpha1.ConditionAgentAggregated,
		ObservedGeneration: agent.Generation,
	}
	switch {
	case set.cycle:
		condition.Status = metav1.ConditionFalse
		condition.Reason = "CycleDetected"
		condition.Message = "agent aggregation forms a cycle; nothing is inherited"
	case set.tooDeep:
		condition.Status = metav1.ConditionFalse
		condition.Reason = "DepthExceeded"
		condition.Message = fmt.Sprintf("agent aggregation is deeper than %d levels; nothing is inherited", maxAgentAggregationDepth)
	case set.tooMany:
		condition.Status = metav1.ConditionFalse
		condition.Reason = "TooManySources"
		condition.Message = fmt.Sprintf("agent aggregation exceeds %d source agents; nothing is inherited", maxAgentSources)
	case set.tooManyPrompts:
		condition.Status = metav1.ConditionFalse
		condition.Reason = "TooManyPromptSources"
		condition.Message = fmt.Sprintf("the aggregated prompt set exceeds %d sources; nothing is inherited", maxPromptSources)
	case len(set.missing) > 0:
		condition.Status = metav1.ConditionFalse
		condition.Reason = "SourceUnavailable"
		condition.Message = fmt.Sprintf("missing or disabled sources: %v", set.missing)
	case len(set.sources) == 0:
		condition.Status = metav1.ConditionTrue
		condition.Reason = "NoSources"
		condition.Message = "no agent is aggregated"
	default:
		condition.Status = metav1.ConditionTrue
		condition.Reason = "Aggregated"
		condition.Message = fmt.Sprintf("aggregated %d agent(s)", len(set.sources))
	}
	return condition
}

// inheritedSets is the capability union contributed by the source agents.
type inheritedSets struct {
	skills          []string
	customCommands  []string
	mcps            []string
	knowledgeBases  []string
	webhooks        []string
	requireApproval []string
	agents          []string
	commands        []string
}

// collectInherited merges every usable source's resolved capability set. Prompt
// sources are merged into a copy of base, which is the set the aggregation step
// turns into the derived Prompt; everything else is a name union.
//
// When the merged prompt set exceeds maxPromptSources the whole inheritance is
// marked unusable: a partial merge would be a silently different prompt.
func (r *Reconciler) collectInherited(
	ctx context.Context, sources agentSet, base promptSet,
) (inheritedSets, promptSet, []string, error) {
	var (
		out     inheritedSets
		missing []string
	)

	prompts := clonePromptSet(base)
	for _, source := range sources.sources {
		agent := source.Agent

		// A source agent's bindings travel with it: aggregating an agent must not
		// become a way to inherit its own spec while dropping the platform
		// capabilities the binding layer gave it.
		sourceBindings, err := r.resolveBindings(ctx, agent)
		if err != nil {
			return out, base, missing, err
		}
		missing = append(missing, qualifyMissing(agent.Name, sourceBindings.missing)...)
		out.commands = append(out.commands, sourceBindings.commands...)

		// A source agent's templates travel with it for the same reason its
		// bindings do: aggregating an agent must not inherit its own spec while
		// dropping behavior a template contributed. Only capability sets and
		// approval patterns travel; model, runtime and authorization never do.
		sourceTemplates, err := r.resolveTemplates(ctx, agent)
		if err != nil {
			return out, base, missing, err
		}
		missing = append(missing, qualifyMissing(agent.Name, sourceTemplates.missing)...)

		promptSources, err := r.resolvePromptSetWith(ctx, agent,
			append(promptReferences(sourceBindings.prompts), sourceTemplates.prompts...),
			sourceTemplates.promptSelectors...)
		if err != nil {
			return out, base, missing, err
		}
		mergePromptSet(&prompts, promptSources)
		missing = append(missing, qualifyMissing(agent.Name, promptSources.missing)...)

		skills, skillMissing, err := r.resolveSkills(ctx, agent)
		if err != nil {
			return out, base, missing, err
		}
		out.skills = append(out.skills, skills...)
		out.skills = append(out.skills, sourceTemplates.skills...)
		out.skills = append(out.skills, sourceBindings.skills...)
		missing = append(missing, qualifyMissing(agent.Name, skillMissing)...)

		commands, commandMissing, err := r.resolveCustomCommands(ctx, agent)
		if err != nil {
			return out, base, missing, err
		}
		out.customCommands = append(out.customCommands, commands...)
		out.customCommands = append(out.customCommands, sourceTemplates.customCommands...)
		out.customCommands = append(out.customCommands, sourceBindings.customCommands...)
		missing = append(missing, qualifyMissing(agent.Name, commandMissing)...)

		mcps, mcpMissing, err := r.resolveMCPs(ctx, agent)
		if err != nil {
			return out, base, missing, err
		}
		out.mcps = append(out.mcps, mcps...)
		out.mcps = append(out.mcps, sourceTemplates.mcps...)
		out.mcps = append(out.mcps, sourceBindings.mcps...)
		missing = append(missing, qualifyMissing(agent.Name, mcpMissing)...)

		kbs, kbMissing, err := r.resolveKnowledgeBases(ctx, agent)
		if err != nil {
			return out, base, missing, err
		}
		out.knowledgeBases = append(out.knowledgeBases, kbs...)
		out.knowledgeBases = append(out.knowledgeBases, sourceTemplates.knowledgeBases...)
		out.knowledgeBases = append(out.knowledgeBases, sourceBindings.knowledgeBases...)
		missing = append(missing, qualifyMissing(agent.Name, kbMissing)...)

		out.webhooks = append(out.webhooks, sourceTemplates.webhooks...)
		for _, name := range agent.Spec.Webhooks {
			_, ok, err := r.exists(ctx, agent.Namespace, name, &agentv1alpha1.Webhook{})
			if err != nil {
				return out, base, missing, err
			}
			if ok {
				out.webhooks = append(out.webhooks, name)
			} else {
				missing = append(missing, "Agent/"+agent.Name+": Webhook/"+name)
			}
		}

		out.requireApproval = append(out.requireApproval, agent.Spec.RequireApproval...)
		out.requireApproval = append(out.requireApproval, sourceTemplates.approval...)
		out.agents = append(out.agents, agent.Name)
	}

	if prompts.tooMany {
		// The prompt union is over the cap; dropping it entirely is the only
		// merge that cannot silently change the derived prompt.
		//
		// The merged set is returned, not base: the caller reads tooMany from
		// it to report TooManyPromptSources. Returning base here made an
		// over-cap union look like a successful aggregation of sources that
		// contributed nothing.
		return inheritedSets{}, prompts, missing, nil
	}
	return out, prompts, missing, nil
}

// mergePromptSet dedupes src into dst by namespace/name, preserving order.
func mergePromptSet(dst *promptSet, src promptSet) {
	seenDeclared := make(map[string]bool, len(dst.declared))
	for _, ref := range dst.declared {
		seenDeclared[agentKey(ref.Namespace, ref.Name)] = true
	}
	for _, ref := range src.declared {
		key := agentKey(ref.Namespace, ref.Name)
		if seenDeclared[key] {
			continue
		}
		seenDeclared[key] = true
		dst.declared = append(dst.declared, ref)
	}

	seenAvailable := make(map[string]bool, len(dst.available))
	for _, source := range dst.available {
		seenAvailable[agentKey(source.Namespace, source.Name)] = true
	}
	for _, source := range src.available {
		key := agentKey(source.Namespace, source.Name)
		if seenAvailable[key] {
			continue
		}
		seenAvailable[key] = true
		dst.available = append(dst.available, source)
	}

	if len(dst.declared) > maxPromptSources {
		dst.tooMany = true
	}
}

func clonePromptSet(set promptSet) promptSet {
	clone := set
	clone.declared = append([]promptRef(nil), set.declared...)
	clone.available = append([]promptSource(nil), set.available...)
	clone.missing = append([]string(nil), set.missing...)
	return clone
}

func qualifyMissing(agentName string, missing []string) []string {
	if len(missing) == 0 {
		return nil
	}
	out := make([]string, 0, len(missing))
	for _, name := range missing {
		out = append(out, "Agent/"+agentName+": "+name)
	}
	return out
}

// unionNames returns the sorted, deduplicated union of the lists.
func unionNames(lists ...[]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, list := range lists {
		for _, name := range list {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
