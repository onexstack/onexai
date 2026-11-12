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

package controlplanestore

import (
	"context"
	"log/slog"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	agentlisters "github.com/onexstack/onexai/pkg/generated/listers/agent/v1alpha1"

	"github.com/onexstack/onexai/internal/chat/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	agentvalidation "github.com/onexstack/onexai/pkg/apis/agent/validation"
	"github.com/onexstack/onexai/pkg/declclient"
)

// ClusterAgentBindingRulePrefix marks rules contributed by a ClusterAgentBinding,
// so the coverage report tells the two grant mechanisms apart.
const ClusterAgentBindingRulePrefix = "clusteragentbinding/"

// ClusterAgentBindingRuleStore projects cluster-wide bindings onto the rule
// shape the command resolver already understands.
//
// The resolver matches rules by agent name across namespaces, so the binding's
// namespace list has to become something the rule model can express: either a
// single-namespace Scope, or — for the wildcard — no scope at all. A binding
// that names several namespaces therefore projects as one rule per namespace,
// which is the same statement split into rows the resolver can evaluate.
type ClusterAgentBindingRuleStore struct {
	lister agentlisters.ClusterAgentBindingLister
}

// NewClusterAgentBindingRuleStore builds the projection over the control plane
// cache.
func NewClusterAgentBindingRuleStore(client *declclient.Client) *ClusterAgentBindingRuleStore {
	return &ClusterAgentBindingRuleStore{lister: client.ClusterAgentBindings()}
}

// ListEnabled returns the rules every enabled cluster binding contributes.
func (s *ClusterAgentBindingRuleStore) ListEnabled(_ context.Context) ([]*model.AgentCommandM, error) {
	if s.lister == nil {
		return nil, nil
	}
	items, err := s.lister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	rows := make([]*model.AgentCommandM, 0, len(items))
	for _, item := range items {
		rows = append(rows, clusterAgentBindingRulesFromCR(item)...)
	}
	return rows, nil
}

// clusterAgentBindingRulesFromCR maps one binding to its rule rows.
//
// A binding that grants no commands — neither named nor selected — is dropped
// rather than projected as an empty rule: an empty rule still matches agents and
// would make the coverage report claim a grant that is not there.
func clusterAgentBindingRulesFromCR(cr *agentv1alpha1.ClusterAgentBinding) []*model.AgentCommandM {
	if cr.Spec.Enabled != nil && !*cr.Spec.Enabled {
		return nil
	}

	commands := jsonList(cr.Spec.Commands)
	commandSelector := commandSelectorString(cr.Name, cr.Spec.CommandSelector)
	if len(cr.Spec.Commands) == 0 && commandSelector == "" {
		return nil
	}

	// The selector has three states, and each means something different:
	// absent (only the named agents), present but empty (every agent in the
	// scope), and present with terms (the agents it matches, unioned with the
	// named ones).
	allAgentsInScope := false
	selectsByLabel := false
	labelSelector := ""
	if cr.Spec.AgentSelector != nil {
		parsed, err := metav1.LabelSelectorAsSelector(cr.Spec.AgentSelector)
		switch {
		case err != nil:
			// Validation refuses an unparseable selector on write, so this is a
			// hand-written object. Fall back to the named agents rather than
			// treating the rule as covering everybody.
			slog.Warn("agent commands: a cluster binding has an unparseable selector and grants nothing by label",
				"binding", cr.Name, "error", err)
		case parsed.Empty():
			allAgentsInScope = true
		default:
			selectsByLabel = true
			labelSelector = parsed.String()
		}
	}

	// Named agents are grouped by the namespace they live in. The rule model
	// names agents without a namespace, so a named rule must carry that
	// namespace as its Scope — otherwise a same-named agent elsewhere would
	// inherit the grant.
	named := make(map[string][]string)
	if !allAgentsInScope {
		for _, ref := range cr.Spec.Agents {
			if ref.Name == "" || ref.Namespace == "" {
				continue
			}
			if !agentvalidation.NamespaceInList(cr.Spec.Namespaces, ref.Namespace) {
				continue
			}
			named[ref.Namespace] = append(named[ref.Namespace], ref.Name)
		}
	}

	newRule := func(scope string, agents []string, selector string) *model.AgentCommandM {
		name := ClusterAgentBindingRulePrefix + cr.Name
		if scope != "" {
			name += "/" + scope
		}
		row := &model.AgentCommandM{
			Name:            name,
			Status:          statusOf(true),
			Scope:           scope,
			Commands:        commands,
			CommandSelector: commandSelector,
		}
		switch {
		case selector != "":
			row.AgentSelector = selector
			if len(agents) > 0 {
				row.Agents = jsonList(agents)
			}
		case len(agents) > 0:
			row.Agents = jsonList(agents)
		default:
			// Neither a selector nor names: the audience is every agent in the
			// scope, which is the rule vocabulary's "*" entry.
			row.Agents = jsonList([]string{allAgentsSentinel})
		}
		return row
	}

	if agentvalidation.IsAllNamespaces(cr.Spec.Namespaces) {
		if allAgentsInScope {
			// One rule, no scope: every agent, in every namespace.
			return []*model.AgentCommandM{newRule("", nil, "")}
		}
		var rows []*model.AgentCommandM
		if selectsByLabel {
			// Labels are namespace-agnostic, so this rule needs no scope.
			rows = append(rows, newRule("", nil, labelSelector))
		}
		for _, namespace := range sortedKeys(named) {
			rows = append(rows, newRule(namespace, uniqueSorted(named[namespace]), ""))
		}
		return rows
	}

	var rows []*model.AgentCommandM
	for _, namespace := range uniqueSorted(cr.Spec.Namespaces) {
		switch {
		case allAgentsInScope:
			rows = append(rows, newRule(namespace, nil, ""))
		case selectsByLabel:
			rows = append(rows, newRule(namespace, uniqueSorted(named[namespace]), labelSelector))
		case len(named[namespace]) > 0:
			rows = append(rows, newRule(namespace, uniqueSorted(named[namespace]), ""))
		}
	}
	return rows
}

func sortedKeys(values map[string][]string) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
