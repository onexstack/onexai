// Copyright 2025 Lingfei Kong <colin404@foxmail.com>
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
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// NewCmdAlpha creates the alpha command group for experimental features.
func NewCmdAlpha(out runtime.Responder) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alpha",
		Short: "处于 alpha 阶段的实验性命令",
		Long:  `These commands correspond to alpha features that are not enabled in Kubernetes clusters by default.`,
	}

	// NewXXXXCommand() will hide the alpha command if it has no subcommands. Overriding
	// the help function ensures a reasonable message if someone types the hidden command anyway.
	if !cmd.HasAvailableSubCommands() {
		cmd.SetHelpFunc(func(*cobra.Command, []string) {
			cmd.Println("No alpha commands are available in this version of OneX")
		})
	}

	return cmd
}
