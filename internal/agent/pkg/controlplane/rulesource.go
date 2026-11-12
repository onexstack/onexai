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

// Package controlplane adapts the declarative control plane (oai-declserver)
// to the interfaces the agent runtime already consumes.
//
// The adapter exists so the rest of the process does not change shape when the
// source of a resource moves from a SQL table to an API server: the OPA rule
// store keeps asking for []*model.AuthzRuleM, and only how those rows are
// produced changes.
package controlplane

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// Source implements opa.RuleSource over AuthorizationRule objects.
type Source struct {
	client  *declclient.Client
	changes chan struct{}
}

// NewSource builds a rule source over the client's informers and subscribes to
// AuthorizationRule changes. The informers must be started (Client.Start) before
// the first List.
func NewSource(client *declclient.Client) *Source {
	s := &Source{
		client: client,
		// Buffered and drained by a single reloader: a burst of events collapses
		// into one reload instead of queueing up behind the evaluator.
		changes: make(chan struct{}, 1),
	}

	handler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(any) { s.notify() },
		UpdateFunc: func(oldObj, newObj any) {
			// Not every update is a change. An informer redelivers every object
			// on each resync, and the agent objects are rewritten continuously by
			// the health reporter; notifying on those turned the reloader into a
			// loop that re-listed the whole rule set (hundreds of rows) and
			// re-prepared the policy several times a second. Only an update that
			// can change what List returns is a change.
			if changeAffectsLoad(oldObj, newObj) {
				s.notify()
			}
		},
		DeleteFunc: func(any) { s.notify() },
	}
	_, _ = client.Factory.Agent().V1alpha1().AuthorizationRules().Informer().AddEventHandler(handler)
	// Agent events matter too: a selector is resolved against the agent set at
	// load time, so an agent that appears, is relabelled or is deleted changes
	// which rules apply. Without this the rule that selected it would keep the
	// resolution it computed while the agent was still missing.
	_, _ = client.Factory.Agent().V1alpha1().Agents().Informer().AddEventHandler(handler)
	return s
}

// Changes reports that the rule set may have changed.
func (s *Source) Changes() <-chan struct{} { return s.changes }

func (s *Source) notify() {
	select {
	case s.changes <- struct{}{}:
	default:
	}
}

// changeAffectsLoad reports whether an update can change what List returns.
//
// The two object kinds this source subscribes to contribute different things: a
// rule contributes its spec (that is what expand turns into rows), an agent
// contributes its name and labels (that is what a selector resolves against).
// Everything else on either object — status, resourceVersion, managed fields —
// is written by other controllers and read by nobody here, so an update that
// touches only those is not worth a reload.
//
// An unrecognised kind is reported as a change: this is a filter over an
// optimisation, and the safe direction for a filter is to let work through.
func changeAffectsLoad(oldObj, newObj any) bool {
	switch old := oldObj.(type) {
	case *agentv1alpha1.AuthorizationRule:
		current, ok := newObj.(*agentv1alpha1.AuthorizationRule)
		if !ok {
			return true
		}
		return !reflect.DeepEqual(old.Spec, current.Spec)

	case *agentv1alpha1.Agent:
		current, ok := newObj.(*agentv1alpha1.Agent)
		if !ok {
			return true
		}
		if old.Name != current.Name || old.Namespace != current.Namespace {
			return true
		}
		return !reflect.DeepEqual(old.Labels, current.Labels)
	}
	return true
}

// List implements opa.RuleSource. The rule store calls it on every Sync, which
// is driven by Changes rather than by a timer.
func (s *Source) List(_ context.Context, _ *where.Options) (int64, []*model.AuthzRuleM, error) {
	rules, err := s.client.AuthorizationRules().List(labels.Everything())
	if err != nil {
		return 0, nil, fmt.Errorf("controlplane: list authorization rules: %w", err)
	}

	out := make([]*model.AuthzRuleM, 0, len(rules))
	for _, rule := range rules {
		agentNames, ok := s.resolveAgentNames(rule)
		if !ok {
			// A selector that resolves to no agent means the rule addresses
			// nobody. Dropping it is what keeps it from becoming an unscoped
			// rule, which the policy reads as "every agent".
			continue
		}
		out = append(out, expand(rule, agentNames)...)
	}
	return int64(len(out)), out, nil
}

// resolveAgentNames returns the agent names the rule addresses, and whether the
// rule should be kept at all.
func (s *Source) resolveAgentNames(rule *agentv1alpha1.AuthorizationRule) ([]string, bool) {
	names := append([]string(nil), rule.Spec.AgentNames...)

	if rule.Spec.AgentSelector == nil {
		// No selector: the names list is the whole scope, and an empty one is
		// the historical "not agent-scoped" meaning.
		return names, true
	}

	selector, err := metav1.LabelSelectorAsSelector(rule.Spec.AgentSelector)
	if err != nil {
		// The API server validates selectors on write, so this can only be a
		// rule that predates that validation. Dropping it is fail-safe.
		return nil, false
	}

	agents, err := s.client.Agents().List(selector)
	if err != nil {
		return nil, false
	}
	for _, agent := range agents {
		names = append(names, agent.Name)
	}
	return names, len(names) > 0
}

// expand turns one rule into the row shape the Rego policy expects: one row per
// (subject kind, resource kind), because the policy matches a single
// subject_type and a single resource_kind per row.
func expand(rule *agentv1alpha1.AuthorizationRule, agentNames []string) []*model.AuthzRuleM {
	verbs := mapVerbs(rule.Spec.Verbs)
	resources := ruleResources(rule)
	if len(verbs) == 0 || len(resources) == 0 {
		return nil
	}

	attributes := ""
	if rule.Spec.Attributes != nil {
		attributes = string(rule.Spec.Attributes.Raw)
	}

	base := model.AuthzRuleM{
		ResourceAttributes: attributes,
		AgentNames:         model.StringList(agentNames),
		Verbs:              verbs,
		Effect:             strings.ToLower(string(rule.Spec.Effect)),
		Description:        rule.Spec.Description,
	}
	if !rule.Spec.Enabled {
		base.Status = model.StatusDisabled
	}

	var out []*model.AuthzRuleM

	// appendRows emits one row per resource for one subject group. A rule that
	// names several resources must produce a row per target: each row is what
	// the policy matches, and folding them into one row would make the last
	// resource win.
	appendRows := func(name string, subjectType string, ids model.StringList) {
		for i, resource := range resources {
			row := base
			row.ResourceKind = resource.Kind
			row.ResourceName = model.StringList(resource.Names)
			row.SubjectType = subjectType
			row.SubjectID = ids
			row.Name = name
			if len(resources) > 1 {
				row.Name = fmt.Sprintf("%s/%s/%d", name, resource.Kind, i)
			}
			out = append(out, &row)
		}
	}

	byKind := make(map[string][]string, len(rule.Spec.Subjects))
	for _, subject := range rule.Spec.Subjects {
		kind := strings.ToLower(subject.Kind)
		byKind[kind] = append(byKind[kind], subject.Name)
	}
	// The kinds are walked in sorted order, not in map order. The rows this
	// produces are compared generation against generation to decide whether the
	// rule set changed, and a map walk would hand back a different order on every
	// load: the comparison would report a change every time, so every reload
	// would re-prepare the policy and drop the decision cache for nothing.
	kinds := make([]string, 0, len(byKind))
	for kind := range byKind {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		// One row per subject kind: the policy matches subject_type exactly, so
		// a rule naming a user and a group is two rows, not one row with two
		// kinds.
		appendRows(rule.Name+"/"+kind, kind, model.StringList(byKind[kind]))
	}

	if rule.Spec.SubjectNamespace != "" {
		// The workspace -> group -> user inheritance the policy already knows.
		appendRows(rule.Name+"/namespace", "namespace", model.StringList{rule.Spec.SubjectNamespace})
	}

	return out
}

// ruleResources returns the rule's effective resource list. The plural shape is
// authoritative when present; the singular field is the shape every rule
// written before the list existed uses.
func ruleResources(rule *agentv1alpha1.AuthorizationRule) []agentv1alpha1.AuthorizationResource {
	if len(rule.Spec.Resources) > 0 {
		out := make([]agentv1alpha1.AuthorizationResource, 0, len(rule.Spec.Resources))
		for _, resource := range rule.Spec.Resources {
			if resource.Kind != "" && len(resource.Names) > 0 {
				out = append(out, resource)
			}
		}
		return out
	}
	if rule.Spec.Resource.Kind != "" && len(rule.Spec.Resource.Names) > 0 {
		return []agentv1alpha1.AuthorizationResource{rule.Spec.Resource}
	}
	return nil
}

// mapVerbs translates the Kubernetes verbs exposed by the API into the policy's
// read/write vocabulary. "use" and "*" pass through: they are the policy's own
// verbs, not translations of a Kubernetes one.
func mapVerbs(in []string) model.StringList {
	seen := make(map[string]struct{}, len(in))
	out := make(model.StringList, 0, len(in))

	add := func(verb string) {
		if _, ok := seen[verb]; ok {
			return
		}
		seen[verb] = struct{}{}
		out = append(out, verb)
	}

	for _, verb := range in {
		switch verb {
		case "get", "list", "watch":
			add("read")
		case "create", "update", "patch", "delete":
			add("write")
		default:
			add(verb)
		}
	}
	return out
}
