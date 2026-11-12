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
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/command"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/pkg/agentlabel"
)

const (
	// skillCommandPrefix marks an entry in a rule's commands list that names a
	// skill rather than a catalog command: "skill:k8s-troubleshoot".
	skillCommandPrefix = "skill:"
	// SkillProviderLabel is the label a skill command carries the repository
	// kind it came from in. It mirrors nothing in the catalog: a skill is not
	// catalogued, so its labels are built where the skill is turned into a
	// command.
	SkillProviderLabel = agentlabel.Prefix + "provider"
	// allAgentsToken is the explicit "every agent" entry in a rule's agents
	// list. It is spelled out rather than left to an empty list because an
	// empty list is what a half-filled rule looks like.
	allAgentsToken = "*"
)

// compiledRule is one stored rule with everything that does not depend on the
// agent already worked out.
type compiledRule struct {
	// name is the stored rule's name. The matching does not need it — a rule
	// that covers nobody is indistinguishable from the next one — but the
	// admin report does: "no rule covers this agent" and "these three do" are
	// the answers an operator is looking for, and one of them is a list.
	name string
	// agentSelector is nil when the rule selects no agents by label, which is
	// not the same as selecting all of them: labels.Parse("") yields a selector
	// that matches everything, so an empty column turned into a selector would
	// silently make every rule global.
	agentSelector labels.Selector
	// agentFieldSelector is the same idea over the agent's own columns rather
	// than its tags — the second of Kubernetes' two selection spaces. A label
	// can never name metadata.name and a field can never name a label, so the
	// two cannot collide and neither has to reserve a name from the other.
	agentFieldSelector fields.Selector
	// agents are the agents named outright, and allAgents is the "*" entry.
	agents    map[string]struct{}
	allAgents bool
	// scope, when set, is one more condition ANDed with everything above: the
	// rule applies only to agents in that namespace. The stored rules leave it
	// empty; the AgentBinding projection sets it to the binding's namespace,
	// which is what keeps a namespaced grant from reaching another namespace's
	// same-named agent.
	scope string

	// commandSelector is kept beside commands because a skill command is not in
	// the catalog: which of them a rule grants can only be answered per agent,
	// against the skill's own labels.
	commandSelector labels.Selector
	// commands are the catalog command names the rule grants. They depend only
	// on the catalog, so they are computed once per rebuild rather than once
	// per message — the cost the snapshot exists to avoid.
	commands map[string]struct{}
	// namedSkills are the skills the rule names outright.
	namedSkills map[string]struct{}
	// orphans are the names the rule grants that the catalog does not have: a
	// command renamed or removed after the rule was written. They grant nothing
	// (the loop in compileRule drops them), and they are carried rather than
	// only dropped because the name is the whole fix — whoever reads the report
	// has to be told which string to change. compileRules in the resolver is
	// what reads them, and logging is what it does with them; they are kept in
	// the order the rule names them, so the line reads like the row it came
	// from.
	//
	// Carrying them is also what keeps the reporting honest about where this
	// can happen at all. A rule cannot be *written* with a name the catalog does
	// not have: the write path refuses it (see biz/v1/agentcommand's
	// validateCommandNames), so the API is never the source. The source is a
	// deploy — a command renamed in code while stored rules still name it —
	// which is exactly the case nobody is watching for, and the reason this
	// belongs in the read path's own report rather than in a write-path check.
	orphans []string
}

// compileRule turns a stored rule into its compiled form.
//
// It returns an error for a selector that does not parse. The write path
// refuses those, so reaching here means the row was written by something else —
// a migration, an operator at a SQL prompt — and the rule is dropped rather
// than guessed at: a selector nobody can read selects nothing.
func compileRule(m *model.AgentCommandM) (compiledRule, error) {
	var rule compiledRule

	rule.name = m.Name
	rule.scope = m.Scope

	agentSelector, err := parseSelector(m.AgentSelector)
	if err != nil {
		return compiledRule{}, fmt.Errorf("rule %q: agent selector: %w", m.Name, err)
	}
	rule.agentSelector = agentSelector

	for _, name := range m.AgentList() {
		if name == allAgentsToken {
			rule.allAgents = true
			continue
		}
		if rule.agents == nil {
			rule.agents = make(map[string]struct{})
		}
		rule.agents[name] = struct{}{}
	}

	agentFieldSelector, err := parseFieldSelector(m.AgentFieldSelector)
	if err != nil {
		return compiledRule{}, fmt.Errorf("rule %q: agent field selector: %w", m.Name, err)
	}
	rule.agentFieldSelector = agentFieldSelector

	commandSelector, err := parseSelector(m.CommandSelector)
	if err != nil {
		return compiledRule{}, fmt.Errorf("rule %q: command selector: %w", m.Name, err)
	}
	rule.commandSelector = commandSelector

	rule.commands = make(map[string]struct{})
	if commandSelector != nil {
		for name, ls := range command.AllLabels() {
			if commandSelector.Matches(ls) {
				rule.commands[name] = struct{}{}
			}
		}
	}

	catalog := command.CommandNames()
	for _, entry := range m.CommandList() {
		name, isSkill := strings.CutPrefix(entry, skillCommandPrefix)
		if isSkill {
			if name == "" {
				continue
			}
			if rule.namedSkills == nil {
				rule.namedSkills = make(map[string]struct{})
			}
			rule.namedSkills[name] = struct{}{}
			continue
		}
		if _, ok := catalog[name]; !ok {
			// An orphan: the command was renamed or removed after the rule was
			// written. It grants nothing — refusing here would take the whole
			// rule down over one stale name — and it is recorded so that the
			// one reporter there is (compileRules in the resolver) has the name
			// to say.
			//
			// The failure this catches is silent by nature: the rule still
			// works, it just grants less than it says, and the command's absence
			// shows up as a user unable to run it rather than as an error
			// anywhere. A deployment where /bind-onex was renamed while two
			// stored rules still named "bind" is what it looks like.
			rule.orphans = append(rule.orphans, name)
			continue
		}
		rule.commands[name] = struct{}{}
	}

	return rule, nil
}

// parseFieldSelector compiles a stored field selector, treating an empty one as
// absent.
//
// The same footgun as the label side: fields.ParseSelector("") yields
// Everything(), so a rule with the column left blank would become a rule
// covering every agent.
func parseFieldSelector(selector string) (fields.Selector, error) {
	if selector == "" {
		return nil, nil
	}
	return fields.ParseSelector(selector)
}

// parseSelector compiles a stored selector, treating an empty one as absent.
//
// The empty string is handled here rather than by the caller because
// labels.Parse("") succeeds and returns a selector that matches everything:
// a rule with the column left blank would become a rule covering every agent.
func parseSelector(selector string) (labels.Selector, error) {
	if selector == "" {
		return nil, nil
	}
	return labels.Parse(selector)
}

// covers reports whether the rule applies to a conversation running as agents.
//
// The three ways of naming agents are a union, which is what makes a rule read
// as "these agents": enumerate them, select them by label, select them by
// field, or any combination.
func (r compiledRule) covers(agents []string, snap *agentSnapshot) bool {
	// A scoped rule applies only to its own namespace. This is checked before
	// every other way of naming agents — including the "*" entry — because it is
	// a precondition rather than a fourth alternative.
	if r.scope != "" && !snap.inScope(agents, r.scope) {
		return false
	}
	if r.allAgents {
		return true
	}
	for _, agent := range agents {
		if _, ok := r.agents[agent]; ok {
			return true
		}
	}
	if r.agentSelector == nil && r.agentFieldSelector == nil {
		return false
	}

	for _, agent := range agents {
		// A label selector only sees an agent whose tags produced labels —
		// which an agent with none does not — so a rule that selects by label
		// fails closed for it. That is the reason a selector rule cannot
		// accidentally cover everybody when the agent service is unreachable.
		if r.agentSelector != nil {
			if ls, ok := snap.set[agent]; ok && r.agentSelector.Matches(ls) {
				return true
			}
		}
		// A field selector only sees an agent the snapshot knows. Unlike the
		// labels above there is nothing to fail closed on: an agent that was
		// listed always has a name and a namespace.
		if r.agentFieldSelector != nil {
			if fs, ok := snap.fields[agent]; ok && r.agentFieldSelector.Matches(fs) {
				return true
			}
		}
	}
	return false
}

// wantsSkills reports whether the rule can grant a skill.
//
// A skill is granted either by name or by the selector matching the skill's own
// labels, so a rule with neither cannot reach one and needs no skill lookup.
func (r compiledRule) wantsSkills() bool {
	return len(r.namedSkills) > 0 || r.commandSelector != nil
}

// grantsSkill reports whether the rule grants one of an agent's skill commands.
func (r compiledRule) grantsSkill(sk runtime.SkillCommand) bool {
	if _, ok := r.namedSkills[sk.Name]; ok {
		return true
	}
	if r.commandSelector == nil {
		return false
	}
	return r.commandSelector.Matches(SkillLabels(sk))
}

// SkillLabels is the label set a skill command is matched by.
//
// It is the counterpart of AllLabels for the commands that are not in the
// catalog: the group is the one the catalog declares for skills, so a rule can
// say "group=skill" — every skill this agent has — or "provider=lark_doc"
// without the catalog knowing any skill by name.
func SkillLabels(sk runtime.SkillCommand) labels.Set {
	ls := labels.Set{command.LabelGroup: command.GroupSkill}
	if sk.Provider != "" {
		ls[SkillProviderLabel] = sk.Provider
	}
	return ls
}
