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

package model

// Status values for AgentCommandM.Status.
const (
	// AgentCommandStatusEnabled lets the rule take part in matching.
	AgentCommandStatusEnabled = "enabled"
	// AgentCommandStatusDisabled takes it out of matching without deleting it.
	AgentCommandStatusDisabled = "disabled"
)

// AgentCommandM is the in-memory rule shape the command resolver reads.
//
// It has no table and no API object: a rule is projected from an AgentBinding
// (see the chat control-plane store), and the projection lives here because the
// resolver's matching code predates the object and is the one place that knows
// how to compare a rule with a conversation's agents.
//
// It holds no command definitions: the commands are compiled in (see
// internal/chat/pkg/command/catalog.go) and the rule only says which of them an
// agent gets.
//
// Both sides of the rule use the same two shapes, which is the whole design:
//
//	                 batch                          explicit
//	this rule covers  selector                       agents
//	these get        command_selector               commands
//
// A selector is a Kubernetes label selector. On the agent side it matches the
// labels projected from the agent record (name, namespace, tags); on the command
// side it matches the labels the catalog declares (group, plus any dimension an
// entry opts into). The two name lists are the escape hatch for a single agent
// or a single command.
type AgentCommandM struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string `gorm:"uniqueIndex;size:128" json:"name"`
	DisplayName string `gorm:"size:256" json:"displayName"`
	Description string `gorm:"type:text" json:"description"`
	Status      string `gorm:"size:32" json:"status"`

	// AgentSelector selects the agents this rule covers, by their labels —
	// which is to say by the tags the agent record carries.
	// Empty means the rule does not match on labels; it never means "all
	// agents" — labels.Parse("") yields a selector that matches everything,
	// which is why the empty string is handled before parsing rather than
	// after.
	AgentSelector string `gorm:"column:agent_selector;type:text" json:"agentSelector"`
	// AgentFieldSelector selects the agents this rule covers by their own
	// columns rather than their tags: metadata.name, metadata.namespace (and the
	// legacy metadata.workspace alias).
	//
	// It is a second selector rather than more label keys because Kubernetes
	// keeps the two spaces apart: a label can never name metadata.name and a
	// field can never name a label, so a tag cannot collide with a property the
	// record owns, and no name has to be reserved. The field names are a closed
	// set, which is also what makes a mistyped one refusable at write time —
	// something a label key can never be.
	AgentFieldSelector string `gorm:"column:agent_field_selector;type:text" json:"agentFieldSelector"`
	// Agents names the agents this rule covers. A malformed or empty value
	// yields nil; ["*"] is the explicit way to say every agent.
	Agents string `gorm:"type:text" json:"agents"`
	// Scope restricts the rule to the agents of one namespace, and is ANDed
	// with everything above. It is not a column: only the AgentBinding
	// projection sets it, because a binding is namespace-scoped while the
	// resolver matches agents by name across namespaces.
	Scope string `gorm:"-" json:"-"`

	// CommandSelector selects the commands this rule grants, by their labels.
	// Empty means it grants none of them, not all.
	CommandSelector string `gorm:"column:command_selector;type:text" json:"commandSelector"`
	// Commands names individual commands: a cobra command name such as "pod",
	// or "skill:<name>" for a skill exposed as a command.
	Commands string `gorm:"type:text" json:"commands"`

	CreatedAt int64 `json:"createdAt"`
	UpdatedAt int64 `json:"updatedAt"`
}

// Enabled reports whether the rule takes part in matching.
func (m *AgentCommandM) Enabled() bool { return m.Status != AgentCommandStatusDisabled }

// AgentList decodes the Agents column.
//
// A malformed value yields nil rather than an error, the same call the on-call
// group makes for the same reason: this is read on the path that answers every
// message, and one bad row must not take the command surface down with it. The
// write path refuses the same value instead — there the operator is still
// around to be told.
func (m *AgentCommandM) AgentList() []string { return DecodeNameList(m.Agents) }

// CommandList decodes the Commands column. It swallows a malformed value for
// the same reason AgentList does.
func (m *AgentCommandM) CommandList() []string { return DecodeNameList(m.Commands) }
