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

package controlplanestore

import (
	"context"
	"log/slog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	agentlisters "github.com/onexstack/onexai/pkg/generated/listers/agent/v1alpha1"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/pkg/agentlabel"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// AgentBindingRulePrefix marks rules contributed by an AgentBinding, so an
// operator reading coverage can tell which mechanism granted a command.
const AgentBindingRulePrefix = "agentbinding/"

// AgentBindingRuleStore adapts AgentBindings to the rule shape the command
// resolver already understands.
//
// It is a projection, not a second implementation: the binding's agent selector
// becomes the rule's agent selector, its commands become the rule's commands,
// and compileRule/covers/accumulate are reused unchanged. Adding a second
// binding is therefore a plain union with no ordering rules.
type AgentBindingRuleStore struct {
	lister agentlisters.AgentBindingLister
}

// NewAgentBindingRuleStore builds the projection over the control plane cache.
func NewAgentBindingRuleStore(client *declclient.Client) *AgentBindingRuleStore {
	return &AgentBindingRuleStore{lister: client.AgentBindings()}
}

// ListEnabled returns one rule per enabled binding.
func (s *AgentBindingRuleStore) ListEnabled(_ context.Context) ([]*model.AgentCommandM, error) {
	if s.lister == nil {
		return nil, nil
	}
	items, err := s.lister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	rows := make([]*model.AgentCommandM, 0, len(items))
	for _, item := range items {
		row := agentBindingRuleFromCR(item)
		if row.Enabled() {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// namespaceOf is the namespace a resource belongs to. It mirrors what the agent
// snapshot does with an empty namespace, so the two sides cannot disagree about
// which namespace a resource belongs to.
func namespaceOf(namespace string) string {
	if namespace == "" {
		return agentlabel.DefaultNamespace
	}
	return namespace
}

// agentBindingRuleFromCR maps a binding to a rule row.
func agentBindingRuleFromCR(cr *agentv1alpha1.AgentBinding) *model.AgentCommandM {
	enabled := cr.Spec.Enabled == nil || *cr.Spec.Enabled

	row := &model.AgentCommandM{
		Name:            AgentBindingRulePrefix + cr.Name,
		Status:          statusOf(enabled),
		Commands:        jsonList(cr.Spec.Commands),
		CommandSelector: commandSelectorString(cr.Name, cr.Spec.CommandSelector),
		// A binding is namespace-scoped, while the resolver matches agents by
		// name across namespaces. Scope is what makes the two statements the
		// same one: a binding in "ops" grants only to agents in "ops".
		Scope: namespaceOf(cr.Namespace),
	}

	// The selector and the named agents are a union, exactly as they are in the
	// controller. An empty selector already covers every agent in the
	// namespace, so it wins outright: naming agents alongside it cannot narrow
	// the result.
	if cr.Spec.Selector != nil {
		selector, err := metav1.LabelSelectorAsSelector(cr.Spec.Selector)
		if err != nil {
			// Validation refuses an unparseable selector on write, so this is a
			// hand-written object. Report it and fall back to the named agents.
			slog.Warn("agent commands: a binding has an unparseable selector and grants nothing by label",
				"binding", cr.Name, "error", err)
		} else if selector.Empty() {
			row.Agents = jsonList([]string{allAgentsSentinel})
		} else {
			row.AgentSelector = selector.String()
			if len(cr.Spec.Agents) > 0 {
				row.Agents = jsonList(cr.Spec.Agents)
			}
		}
	} else if len(cr.Spec.Agents) > 0 {
		row.Agents = jsonList(cr.Spec.Agents)
	}
	return row
}

// commandSelectorString projects the CRD's structured selector onto the rule
// model's string form, which compileRule expands against the compiled catalogue.
//
// A selector that cannot be parsed is degraded to "no selector" rather than
// passed through: compileRule rejects the whole rule on a bad selector, which
// would take the binding's explicitly named commands down with it. An empty
// selector degrades the same way — Everything() would grant the whole catalogue.
func commandSelectorString(owner string, selector *metav1.LabelSelector) string {
	if selector == nil {
		return ""
	}
	parsed, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		slog.Warn("agent commands: a binding has an unparseable command selector; it grants nothing by label",
			"binding", owner, "error", err)
		return ""
	}
	if parsed.Empty() {
		slog.Warn("agent commands: a binding has an empty command selector; it grants nothing by label",
			"binding", owner)
		return ""
	}
	return parsed.String()
}
