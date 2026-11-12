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

// Package approval decides which tool calls must be approved by a human before
// they run.
//
// The decision is declarative and lives outside the tools: a Policy is a set of
// tool-name patterns collected from the resources a run selected, and it is
// enforced in one place — the engine's tool-call middleware. Neither a built-in
// tool nor a third-party MCP server has to know it is being gated, which is the
// point: an MCP tool cannot be edited to add an approval step, but its name can
// be listed here.
package approval

import "strings"

// Decision is the verdict a Policy reaches for one tool call.
type Decision int

const (
	// DecisionAllow lets the call proceed.
	DecisionAllow Decision = iota
	// DecisionAsk holds the call until a human approves it.
	DecisionAsk
)

// Policy is an immutable set of tool-name patterns that require approval.
//
// The zero value allows everything, which is the pre-existing behaviour: a
// deployment that declares no pattern keeps running every tool. Being permissive
// by default is deliberate — this gates work that was already permitted, and a
// policy mistake that blocks every tool would be worse than one that gates
// nothing.
type Policy struct {
	// ask holds the patterns whose match must be approved, in declaration order.
	ask []string
	// reserved holds tool names that are never gated however well they match
	// ask. It exists for tools whose contract is itself to consult the human
	// (ask_user_question): gating one would ask twice for a single answer, and
	// the asker would have to approve the act of asking.
	reserved map[string]bool
}

// New builds a Policy from the approval patterns the run's resources declared
// and the tool names that opt out of gating. Blank entries are ignored, as are
// repeats: a policy is a set of patterns, and the callers that build one union
// several sources, so the same pattern arriving twice is the normal case rather
// than a mistake.
//
// Matching is case-insensitive. Tool names are whatever an MCP server chose to
// call its tools, so an operator writing "deploy" cannot be expected to know
// whether the server spells it "Deploy" — and a case mismatch that silently
// disabled a gate is a far worse failure than one that asks unnecessarily.
func New(ask []string, reserved []string) Policy {
	p := Policy{reserved: make(map[string]bool, len(reserved))}
	seen := make(map[string]bool, len(ask))
	for _, a := range ask {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		p.ask = append(p.ask, a)
	}
	for _, r := range reserved {
		r = strings.ToLower(strings.TrimSpace(r))
		if r != "" {
			p.reserved[r] = true
		}
	}
	return p
}

// Decide reports whether toolName must be approved before it runs.
//
// A pattern is one of three things, and there is no fourth: the bare "*" gates
// every tool, a trailing "*" ("delete_*") gates every tool whose name starts
// with that prefix, and anything else gates exactly that name. A "*" anywhere
// else is a literal character. Anchoring the form this way means a pattern can
// never be malformed — a misspelling simply matches nothing, exactly as a
// misspelled exact name would.
func (p Policy) Decide(toolName string) Decision {
	if len(p.ask) == 0 {
		return DecisionAllow
	}
	name := strings.ToLower(strings.TrimSpace(toolName))
	// An unnamed call is a framework anomaly, not something a policy can
	// describe: there is no name to match and none to show the approver.
	if name == "" {
		return DecisionAllow
	}
	if p.reserved[name] {
		return DecisionAllow
	}
	for _, pattern := range p.ask {
		if match(pattern, name) {
			return DecisionAsk
		}
	}
	return DecisionAllow
}

// Empty reports whether the policy gates nothing, so callers can skip the
// machinery of asking — there is no point pausing a run no one can approve.
func (p Policy) Empty() bool { return len(p.ask) == 0 }

// String renders the policy for a log line.
func (p Policy) String() string {
	if len(p.ask) == 0 {
		return "none"
	}
	return strings.Join(p.ask, ",")
}

// match reports whether pattern gates name. Both are already lower-cased.
func match(pattern, name string) bool {
	if pattern == "*" {
		return true
	}
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(name, prefix)
	}
	return pattern == name
}
