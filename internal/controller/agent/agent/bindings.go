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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"maps"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/onexstack/onexai/internal/agent/model"
	agentbindingcontroller "github.com/onexstack/onexai/internal/controller/agent/agentbinding"
	clusteragentbindingcontroller "github.com/onexstack/onexai/internal/controller/agent/clusteragentbinding"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// maxBindingContributions caps the provenance list stored on an agent, not the
// capability union: a binding is additive, so dropping one to save status bytes
// would silently change behavior. The total is reported alongside the list.
const maxBindingContributions = 64

// bindingSet is the union every matching AgentBinding contributes to one agent.
type bindingSet struct {
	contributions  []agentv1alpha1.AgentBindingContribution
	total          int32
	truncated      bool
	prompts        []promptRef
	skills         []string
	mcps           []string
	knowledgeBases []string
	customCommands []string
	commands       []string
	missing        []string
	// memoryRef is the memory the binding layer contributes. It is a scalar:
	// the agent's own declaration wins, and a template outranks a binding.
	memoryRef *agentv1alpha1.Reference
	// modelRef is the model the binding layer contributes. Like memoryRef it is
	// a scalar: the agent's own model wins, and a template outranks a binding.
	modelRef *agentv1alpha1.Reference
	// modelConflict records bindings that disagree on the model. It is reported
	// instead of silently picking one.
	modelConflict bool
	// conflicts records bindings that disagree on the scalar memory. It is
	// reported instead of silently picking one.
	conflicts []string
	// admin* record references contributed by a ClusterAgentBinding. A
	// cluster-scoped binding is created by an administrator, so it is the
	// authorization: the namespace filter must not ask for a ReferenceGrant it
	// would otherwise require of a namespaced binding.
	adminSkills  map[string]struct{}
	adminMCPs    map[string]struct{}
	adminKBs     map[string]struct{}
	adminPrompts map[string]struct{}
	stale        bool
}

func adminSet(set *bindingSet, kind string) map[string]struct{} {
	switch kind {
	case "Skill":
		if set.adminSkills == nil {
			set.adminSkills = map[string]struct{}{}
		}
		return set.adminSkills
	case "MCPServer":
		if set.adminMCPs == nil {
			set.adminMCPs = map[string]struct{}{}
		}
		return set.adminMCPs
	case "KnowledgeBase":
		if set.adminKBs == nil {
			set.adminKBs = map[string]struct{}{}
		}
		return set.adminKBs
	default:
		return nil
	}
}

// resolveBindings applies the binding layer to one agent.
//
// Bindings are read from the agent's namespace only. Each binding's resolved
// contribution is taken from its status when the status is current; otherwise
// it is computed here and the binding is reported stale, so a binding controller
// that has not caught up yet never makes an agent read as unbound.
func (r *Reconciler) resolveBindings(ctx context.Context, agent *agentv1alpha1.Agent) (bindingSet, error) {
	var set bindingSet

	// A disabled agent is withdrawn from use, exactly like a disabled Skill is
	// invisible to resolution. The binding controller applies the same rule when
	// it counts matches, so the agent's view and the binding's view agree.
	if !agent.Spec.Enabled {
		return set, nil
	}

	list := &agentv1alpha1.AgentBindingList{}
	if err := r.List(ctx, list, client.InNamespace(agent.Namespace)); err != nil {
		return set, err
	}

	agentLabels := labels.Set(agent.Labels)
	matched := make([]*agentv1alpha1.AgentBinding, 0, len(list.Items))
	for i := range list.Items {
		binding := &list.Items[i]
		if binding.Spec.Enabled != nil && !*binding.Spec.Enabled {
			continue
		}
		if !bindingMatches(binding, agent, agentLabels) {
			continue
		}
		matched = append(matched, binding)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Name < matched[j].Name })
	set.total = int32(len(matched))

	for _, binding := range matched {
		resolved, missing, stale, err := r.bindingContribution(ctx, binding)
		if err != nil {
			return set, err
		}
		if stale {
			set.stale = true
		}

		set.prompts = append(set.prompts, referencesToPromptRefs(agent.Namespace, resolved.prompts)...)
		set.skills = append(set.skills, resolved.skills...)
		set.mcps = append(set.mcps, resolved.mcps...)
		set.knowledgeBases = append(set.knowledgeBases, resolved.knowledgeBases...)
		set.customCommands = append(set.customCommands, resolved.customCommands...)
		set.commands = append(set.commands, resolved.commands...)
		mergeModel(&set, resolved.modelRef)
		set.missing = append(set.missing, missing...)

		if len(set.contributions) < maxBindingContributions {
			set.contributions = append(set.contributions, contributionOf(binding, resolved))
		} else {
			set.truncated = true
		}
	}

	if err := r.resolveClusterBindings(ctx, agent, &set); err != nil {
		return set, err
	}
	// A binding can name a resource this agent's namespace may not use. The
	// target's own declaration decides; without this filter status.resolved
	// would advertise what the run is going to refuse.
	if err := r.filterBindingNamespaces(ctx, agent.Namespace, &set); err != nil {
		return set, err
	}

	// Binding contributions are canonical "namespace/name", but a custom command
	// only runs inside the agent's own namespace (see localizeCustomCommands).
	set.customCommands = localizeCustomCommands(agent.Namespace, set.customCommands, &set.missing)

	set.skills = sortedUnique(set.skills)
	set.mcps = sortedUnique(set.mcps)
	set.knowledgeBases = sortedUnique(set.knowledgeBases)
	set.customCommands = sortedUnique(set.customCommands)
	set.commands = sortedUnique(set.commands)

	return set, nil
}

// resolveClusterBindings merges every ClusterAgentBinding whose audience
// includes this agent.
//
// The object is cluster-scoped, so it is listed without a namespace filter. The
// namespace list is what keeps that from being an unbounded grant: an agent is
// only in the audience when the binding named its namespace.
func (r *Reconciler) resolveClusterBindings(
	ctx context.Context, agent *agentv1alpha1.Agent, set *bindingSet,
) error {
	list := &agentv1alpha1.ClusterAgentBindingList{}
	if err := r.List(ctx, list); err != nil {
		return err
	}

	matched := make([]*agentv1alpha1.ClusterAgentBinding, 0, len(list.Items))
	for i := range list.Items {
		if clusteragentbindingcontroller.MatchesAgent(&list.Items[i], agent) {
			matched = append(matched, &list.Items[i])
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Name < matched[j].Name })
	set.total += int32(len(matched))

	for _, binding := range matched {
		resolved, missing, stale, err := r.clusterBindingContribution(ctx, binding)
		if err != nil {
			return err
		}
		if stale {
			set.stale = true
		}

		set.prompts = append(set.prompts, referencesToPromptRefs(agent.Namespace, resolved.prompts)...)
		set.skills = append(set.skills, resolved.skills...)
		set.mcps = append(set.mcps, resolved.mcps...)
		set.knowledgeBases = append(set.knowledgeBases, resolved.knowledgeBases...)
		set.customCommands = append(set.customCommands, resolved.customCommands...)
		set.commands = append(set.commands, resolved.commands...)
		mergeModel(set, resolved.modelRef)
		set.missing = append(set.missing, missing...)

		// A ClusterAgentBinding is an administrator's direct grant. Record its
		// refs so the namespace filter does not demand a ReferenceGrant too.
		for _, entry := range resolved.skills {
			adminSet(set, "Skill")[entry] = struct{}{}
		}
		for _, entry := range resolved.mcps {
			adminSet(set, "MCPServer")[entry] = struct{}{}
		}
		for _, entry := range resolved.knowledgeBases {
			adminSet(set, "KnowledgeBase")[entry] = struct{}{}
		}
		if set.adminPrompts == nil {
			set.adminPrompts = map[string]struct{}{}
		}
		for _, entry := range resolved.prompts {
			set.adminPrompts[model.ResolveRef(agent.Namespace, entry).Canonical()] = struct{}{}
		}

		if resolved.memoryRef != nil {
			switch {
			case set.memoryRef == nil:
				set.memoryRef = resolved.memoryRef
			case !sameReference(set.memoryRef, resolved.memoryRef):
				// Two bindings naming different memories is a structural conflict.
				// Picking one silently would make the effective memory depend on
				// the object name.
				set.conflicts = append(set.conflicts, "ClusterAgentBinding/"+binding.Name)
			}
		}

		if len(set.contributions) < maxBindingContributions {
			set.contributions = append(set.contributions, clusterContributionOf(binding, resolved))
		} else {
			set.truncated = true
		}
	}
	return nil
}

// clusterBindingContribution returns the resolved sets for one cluster binding,
// preferring the status its controller computed.
func (r *Reconciler) clusterBindingContribution(
	ctx context.Context, binding *agentv1alpha1.ClusterAgentBinding,
) (resolvedBinding, []string, bool, error) {
	if binding.Generation > 0 && binding.Status.ObservedGeneration == binding.Generation {
		return resolvedBinding{
			prompts:        binding.Status.Resolved.Prompts,
			skills:         binding.Status.Resolved.Skills,
			mcps:           binding.Status.Resolved.MCPs,
			knowledgeBases: binding.Status.Resolved.KnowledgeBases,
			customCommands: binding.Status.Resolved.CustomCommands,
			commands:       binding.Status.Resolved.Commands,
			modelRef:       binding.Spec.ModelRef,
		}, binding.Status.Missing, false, nil
	}

	contribution, err := clusteragentbindingcontroller.Compute(ctx, r.Client, binding)
	if err != nil {
		return resolvedBinding{}, nil, false, err
	}
	return resolvedBinding{
		prompts:        contribution.Prompts,
		skills:         contribution.Skills,
		mcps:           contribution.MCPs,
		knowledgeBases: contribution.KnowledgeBases,
		customCommands: contribution.CustomCommands,
		commands:       contribution.Commands,
		memoryRef:      contribution.Memory,
		modelRef:       contribution.Model,
	}, contribution.Missing, true, nil
}

// filterBindingNamespaces drops binding-contributed references the target does
// not admit for this agent, and reports them through missing.
func (r *Reconciler) filterBindingNamespaces(
	ctx context.Context, agentNamespace string, set *bindingSet,
) error {
	owner := model.NormalizeNamespace(agentNamespace)

	filter := func(kind string, entries []string, newObject func() client.Object) ([]string, error) {
		kept := entries[:0]
		admin := adminSet(set, kind)
		for _, entry := range entries {
			if _, ok := admin[entry]; ok {
				kept = append(kept, entry)
				continue
			}
			ref := model.SplitRef(entry)
			if ref.Namespace == "" || ref.Namespace == owner {
				kept = append(kept, entry)
				continue
			}
			_, ok, err := r.exists(ctx, ref.Namespace, ref.Name, newObject())
			if err != nil {
				return nil, err
			}
			if !ok || len(grantWildcard(ctx, agentNamespace, ref.Namespace, kind, ref.Name)) == 0 {
				set.missing = append(set.missing, kind+"/"+ref.Canonical()+" (namespace not allowed)")
				continue
			}
			kept = append(kept, entry)
		}
		return kept, nil
	}

	var err error
	if set.skills, err = filter("Skill", set.skills,
		func() client.Object { return &agentv1alpha1.Skill{} }); err != nil {
		return err
	}
	if set.mcps, err = filter("MCPServer", set.mcps,
		func() client.Object { return &agentv1alpha1.MCPServer{} }); err != nil {
		return err
	}
	if set.knowledgeBases, err = filter("KnowledgeBase", set.knowledgeBases,
		func() client.Object { return &agentv1alpha1.KnowledgeBase{} }); err != nil {
		return err
	}

	prompts := set.prompts[:0]
	for _, ref := range set.prompts {
		namespace := ref.Namespace
		if namespace == "" {
			namespace = owner
		}
		if _, ok := set.adminPrompts[model.CanonicalRef(namespace, ref.Name)]; ok {
			prompts = append(prompts, ref)
			continue
		}
		if namespace == owner {
			prompts = append(prompts, ref)
			continue
		}
		_, ok, err := r.exists(ctx, namespace, ref.Name, &agentv1alpha1.Prompt{})
		if err != nil {
			return err
		}
		if !ok || len(grantWildcard(ctx, agentNamespace, namespace, "Prompt", ref.Name)) == 0 {
			set.missing = append(set.missing, "Prompt/"+namespace+"/"+ref.Name+" (namespace not allowed)")
			continue
		}
		prompts = append(prompts, ref)
	}
	set.prompts = prompts

	return nil
}

// localizeCustomCommands reduces binding-contributed CustomCommand references to
// the names the agent service can actually read.
//
// A binding stores a reference as the canonical "namespace/name" because a
// cluster-scoped object has no namespace to default to. The agent service,
// however, reads a command from its namespace-scoped store and invokes it with
// the agent's namespace as the execution context: only the command in the
// agent's own namespace can run. Writing the qualified spelling into
// status.resolved would advertise a command no surface can resolve —
// ContextCommands skips every name containing "/" — so a same-namespace
// contribution is reduced to its bare name, and a cross-namespace one is
// reported through missing instead of silently disappearing.
func localizeCustomCommands(agentNamespace string, entries []string, missing *[]string) []string {
	if len(entries) == 0 {
		return entries
	}
	owner := model.NormalizeNamespace(agentNamespace)
	kept := make([]string, 0, len(entries))
	for _, entry := range entries {
		ref := model.SplitRef(entry)
		switch {
		case ref.Name == "":
			continue
		case ref.Namespace == "" || ref.Namespace == owner:
			kept = append(kept, ref.Name)
		default:
			*missing = append(*missing, "CustomCommand/"+ref.Canonical()+" (cross-namespace)")
		}
	}
	return kept
}

// mergeModel folds one binding's model into the set. The first contribution
// wins; a later one that names a different model is a structural conflict, not
// an override.
func mergeModel(set *bindingSet, model *agentv1alpha1.Reference) {
	if model == nil || model.Name == "" {
		return
	}
	if set.modelRef == nil {
		copied := *model
		set.modelRef = &copied
		return
	}
	if !sameReference(set.modelRef, model) {
		set.modelConflict = true
	}
}

func sameReference(left, right *agentv1alpha1.Reference) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Name == right.Name && left.Namespace == right.Namespace
}

// bindingChangePredicate ignores writes that cannot change what an agent
// resolves to.
//
// Status writes are not ignored wholesale: the binding controller recomputes
// status.resolved when a referenced CustomCommand appears, changes or is
// deleted, and the generation stays the same. An agent that reconciled just
// before that write read the previous resolved set; without this predicate it
// would never be woken to read the new one.
func bindingChangePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldBinding, okOld := e.ObjectOld.(*agentv1alpha1.AgentBinding)
			newBinding, okNew := e.ObjectNew.(*agentv1alpha1.AgentBinding)
			if !okOld || !okNew {
				return true
			}
			return bindingSpecChanged(oldBinding, newBinding) ||
				bindingStatusChanged(oldBinding, newBinding)
		},
	}
}

// clusterBindingChangePredicate is bindingChangePredicate for cluster bindings.
func clusterBindingChangePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldBinding, okOld := e.ObjectOld.(*agentv1alpha1.ClusterAgentBinding)
			newBinding, okNew := e.ObjectNew.(*agentv1alpha1.ClusterAgentBinding)
			if !okOld || !okNew {
				return true
			}
			return oldBinding.Generation != newBinding.Generation ||
				!maps.Equal(oldBinding.Labels, newBinding.Labels) ||
				oldBinding.Status.ObservedGeneration != newBinding.Status.ObservedGeneration ||
				!apiequality.Semantic.DeepEqual(oldBinding.Status.Resolved, newBinding.Status.Resolved) ||
				!slices.Equal(oldBinding.Status.Missing, newBinding.Status.Missing)
		},
	}
}

func bindingSpecChanged(oldBinding, newBinding *agentv1alpha1.AgentBinding) bool {
	return oldBinding.Generation != newBinding.Generation ||
		!maps.Equal(oldBinding.Labels, newBinding.Labels)
}

func bindingStatusChanged(oldBinding, newBinding *agentv1alpha1.AgentBinding) bool {
	// ObservedGeneration matters on its own: bindingContribution trusts a
	// status only while ObservedGeneration == Generation, so crossing that
	// boundary changes which answer the agent resolves from.
	return oldBinding.Status.ObservedGeneration != newBinding.Status.ObservedGeneration ||
		!apiequality.Semantic.DeepEqual(oldBinding.Status.Resolved, newBinding.Status.Resolved) ||
		!slices.Equal(oldBinding.Status.Missing, newBinding.Status.Missing)
}

func bindingMatches(binding *agentv1alpha1.AgentBinding, agent *agentv1alpha1.Agent, agentLabels labels.Set) bool {
	for _, name := range binding.Spec.Agents {
		if name == agent.Name {
			return true
		}
	}
	if binding.Spec.Selector == nil {
		return false
	}
	selector, err := metav1.LabelSelectorAsSelector(binding.Spec.Selector)
	if err != nil {
		return false
	}
	return selector.Matches(agentLabels)
}

// bindingContribution returns the resolved sets for one binding, preferring the
// status the binding controller computed.
func (r *Reconciler) bindingContribution(
	ctx context.Context, binding *agentv1alpha1.AgentBinding,
) (resolvedBinding, []string, bool, error) {
	if binding.Generation > 0 && binding.Status.ObservedGeneration == binding.Generation {
		return resolvedBinding{
			prompts:        binding.Status.Resolved.Prompts,
			skills:         binding.Status.Resolved.Skills,
			mcps:           binding.Status.Resolved.MCPs,
			knowledgeBases: binding.Status.Resolved.KnowledgeBases,
			customCommands: binding.Status.Resolved.CustomCommands,
			commands:       binding.Status.Resolved.Commands,
			modelRef:       binding.Spec.ModelRef,
		}, binding.Status.Missing, false, nil
	}

	resolved, missing, err := r.computeBindingContribution(ctx, binding)
	return resolved, missing, true, err
}

// computeBindingContribution resolves a binding's references directly. It is
// the fallback used only while the binding controller has not written status.
// The work is delegated so both controllers agree on what a binding resolves to.
func (r *Reconciler) computeBindingContribution(
	ctx context.Context, binding *agentv1alpha1.AgentBinding,
) (resolvedBinding, []string, error) {
	contribution, err := agentbindingcontroller.Compute(ctx, r.Client, binding)
	if err != nil {
		return resolvedBinding{}, nil, err
	}
	return resolvedBinding{
		prompts:        contribution.Prompts,
		skills:         contribution.Skills,
		mcps:           contribution.MCPs,
		knowledgeBases: contribution.KnowledgeBases,
		customCommands: contribution.CustomCommands,
		commands:       contribution.Commands,
		modelRef:       contribution.Model,
	}, contribution.Missing, nil
}

type resolvedBinding struct {
	prompts        []string
	skills         []string
	mcps           []string
	knowledgeBases []string
	customCommands []string
	commands       []string
	memoryRef      *agentv1alpha1.Reference
	modelRef       *agentv1alpha1.Reference
}

// promptReferences turns the binding-contributed prompt refs into the Reference
// shape resolvePromptSetWith expects.
func promptReferences(refs []promptRef) []agentv1alpha1.Reference {
	out := make([]agentv1alpha1.Reference, 0, len(refs))
	for _, ref := range refs {
		out = append(out, agentv1alpha1.Reference{Namespace: ref.Namespace, Name: ref.Name})
	}
	return out
}

// referencesToPromptRefs turns resolved entries into prompt references. An
// entry that carries a namespace names its own; a bare one belongs to the
// agent's namespace.
func referencesToPromptRefs(namespace string, names []string) []promptRef {
	refs := make([]promptRef, 0, len(names))
	for _, entry := range names {
		if entry == "" {
			continue
		}
		ref := model.ResolveRef(namespace, entry)
		refs = append(refs, promptRef{Namespace: ref.Namespace, Name: ref.Name})
	}
	return refs
}

func contributionOf(binding *agentv1alpha1.AgentBinding, resolved resolvedBinding) agentv1alpha1.AgentBindingContribution {
	return agentv1alpha1.AgentBindingContribution{
		Kind:               "AgentBinding",
		Namespace:          binding.Namespace,
		Name:               binding.Name,
		Generation:         binding.Generation,
		Hash:               hashBindingResolved(resolved),
		PromptCount:        int32(len(resolved.prompts)),
		SkillCount:         int32(len(resolved.skills)),
		MCPCount:           int32(len(resolved.mcps)),
		KnowledgeBaseCount: int32(len(resolved.knowledgeBases)),
		CustomCommandCount: int32(len(resolved.customCommands)),
		CommandCount:       int32(len(resolved.commands)),
	}
}

func clusterContributionOf(
	binding *agentv1alpha1.ClusterAgentBinding, resolved resolvedBinding,
) agentv1alpha1.AgentBindingContribution {
	return agentv1alpha1.AgentBindingContribution{
		Kind:               "ClusterAgentBinding",
		Name:               binding.Name,
		Generation:         binding.Generation,
		Hash:               hashBindingResolved(resolved),
		PromptCount:        int32(len(resolved.prompts)),
		SkillCount:         int32(len(resolved.skills)),
		MCPCount:           int32(len(resolved.mcps)),
		KnowledgeBaseCount: int32(len(resolved.knowledgeBases)),
		CustomCommandCount: int32(len(resolved.customCommands)),
		CommandCount:       int32(len(resolved.commands)),
	}
}

func hashBindingResolved(resolved resolvedBinding) string {
	raw, err := json.Marshal(resolved)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// sortedUnique sorts values and drops duplicates in place. The binding layers
// are a union, so the caller only ever needs the set, in a stable order.
func sortedUnique(values []string) []string {
	slices.Sort(values)
	return slices.Compact(values)
}

// bindingsCondition explains the binding union.
func bindingsCondition(agent *agentv1alpha1.Agent, set bindingSet) metav1.Condition {
	condition := metav1.Condition{
		Type:               agentv1alpha1.ConditionBindingsApplied,
		ObservedGeneration: agent.Generation,
	}
	switch {
	case set.total == 0:
		condition.Status = metav1.ConditionTrue
		condition.Reason = "NoBindings"
		condition.Message = "no AgentBinding selects this agent"
	case len(set.conflicts) > 0:
		condition.Status = metav1.ConditionFalse
		condition.Reason = "MemoryConflict"
		condition.Message = fmt.Sprintf("%d binding(s) matched; more than one names a different Memory: %v",
			set.total, set.conflicts)
	case set.modelConflict:
		condition.Status = metav1.ConditionFalse
		condition.Reason = "ModelConflict"
		condition.Message = fmt.Sprintf("%d binding(s) matched; more than one names a different Model", set.total)
	case len(set.missing) > 0:
		condition.Status = metav1.ConditionFalse
		condition.Reason = "ReferenceNotFound"
		condition.Message = fmt.Sprintf("%d binding(s) matched; missing or disabled: %v", set.total, set.missing)
	case set.stale:
		condition.Status = metav1.ConditionTrue
		condition.Reason = "BindingStale"
		condition.Message = fmt.Sprintf("%d binding(s) merged; at least one status was stale and was resolved inline", set.total)
	case set.truncated:
		condition.Status = metav1.ConditionTrue
		condition.Reason = "Merged"
		condition.Message = fmt.Sprintf("merged %d binding(s); provenance list truncated to %d", set.total, maxBindingContributions)
	default:
		condition.Status = metav1.ConditionTrue
		condition.Reason = "Merged"
		condition.Message = fmt.Sprintf("merged %d binding(s)", set.total)
	}
	return condition
}
