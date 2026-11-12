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

package agentbinding

import (
	"context"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/onexstack/onexai/internal/controller/agent/grant"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
)

// Contribution is what one binding resolves to. It is exported so the agent
// controller can fall back to it when a binding's status is stale, keeping the
// two controllers on one implementation of the same rule.
type Contribution struct {
	Prompts        []string
	Skills         []string
	MCPs           []string
	KnowledgeBases []string
	CustomCommands []string
	Commands       []string
	Model          *agentv1alpha1.Reference
	Missing        []string
}

// Compute resolves the binding's explicit references.
//
// Only explicit references are resolved here. The binding has no per-kind
// selectors: a selector on the binding selects agents, not resources. That is
// what keeps the merge set-independent and the conflict story trivial.
func Compute(ctx context.Context, c client.Client, binding *agentv1alpha1.AgentBinding) (Contribution, error) {
	var out Contribution

	index, err := grant.Index(ctx, c)
	if err != nil {
		return out, err
	}

	collect := func(
		kind string,
		refs []agentv1alpha1.Reference,
		newObject func() client.Object,
		target *[]string,
	) error {
		for _, ref := range refs {
			namespace := ref.Namespace
			if namespace == "" {
				namespace = binding.Namespace
			}
			obj := newObject()
			ok, err := load(ctx, c, namespace, ref.Name, obj)
			if err != nil {
				return err
			}
			if !ok {
				out.Missing = append(out.Missing, kind+"/"+namespace+"/"+ref.Name)
				continue
			}
			// The target decides who may use it. A binding that reaches across a
			// namespace boundary is admitted by a ReferenceGrant in the target's
			// namespace — never by the binding's intent alone.
			if !grant.Allows(index, binding.Namespace, namespace, kind, ref.Name) {
				out.Missing = append(out.Missing, kind+"/"+namespace+"/"+ref.Name+" (namespace not allowed)")
				continue
			}
			// A reference in the binding's own namespace stays bare, which is the
			// spelling every existing consumer already resolves.
			if namespace == binding.Namespace {
				*target = append(*target, ref.Name)
			} else {
				*target = append(*target, namespace+"/"+ref.Name)
			}
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
	if err := resolveCommands(ctx, c, binding, &out); err != nil {
		return out, err
	}
	if binding.Spec.ModelRef != nil {
		ref := *binding.Spec.ModelRef
		namespace := ref.Namespace
		if namespace == "" {
			namespace = binding.Namespace
		}
		ok, err := load(ctx, c, namespace, ref.Name, &agentv1alpha1.Model{})
		if err != nil {
			return out, err
		}
		switch {
		case !ok:
			out.Missing = append(out.Missing, "Model/"+namespace+"/"+ref.Name)
		case !grant.Allows(index, binding.Namespace, namespace, sharing.KindModel, ref.Name):
			out.Missing = append(out.Missing, "Model/"+namespace+"/"+ref.Name+" (namespace not allowed)")
		default:
			out.Model = &ref
		}
	}

	out.sort()
	return out, nil
}

// resolveCommands checks the named commands against the Command catalogue and
// expands the command selector against the same catalogue.
//
// A command body is compiled into the chat server; the Command object is its
// catalogue entry, reconciled by chat at start-up. Checking here is what turns
// "this binding grants a command the binary no longer implements" from a silent
// no-op into status.missing.
//
// A "skill:<name>" entry is not a Command object: it is resolved per agent at
// run time (the skill has to be exposed as a command on that agent), so it is
// carried through unchecked, exactly as the resolver does.
//
// The selector is the batch axis: it matches the Command objects' own labels,
// which is how "group in (duty)" follows a command added to that group later.
// The result is a set — a name that is both listed and selected is granted once.
func resolveCommands(ctx context.Context, c client.Client, binding *agentv1alpha1.AgentBinding, out *Contribution) error {
	if len(binding.Spec.Commands) == 0 && binding.Spec.CommandSelector == nil {
		return nil
	}

	list := &chatv1alpha1.CommandList{}
	if err := c.List(ctx, list, client.InNamespace(binding.Namespace)); err != nil {
		return err
	}
	known := make(map[string]bool, len(list.Items))
	for i := range list.Items {
		known[list.Items[i].Name] = list.Items[i].Spec.Enabled
	}

	granted := make(map[string]struct{}, len(list.Items))
	for _, name := range binding.Spec.Commands {
		if strings.HasPrefix(name, skillCommandPrefix) {
			granted[name] = struct{}{}
			continue
		}
		if known[name] {
			granted[name] = struct{}{}
			continue
		}
		out.Missing = append(out.Missing, "Command/"+binding.Namespace+"/"+name)
	}

	if selectorSpec := binding.Spec.CommandSelector; selectorSpec != nil {
		selector, err := metav1.LabelSelectorAsSelector(selectorSpec)
		switch {
		case err != nil:
			// Validation refuses this on write, so reaching here means the object
			// was written by something else. Granting nothing is the only safe
			// reading, and the report names the selector that needs fixing.
			out.Missing = append(out.Missing, "CommandSelector/"+binding.Namespace+"/"+selectorSpec.String())
		case selector.Empty():
			// An empty selector is refused on write. If one arrives anyway,
			// treat it as granting nothing: Everything() would hand over the
			// whole catalogue.
		default:
			for i := range list.Items {
				item := &list.Items[i]
				if !item.Spec.Enabled {
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

// sortedSet turns a set into a sorted slice. The union is order-independent, so
// the stored status should be too.
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

// skillCommandPrefix marks a grant entry that names a skill rather than a
// compiled command. It mirrors the chat resolver's spelling.
const skillCommandPrefix = "skill:"

// MatchAgents counts the enabled agents the binding selects.
func MatchAgents(ctx context.Context, c client.Client, binding *agentv1alpha1.AgentBinding) (int32, error) {
	list := &agentv1alpha1.AgentList{}
	if err := c.List(ctx, list, client.InNamespace(binding.Namespace)); err != nil {
		return 0, err
	}

	selector, err := selectorOf(binding)
	if err != nil {
		return 0, err
	}

	var matched int32
	for i := range list.Items {
		agent := &list.Items[i]
		if !agent.Spec.Enabled {
			continue
		}
		if named(binding, agent.Name) || (selector != nil && selector.Matches(labels.Set(agent.Labels))) {
			matched++
		}
	}
	return matched, nil
}

// SelectorMatchesAll reports whether the binding's selector was written as an
// explicit empty object.
func SelectorMatchesAll(binding *agentv1alpha1.AgentBinding) bool {
	selector, err := selectorOf(binding)
	return err == nil && selector != nil && selector.Empty()
}

func selectorOf(binding *agentv1alpha1.AgentBinding) (labels.Selector, error) {
	if binding.Spec.Selector == nil {
		return nil, nil
	}
	return metav1.LabelSelectorAsSelector(binding.Spec.Selector)
}

func named(binding *agentv1alpha1.AgentBinding, name string) bool {
	for _, candidate := range binding.Spec.Agents {
		if candidate == name {
			return true
		}
	}
	return false
}

// load reads the object into obj, reporting whether it is present and enabled.
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
	default:
		// Fail closed: an unknown kind contributes nothing rather than being
		// treated as reachable.
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
