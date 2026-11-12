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
	"context"
	"strings"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// Selection resolves this request's command selection — the same answer
// buildGroups filters the menu by and ValidateGranted gates a run with.
//
// It is exported for a surface that needs the answer without building a tree to
// ask for it: the MCP bridge filters its tool list with it, which is what keeps
// the tools a model is shown and the commands it may run one set. It is a thin
// wrapper over selectCommands rather than a second implementation, because the
// whole point is that there is one answer.
func Selection(ctx context.Context, sr *runtime.StaticRuntime) runtime.CommandSelection {
	return selectCommands(ctx, sr)
}

// TopLevelCommand maps an MCP tool name to the catalogued command it exposes,
// reporting false for a tool no command backs.
//
// A tool name is a command path with the root dropped and spaces turned into
// underscores (cobrax's rule), so "job_create" is the "job create" command and
// its catalogued top level — the thing a rule grants — is "job". The match is the
// longest catalogued name that is either the whole tool name or a leading
// "name_" segment of it, which is unambiguous because a catalogued command name
// holds no underscore (TestCatalog_NamesHaveNoUnderscore pins that; if it ever
// stops holding, this needs a precise mapping from the bridge, not a heuristic).
//
// A name no command backs is reported as false rather than mapped to something:
// the root's own additions — the MCP-only askai entry among them — are in no
// rule, and ValidateGranted deliberately leaves exactly those ungated. Reporting
// them as unmapped lets the caller give them the same answer.
func TopLevelCommand(toolName string) (string, bool) {
	names := CommandNames()
	if _, ok := names[toolName]; ok {
		return toolName, true
	}

	best := ""
	for name := range names {
		if strings.HasPrefix(toolName, name+"_") && len(name) > len(best) {
			best = name
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}
