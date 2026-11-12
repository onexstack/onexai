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

package agentcmd

import (
	"context"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/chat/model"
)

// Preview is what a candidate rule would do if it were stored.
//
// It exists because a rule is a query, and a query nobody can run is a query
// nobody trusts: "which agents does this cover" and "what do they get" are the
// two questions an operator has after writing one, and answering them by saving
// it and watching is how a typo becomes an incident.
type Preview struct {
	// Agents are the names the rule covers, in the order the agent service
	// listed them.
	Agents []string
	// Commands are the catalog commands the rule grants.
	Commands []string
	// NamedSkills are the skills the rule names outright. A rule that reaches
	// skills through a selector reports them under Agents rather than here:
	// which skills an agent has is a question only the agent service can
	// answer, and the preview runs on the stored agent list alone.
	NamedSkills []string
	// AgentFields is every listed agent's field set, again for the whole list.
	// The two spaces are reported side by side because "why is this agent not
	// in Agents" is answered by whichever of them the rule reads — and by
	// seeing that the other one was never consulted.
	AgentFields map[string]fields.Set
	// AgentLabels is every listed agent's label set, not only the covered ones.
	// The question an operator actually has is usually about an agent that is
	// missing from Agents, and "these are the labels it was evaluated against"
	// is the half of the answer the report cannot give if it only lists the
	// matches.
	AgentLabels map[string]labels.Set
}

// Covers reports whether the rule would apply to anyone.
func (p *Preview) Covers() bool { return len(p.Agents) > 0 }

// Grants reports whether the rule would give those agents anything.
func (p *Preview) Grants() bool { return len(p.Commands) > 0 || len(p.NamedSkills) > 0 }

// Preview reports what one candidate rule would do, without storing it.
//
// It reads the same snapshots Resolve does and runs the same matching, so a
// preview cannot disagree with the rule it describes — the failure a separate
// "just for the admin UI" implementation would guarantee.
func (r *Resolver) Preview(ctx context.Context, m *model.AgentCommandM) (*Preview, error) {
	rule, err := compileRule(m)
	if err != nil {
		return nil, err
	}

	snap, err := r.agentSnapshotRead(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the agents: %w", err)
	}

	preview := &Preview{
		AgentLabels: make(map[string]labels.Set),
		AgentFields: make(map[string]fields.Set),
	}

	for _, name := range snap.names {
		// The labels are reported for every agent, not only the covered ones:
		// the agent an operator is asking about is usually the one missing from
		// Agents.
		if ls, ok := snap.set[name]; ok {
			preview.AgentLabels[name] = ls
		}
		if fs, ok := snap.fields[name]; ok {
			preview.AgentFields[name] = fs
		}

		if rule.covers([]string{name}, snap) {
			preview.Agents = append(preview.Agents, name)
		}
	}

	preview.Commands = sortedKeys(rule.commands)
	preview.NamedSkills = sortedKeys(rule.namedSkills)

	return preview, nil
}

// ValidateFieldSelector parses a stored field selector, treating an empty one
// as absent.
//
// It is the field-space twin of ValidateSelector, and it exists for the reason
// the field space does: a field name is a closed set, so a caller can check the
// names it uses — which is the check the label side cannot make.
func ValidateFieldSelector(selector string) (fields.Selector, error) {
	parsed, err := parseFieldSelector(selector)
	if err != nil {
		return nil, fmt.Errorf("not a field selector: %w", err)
	}
	return parsed, nil
}

// ValidateSelector parses a stored selector and reports what is wrong with it
// in the operator's terms.
//
// It is exported so the write path can refuse a rule with the same parser the
// read path uses. The empty string is accepted and means "absent": the
// alternative — labels.Parse("") — succeeds and matches everything, which would
// turn a half-filled rule into a global one.
func ValidateSelector(selector string) (labels.Selector, error) {
	parsed, err := parseSelector(selector)
	if err != nil {
		return nil, fmt.Errorf("not a label selector: %w", err)
	}
	return parsed, nil
}

// sortedKeys returns a set's keys in order.
func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
