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
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// maxPromptSources bounds the union a selector may pull in. Explicit refs are
// capped by validation; this is the runtime guard for selector matches.
const maxPromptSources = 32

// promptSource is one resolved Prompt's content.
type promptSource struct {
	Name      string
	Namespace string
	Template  string
	Variables []string
}

type promptRef struct {
	Namespace string
	Name      string
}

// promptSet is the declared and resolved prompt union for one agent.
type promptSet struct {
	// declared is every source the spec names, in deterministic order,
	// including ones that failed to resolve.
	declared []promptRef
	// available are the ones that exist, are enabled, and are real sources.
	available []promptSource
	// missing are qualified names plus a short reason for every declared
	// source that is not available.
	missing []string
	// tooMany reports that the union exceeded maxPromptSources.
	tooMany bool
	// sourceUnavailable reports that an aggregated agent is missing, disabled,
	// or part of a structurally broken union. The derived prompt then keeps
	// its last good content: a source that is momentarily unavailable is not
	// the same statement as "this agent has no prompt".
	sourceUnavailable bool
}

func (s promptSet) availableNames() []string {
	names := make([]string, 0, len(s.available))
	for _, source := range s.available {
		names = append(names, source.Name)
	}
	return names
}

// resolvePromptSet applies "PromptRef ∪ Prompts ∪ selector matches" and loads
// each source. It excludes derived prompts: an aggregate is never a source, and
// another agent's materialized inline prompt is not either.
func (r *Reconciler) resolvePromptSet(ctx context.Context, agent *agentv1alpha1.Agent) (promptSet, error) {
	return r.resolvePromptSetWith(ctx, agent, nil)
}

// resolvePromptSetWith is resolvePromptSet plus the prompts contributed by
// AgentBindings. They join the declared set before loading, so a binding prompt
// goes through the same existence, enabled and derived-prompt checks as one the
// agent names itself.
func (r *Reconciler) resolvePromptSetWith(
	ctx context.Context, agent *agentv1alpha1.Agent, extra []agentv1alpha1.Reference,
	extraSelectors ...*metav1.LabelSelector,
) (promptSet, error) {
	set := promptSet{}
	seen := make(map[string]bool)

	addDeclared := func(ref agentv1alpha1.Reference) {
		namespace := ref.Namespace
		if namespace == "" {
			namespace = agent.Namespace
		}
		key := namespace + "/" + ref.Name
		if seen[key] {
			return
		}
		seen[key] = true
		set.declared = append(set.declared, promptRef{Namespace: namespace, Name: ref.Name})
	}

	addSelector := func(labelSelector *metav1.LabelSelector) error {
		selector, err := metav1.LabelSelectorAsSelector(labelSelector)
		if err != nil {
			return err
		}
		list := &agentv1alpha1.PromptList{}
		if err := r.List(ctx, list, client.InNamespace(agent.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
			return err
		}
		names := make([]string, 0, len(list.Items))
		for i := range list.Items {
			prompt := &list.Items[i]
			if !prompt.Spec.Enabled {
				continue
			}
			if prompt.Name == agentv1alpha1.AggregatedPromptName(agent.Name) {
				continue
			}
			if prompt.Annotations[agentv1alpha1.AnnotationAggregatedFor] != "" {
				continue
			}
			if other, ok := prompt.Annotations[agentv1alpha1.AnnotationInlineFor]; ok && other != agent.Name {
				continue
			}
			names = append(names, prompt.Name)
		}
		sort.Strings(names)
		for _, name := range names {
			addDeclared(agentv1alpha1.Reference{Name: name})
		}
		return nil
	}

	if agent.Spec.PromptRef != nil {
		addDeclared(*agent.Spec.PromptRef)
	}
	for _, ref := range agent.Spec.Prompts {
		addDeclared(ref)
	}
	for _, ref := range extra {
		addDeclared(ref)
	}

	if agent.Spec.PromptSelector != nil {
		if err := addSelector(agent.Spec.PromptSelector); err != nil {
			return set, err
		}
	}
	for _, labelSelector := range extraSelectors {
		if labelSelector == nil {
			continue
		}
		if err := addSelector(labelSelector); err != nil {
			return set, err
		}
	}

	if len(set.declared) > maxPromptSources {
		set.tooMany = true
		return set, nil
	}

	for _, ref := range set.declared {
		prompt := &agentv1alpha1.Prompt{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, prompt); err != nil {
			if client.IgnoreNotFound(err) == nil {
				set.missing = append(set.missing, "Prompt/"+qualifiedPrompt(ref))
				continue
			}
			return set, err
		}
		// Whether the target admits this agent is the target's declaration, not
		// this agent's. A refused reference is reported like a missing one:
		// naming the namespace that would have worked turns the status into a map
		// of the other tenants.
		if prompt.Namespace != agent.Namespace &&
			len(grantWildcard(ctx, agent.Namespace, prompt.Namespace, "Prompt", ref.Name)) == 0 {
			set.missing = append(set.missing, "Prompt/"+qualifiedPrompt(ref)+" (namespace not allowed)")
			continue
		}
		switch {
		case !prompt.Spec.Enabled:
			set.missing = append(set.missing, "Prompt/"+ref.Name)
		case prompt.Annotations[agentv1alpha1.AnnotationAggregatedFor] != "" ||
			ref.Name == agentv1alpha1.AggregatedPromptName(agent.Name):
			set.missing = append(set.missing, "Prompt/"+ref.Name+" (aggregated prompt)")
		case prompt.Annotations[agentv1alpha1.AnnotationInlineFor] != "" &&
			prompt.Annotations[agentv1alpha1.AnnotationInlineFor] != agent.Name:
			set.missing = append(set.missing, "Prompt/"+ref.Name+" (inline prompt of another agent)")
		default:
			set.available = append(set.available, promptSource{
				Name:      ref.Name,
				Namespace: ref.Namespace,
				Template:  prompt.Spec.Template,
				Variables: append([]string(nil), prompt.Spec.Variables...),
			})
		}
	}
	return set, nil
}

func qualifiedPrompt(ref promptRef) string {
	if ref.Namespace == "" {
		return ref.Name
	}
	return ref.Namespace + "/" + ref.Name
}

func ownedByAgentName(prompt *agentv1alpha1.Prompt, agentName string) bool {
	for _, ref := range prompt.OwnerReferences {
		if ref.Controller != nil && *ref.Controller && ref.Kind == "Agent" && ref.Name == agentName {
			return true
		}
	}
	return false
}

// reclaimableInlinePrompt reports whether the object is the unclaimed Prompt the
// SQL->CRD migration left behind for agentName: it carries this agent's
// inline-for annotation and no owner at all, because a static manifest has no
// Agent UID to own it with.
//
// Anything with an owner is refused, not merely anything with a controller
// owner: the object belongs to someone else, and appending ourselves beside a
// non-controller owner would keep it alive after its agent is gone.
func reclaimableInlinePrompt(prompt *agentv1alpha1.Prompt, agentName string) bool {
	if prompt.DeletionTimestamp != nil || len(prompt.OwnerReferences) != 0 {
		return false
	}
	return prompt.Annotations[agentv1alpha1.AnnotationInlineFor] == agentName
}
