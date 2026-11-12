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

package core

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// NewHelpCmd returns the /help command that lists all available chat commands.
func NewHelpCmd(root *cobra.Command, out runtime.Responder) *cobra.Command {
	return &cobra.Command{
		Use:   "help",
		Short: "查看可用的聊天命令",
		Annotations: map[string]string{
			chat.ChatGroup: chat.ChatGroupCore,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(out, "可用命令：")
			for _, c := range root.Commands() {
				if c.Hidden {
					continue
				}
				fmt.Fprintf(out, "  /%-20s %s\n", c.Use, c.Short)
			}
			return nil
		},
	}
}
