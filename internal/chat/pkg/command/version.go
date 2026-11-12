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
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/onexstack/onexstack/pkg/version"
)

// NewCmdVersion returns a cobra command for fetching version.
func NewCmdVersion(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "查看 oai-chat-apiserver 构建版本信息",
		Long:  "Print the oai-chat-apiserver version injected at compile time via ldflags.",
		Run: func(cmd *cobra.Command, args []string) {
			info := version.Get()
			if info.GitVersion == "" {
				info.GitVersion = "dev"
			}
			fmt.Fprintf(out, "oai-chat-apiserver version %s\n", info.String())
		},
	}
	return cmd
}
