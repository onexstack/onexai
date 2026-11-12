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

package command

import (
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// skillCommands builds the commands a conversation may run from the skills its
// agents exposed.
//
// They are not in the catalog because their names are not knowable there: a
// skill's name lives inside a skill repository, and only the agent service can
// enumerate it. What can be declared ahead of time is the group they belong to,
// which is why GroupSkill exists as an empty group rather than as nothing.
//
// A skill whose name is already a compiled command is dropped. The compiled one
// wins because its behaviour is known here — it is the command the rest of this
// package's rules were written against — and a skill shadowing it would change
// what an existing name does depending on what an agent happens to expose.
func skillCommands(skills []runtime.SkillCommand, sr *runtime.StaticRuntime, out runtime.Responder) []*cobra.Command {
	if len(skills) == 0 {
		return nil
	}

	taken := CommandNames()
	cmds := make([]*cobra.Command, 0, len(skills))
	for _, sk := range skills {
		if _, clash := taken[sk.Name]; clash {
			slog.Warn("a skill command was dropped: its name is already a command", "skill", sk.Name)
			continue
		}
		if sk.Name == "" {
			continue
		}
		cmds = append(cmds, newSkillCommand(sr, out, sk))
	}
	return cmds
}

// newSkillCommand builds one skill command.
func newSkillCommand(sr *runtime.StaticRuntime, out runtime.Responder, sk runtime.SkillCommand) *cobra.Command {
	return &cobra.Command{
		Use:   sk.Name,
		Short: sk.Description,
		// What follows a skill command is the asker's own words for the turn,
		// so it reaches the model rather than being parsed as flags here.
		DisableFlagParsing: true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		Annotations: map[string]string{
			// A skill command is a turn, and a turn draws a streaming card, so
			// it needs the chat context only the Lark surface has. Hiding it
			// elsewhere is honest: the other surfaces cannot run it.
			chat.LarkOnly: chat.True,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if sr.SkillRunner == nil {
				return runtime.NewUserError("Skill commands are not enabled on this deployment.")
			}
			return sr.SkillRunner.RunSkill(
				cmd.Context(), runtime.From(cmd.Context()), sk, strings.Join(args, " "),
			)
		},
	}
}
