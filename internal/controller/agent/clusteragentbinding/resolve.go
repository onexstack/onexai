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

package clusteragentbinding

import (
	"context"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	agentvalidation "github.com/onexstack/onexai/pkg/apis/agent/validation"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
)

// Contribution is what one cluster binding resolves to. Entries are canonical
// "namespace/name".
type Contribution struct {
	Prompts        []string
	Skills         []string
	MCPs           []string
	KnowledgeBases []string
	CustomCommands []string
	Commands       []string
	Memory         *agentv1alpha1.Reference
	Model          *agentv1alpha1.Reference
	Missing        []string
}

// Compute resolves the binding's explicit references.
//
// Only existence and the enabled flag are decided here. Whether a target admits
// a given agent depends on the agent's namespace, so it is enforced when the
// agent merges the contribution and again at run time — deciding it once for
// the whole binding would have to pick one of several answers.
func Compute(ctx context.Context, c client.Client, binding *agentv1alpha1.ClusterAgentBinding) (Contribution, error) {
	var out Contribution

	collect := func(
		kind string,
		refs []agentv1alpha1.Reference,
		newObject func() client.Object,
		target *[]string,
	) error {
		for _, ref := range refs {
			// A cluster-scoped object has no namespace to default to. Validation
			// refuses one, so a stored object without a namespace is reported
			// rather than resolved against a guess.
			if ref.Namespace == "" {
				out.Missing = append(out.Missing, kind+"/"+ref.Name+" (namespace missing)")
				continue
			}
			obj := newObject()
			ok, err := load(ctx, c, ref.Namespace, ref.Name, obj)
			if err != nil {
				return err
			}
			if !ok {
				out.Missing = append(out.Missing, kind+"/"+ref.Namespace+"/"+ref.Name)
				continue
			}
			*target = append(*target, ref.Namespace+"/"+ref.Name)
		}
		return nil
	}

	if err := collect("Prompt", binding.Spec.Prompts,
		func() client.Object { return &agentv1alpha1.Prompt{} }, &out.Prompts); err != nil {
		return out, err
	}
	if err := collect("Skill", binding.Spec.Skills,
		func() client.Object { return &agentv1alpha1.Skill{} }, &out.Skills); err != nil {
		return out, err
	}
	if err := collect("KnowledgeBase", binding.Spec.KnowledgeBases,
		func() client.Object { return &agentv1alpha1.KnowledgeBase{} }, &out.KnowledgeBases); err != nil {
		return out, err
	}
	if err := collect("MCPServer", binding.Spec.MCPServers,
		func() client.Object { return &agentv1alpha1.MCPServer{} }, &out.MCPs); err != nil {
		return out, err
	}
	if err := collect("CustomCommand", binding.Spec.CustomCommands,
		func() client.Object { return &agentv1alpha1.CustomCommand{} }, &out.CustomCommands); err != nil {
		return out, err
	}
	if binding.Spec.MemoryRef != nil {
		if binding.Spec.MemoryRef.Namespace == "" {
			out.Missing = append(out.Missing, "Memory/"+binding.Spec.MemoryRef.Name+" (namespace missing)")
		} else if ok, err := load(ctx, c, binding.Spec.MemoryRef.Namespace, binding.Spec.MemoryRef.Name,
			&agentv1alpha1.Memory{}); err != nil {
			return out, err
		} else if ok {
			out.Memory = binding.Spec.MemoryRef.DeepCopy()
		} else {
			out.Missing = append(out.Missing, "Memory/"+binding.Spec.MemoryRef.Namespace+"/"+binding.Spec.MemoryRef.Name)
		}
	}
	if binding.Spec.ModelRef != nil {
		ref := binding.Spec.ModelRef
		if ref.Namespace == "" {
			out.Missing = append(out.Missing, "Model/"+ref.Name+" (namespace missing)")
		} else if ok, err := load(ctx, c, ref.Namespace, ref.Name,
			&agentv1alpha1.Model{}); err != nil {
			return out, err
		} else if ok {
			out.Model = ref.DeepCopy()
		} else {
			out.Missing = append(out.Missing, "Model/"+ref.Namespace+"/"+ref.Name)
		}
	}
	if err := resolveCommands(ctx, c, binding, &out); err != nil {
		return out, err
	}

	out.sort()
	return out, nil
}

// resolveCommands merges the explicitly named commands with everything the
// binding's selector matches.
//
// The catalogue is published into every namespace that holds an agent or a
// binding, so the same entry appears many times. The listing is cluster-wide and
// filtered through the binding's own namespace list: namespaces are the audience
// boundary, exactly as they are for agents, so a wildcard follows the whole
// cluster and an explicit list cannot reach a namespace it never named.
//
// The result is a set: a command that is both named and selected is granted
// once, and a selector that matches nothing grants nothing.
func resolveCommands(ctx context.Context, c client.Client, binding *agentv1alpha1.ClusterAgentBinding, out *Contribution) error {
	granted := make(map[string]struct{}, len(binding.Spec.Commands))
	for _, name := range binding.Spec.Commands {
		granted[name] = struct{}{}
	}

	if selectorSpec := binding.Spec.CommandSelector; selectorSpec != nil {
		selector, err := metav1.LabelSelectorAsSelector(selectorSpec)
		switch {
		case err != nil:
			// Validation refuses this on write. Granting nothing is the only safe
			// reading of a selector nobody can parse, and the report names it.
			out.Missing = append(out.Missing, "CommandSelector/"+selectorSpec.String())
		case selector.Empty():
			// An empty selector is refused on write. If one arrives anyway,
			// treat it as granting nothing: Everything() would hand over the
			// whole catalogue.
		default:
			list := &chatv1alpha1.CommandList{}
			if err := c.List(ctx, list); err != nil {
				return err
			}
			for i := range list.Items {
				item := &list.Items[i]
				if !item.Spec.Enabled {
					continue
				}
				if !agentvalidation.NamespaceInList(binding.Spec.Namespaces, item.Namespace) {
					continue
				}
				if selector.Matches(labels.Set(item.Labels)) {
					granted[item.Name] = struct{}{}
				}
			}
		}
	}

	out.Commands = append(out.Commands, sortedSet(granted)...)
	return nil
}

// sortedSet turns a set into a sorted slice, so the stored status does not
// depend on map iteration order.
func sortedSet(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// MatchesAgent reports whether the binding's audience includes agent.
//
// The namespace list is checked first: it is the boundary the object exists to
// state. Only inside it does the agent selector apply — so a label cannot drag
// an agent from a namespace the binding never named into the audience.
func MatchesAgent(binding *agentv1alpha1.ClusterAgentBinding, agent *agentv1alpha1.Agent) bool {
	if binding.Spec.Enabled != nil && !*binding.Spec.Enabled {
		return false
	}
	if !agentvalidation.NamespaceInList(binding.Spec.Namespaces, agent.Namespace) {
		return false
	}
	for _, ref := range binding.Spec.Agents {
		if ref.Namespace == agent.Namespace && ref.Name == agent.Name {
			return true
		}
	}
	if binding.Spec.AgentSelector == nil {
		return false
	}
	selector, err := metav1.LabelSelectorAsSelector(binding.Spec.AgentSelector)
	if err != nil {
		return false
	}
	return selector.Matches(labels.Set(agent.Labels))
}

// MatchAgents counts the enabled agents the binding selects.
func MatchAgents(ctx context.Context, c client.Client, binding *agentv1alpha1.ClusterAgentBinding) (int32, error) {
	list := &agentv1alpha1.AgentList{}
	if err := c.List(ctx, list); err != nil {
		return 0, err
	}
	var matched int32
	for i := range list.Items {
		agent := &list.Items[i]
		if !agent.Spec.Enabled {
			continue
		}
		// MatchesAgent checks the namespace list first, so the wildcard is
		// handled there rather than in a second copy of the same rule.
		if MatchesAgent(binding, agent) {
			matched++
		}
	}
	return matched, nil
}

func load(ctx context.Context, c client.Client, namespace, name string, obj client.Object) (bool, error) {
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return enabled(obj), nil
}

func enabled(obj client.Object) bool {
	switch o := obj.(type) {
	case *agentv1alpha1.Skill:
		return o.Spec.Enabled
	case *agentv1alpha1.MCPServer:
		return o.Spec.Enabled
	case *agentv1alpha1.KnowledgeBase:
		return o.Spec.Enabled
	case *agentv1alpha1.Prompt:
		return o.Spec.Enabled
	case *agentv1alpha1.CustomCommand:
		return o.Spec.Enabled
	case *agentv1alpha1.Memory:
		return o.Spec.Enabled
	default:
		return false
	}
}

func (c *Contribution) sort() {
	sort.Strings(c.Prompts)
	sort.Strings(c.Skills)
	sort.Strings(c.MCPs)
	sort.Strings(c.KnowledgeBases)
	sort.Strings(c.CustomCommands)
	sort.Strings(c.Commands)
	sort.Strings(c.Missing)
}
