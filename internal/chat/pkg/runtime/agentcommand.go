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

package runtime

import (
	"context"
	"time"
)

// SkillCommand is one skill an agent has exposed as a command.
//
// It is a command like any other to the person typing it — /k8s-troubleshoot —
// but it is not in the compiled catalog: its name is inside a skill repository
// and only the agent service can enumerate it. That is why it travels here as
// data rather than being listed with the rest.
type SkillCommand struct {
	// Name is what is typed after the slash.
	Name string
	// DisplayName and Description are what a menu shows.
	DisplayName string
	Description string
	// Provider names the repository kind the skill came from
	// (github/gitlab/lark_doc). It is a label on the command, so a rule can
	// grant every skill from one kind without naming them.
	Provider string
}

// CustomCommand is one user-defined slash command an agent exposes.
//
// Its parameters travel with it because the chat service builds a cobra command
// from them: flags, positional arguments and usage are the same code path the
// built-in commands use.
type CustomCommand struct {
	// Name is what is typed after the slash.
	Name string
	// DisplayName and Description are what a menu shows.
	DisplayName string
	Description string
	// Usage is the human-facing invocation shape.
	Usage string
	// Type is prompt, http or tool.
	Type string
	// Aliases are extra names the command answers to.
	Aliases []string
	// Agent is the agent that exposed the command. An invocation names it so
	// the server can check the binding it already used to build the menu.
	Agent string
	// Labels are the object's labels, should a rule want to select them.
	Labels map[string]string
	// Parameters are the declared arguments.
	Parameters []CustomCommandParameter
}

// CustomCommandParameter is one declared argument.
type CustomCommandParameter struct {
	Name        string
	In          string
	Type        string
	Description string
	Required    bool
	// DefaultJSON is the JSON-encoded default, empty when there is none.
	DefaultJSON string
	Enum        []string
	Format      string
	Short       string
	// SchemaJSON constrains object, array and json parameters.
	SchemaJSON string
}

// CustomCommandSource enumerates the commands an agent exposes.
type CustomCommandSource interface {
	CustomCommandsOf(ctx context.Context, agent string) ([]CustomCommand, error)
}

// CustomRunner runs one user-defined command. The runner invokes the agent
// service and renders the result on the surface the command came from.
type CustomRunner interface {
	RunCustom(
		ctx context.Context, dr *DynamicRuntime, out Responder,
		command CustomCommand, params map[string]any,
	) error
}

// CommandSelection is the set of commands one conversation may see and run.
//
// It is a command-name set rather than a list of groups: a group is how the
// catalog is organised, and a rule that names a group is compiled into the
// names that group holds before it ever reaches here.
//
// # The zero value admits nothing
//
// A selection is a grant list, and there is deliberately no way to spell
// "everything": commands come from the stored rules and from nowhere else. An
// agent no rule covers has no commands, and neither does a caller that cannot
// name an agent — the answer to "who granted this?" is the only thing that puts
// a command in the set, so an unanswerable question is an empty set rather than
// an unfiltered one. A caller that wants the whole catalog without asking about
// a conversation is building a catalogue, not a menu, and says so where it
// builds one.
type CommandSelection struct {
	// Names holds the granted catalog command names. It is empty for a
	// conversation no rule addresses, and empty again when rules address it but
	// grant nothing — the second is a configuration error rather than a
	// deliberate "nothing", and the resolver reports it.
	Names map[string]struct{}
	// Skills are the skill commands the conversation may run, already narrowed
	// to the ones its agents exposed.
	Skills []SkillCommand
	// Customs are the user-defined commands the conversation's agents bind.
	// Binding is the whole grant: there is no second rule layer for them.
	Customs []CustomCommand
	// ResolvedAt is when the selection was computed. It is carried so a log
	// line can say how stale an answer is without a second lookup.
	ResolvedAt time.Time
}

// Allows reports whether a catalog command may be shown and run.
func (s CommandSelection) Allows(name string) bool {
	_, ok := s.Names[name]
	return ok
}

// AllowsAny reports whether the selection admits at least one command.
//
// False is the ordinary state of a conversation no rule addresses, and the
// reported state when a rule covers one and grants it nothing: the two are the
// same set, and the resolver is what tells them apart in its logs.
func (s CommandSelection) AllowsAny() bool {
	return len(s.Names) > 0 || len(s.Skills) > 0 || len(s.Customs) > 0
}

// AllowsCustom reports whether a user-defined command may be shown and run.
func (s CommandSelection) AllowsCustom(name string) bool {
	for i := range s.Customs {
		if s.Customs[i].Name == name {
			return true
		}
		for _, alias := range s.Customs[i].Aliases {
			if alias == name {
				return true
			}
		}
	}
	return false
}

// SkillRunner runs one skill command as a turn.
//
// It is the same shape as AgentCommandResolver and exists for the same reason:
// turning a skill command into a turn means reaching the AI pipeline, which
// lives in the bot, which the command package cannot import.
type SkillRunner interface {
	// RunSkill runs skill, with the text the user typed after the command as
	// the turn's question. An empty args is the ordinary case for a command
	// that is a verb on its own.
	RunSkill(ctx context.Context, dr *DynamicRuntime, skill SkillCommand, args string) error
}

// AgentCommandResolver answers which commands a conversation's agents have.
//
// It is implemented by the store-backed resolver and held on StaticRuntime,
// which is what lets the command package ask without knowing where the answer
// comes from.
type AgentCommandResolver interface {
	// Resolve returns the commands available to a conversation running as the
	// given agents. The result is the union over them, in the order the rules
	// were written: a conversation may be answered by any of its agents, so it
	// may run any of their commands.
	//
	// The default is deny. An empty agent list means the caller has no agent
	// context — a surface without a conversation, or one whose agents have not
	// been resolved yet — and nobody has granted commands to an agent that
	// cannot be named, so it yields an empty selection. Agents no stored rule
	// covers yield an empty selection for the same reason.
	//
	// It never fails. A resolver that cannot read its rules reports the failure
	// and keeps serving what it last read; a deployment that has never read
	// them has nothing to grant, and answers with the empty selection that a
	// conversation nobody configured gets. A grant list that cannot be read is
	// an unknown grant list, and an unknown grant list grants nothing.
	Resolve(ctx context.Context, agents []string) CommandSelection
}
