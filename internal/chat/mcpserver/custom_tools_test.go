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

package mcpserver

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/agentcmd"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

type mcpEmptyRules struct{}

func (mcpEmptyRules) ListEnabled(context.Context) ([]*model.AgentCommandM, error) { return nil, nil }

type mcpCustomSource struct{ names []string }

func (s mcpCustomSource) CustomCommandsOf(context.Context, string) ([]runtime.CustomCommand, error) {
	out := make([]runtime.CustomCommand, 0, len(s.names))
	for _, name := range s.names {
		out = append(out, runtime.CustomCommand{Name: name, Type: "prompt"})
	}
	return out, nil
}

// TestToolFilterGatesCustomToolsByBinding pins the two kinds of gate the MCP
// bridge applies in one pass: a custom tool follows the caller's binding, a
// catalogued tool follows its grant, and an uncatalogued root tool is left
// alone (the same rule ValidateGranted keeps).
func TestToolFilterGatesCustomToolsByBinding(t *testing.T) {
	resolver := agentcmd.New(agentcmd.Options{
		Rules:   mcpEmptyRules{},
		Customs: mcpCustomSource{names: []string{"hello"}},
	})
	sr := &runtime.StaticRuntime{AgentCommands: resolver}
	dr := &runtime.DynamicRuntime{ResolvedAgents: runtime.FixedAgents([]string{"helper"})}
	ctx := runtime.Inject(context.Background(), dr)

	filter := toolFilter(sr, map[string]string{
		"hello": customToolPrefix + "hello",
		"job":   "job",
	})
	kept := filter(ctx, []mcp.Tool{{Name: "hello"}, {Name: "job"}, {Name: "other"}})

	names := make(map[string]bool, len(kept))
	for _, tool := range kept {
		names[tool.Name] = true
	}
	if !names["hello"] {
		t.Errorf("a bound custom tool was filtered out")
	}
	if names["job"] {
		t.Errorf("a catalogued tool with no grant was kept")
	}
	if !names["other"] {
		t.Errorf("an uncatalogued tool was filtered out")
	}
}
