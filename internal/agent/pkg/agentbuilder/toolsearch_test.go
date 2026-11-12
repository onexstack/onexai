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

package agentbuilder

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// namedTool is the minimum a tool needs to be considered for tool search: the
// decision is made on names alone, before any Info call.
type namedTool struct{ name string }

func (t namedTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "fake"}, nil
}

func toolsNamed(names ...string) ([]tool.BaseTool, []string) {
	tools := make([]tool.BaseTool, 0, len(names))
	for _, name := range names {
		tools = append(tools, namedTool{name: name})
	}
	return tools, names
}

func TestDynamicTools(t *testing.T) {
	// Five MCP tools plus one built-in, so "only MCP is hidden" and the
	// threshold are both observable in the same fixture.
	tools, names := toolsNamed("mcp_a", "mcp_b", "mcp_c", "mcp_d", "mcp_e", "ask_user_question")

	cases := []struct {
		name string
		opts ToolSearchOptions
		mcp  []string
		want []string
	}{
		{
			name: "disabled installs nothing",
			opts: ToolSearchOptions{Enabled: false},
			mcp:  []string{"mcp_a", "mcp_b", "mcp_c", "mcp_d", "mcp_e"},
		},
		{
			name: "no MCP tools means nothing to hide",
			opts: ToolSearchOptions{Enabled: true},
			mcp:  nil,
		},
		{
			name: "below the threshold installs nothing",
			opts: ToolSearchOptions{Enabled: true, MinTools: 6},
			mcp:  []string{"mcp_a", "mcp_b", "mcp_c", "mcp_d", "mcp_e"},
		},
		{
			name: "at the threshold hides the MCP tools and only those",
			opts: ToolSearchOptions{Enabled: true, MinTools: 5},
			mcp:  []string{"mcp_a", "mcp_b", "mcp_c", "mcp_d", "mcp_e"},
			want: []string{"mcp_a", "mcp_b", "mcp_c", "mcp_d", "mcp_e"},
		},
		{
			name: "the default threshold applies when unset",
			opts: ToolSearchOptions{Enabled: true},
			mcp:  []string{"mcp_a", "mcp_b", "mcp_c", "mcp_d", "mcp_e"},
		},
		{
			name: "an always-visible tool is kept and no longer counts",
			opts: ToolSearchOptions{Enabled: true, MinTools: 5, AlwaysVisible: []string{"mcp_e"}},
			mcp:  []string{"mcp_a", "mcp_b", "mcp_c", "mcp_d", "mcp_e"},
		},
		{
			name: "a name the run does not have is ignored",
			opts: ToolSearchOptions{Enabled: true, MinTools: 5},
			mcp:  []string{"mcp_a", "mcp_b", "mcp_c", "mcp_d", "mcp_e", "deleted_last_week"},
			want: []string{"mcp_a", "mcp_b", "mcp_c", "mcp_d", "mcp_e"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBuilder(Deps{}, Options{ToolSearch: tc.opts})
			got := b.dynamicTools(context.Background(), Request{MCPTools: tc.mcp}, tools, names)

			gotNames := make([]string, 0, len(got))
			for _, g := range got {
				gotNames = append(gotNames, g.(namedTool).name)
			}
			if !slices.Equal(gotNames, tc.want) {
				t.Errorf("dynamic tools = %v, want %v", gotNames, tc.want)
			}
		})
	}
}

// TestToolSearchHidesAndRevealsDynamicTools drives the middleware itself, which
// is the only way to see what the model is actually offered: below the threshold
// nothing is hidden, above it every MCP tool disappears from the tool list until
// a tool_search result names it, and the built-ins never disappear.
func TestToolSearchHidesAndRevealsDynamicTools(t *testing.T) {
	const count = 45
	names := make([]string, 0, count)
	tools := make([]tool.BaseTool, 0, count)
	infos := make([]*schema.ToolInfo, 0, count+1)
	for i := range count {
		name := fmt.Sprintf("mcp_%02d", i)
		names = append(names, name)
		tools = append(tools, namedTool{name: name})
		infos = append(infos, &schema.ToolInfo{Name: name, Desc: "dynamic"})
	}
	infos = append(infos, &schema.ToolInfo{Name: "ask_user_question", Desc: "builtin"})

	b := NewBuilder(Deps{}, Options{ToolSearch: ToolSearchOptions{Enabled: true, MinTools: 40}})
	mw := b.toolSearchHandler(context.Background(), Request{MCPTools: names}, tools, names)
	if mw == nil {
		t.Fatal("tool search was not installed above the threshold")
	}

	state := &adk.ChatModelAgentState{
		Messages:  []*schema.Message{schema.UserMessage("帮我排查 pod")},
		ToolInfos: infos,
	}
	_, got, err := mw.BeforeModelRewriteState(context.Background(), state, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("BeforeModelRewriteState: %v", err)
	}
	visible := map[string]bool{}
	for _, info := range got.ToolInfos {
		visible[info.Name] = true
	}
	if len(got.ToolInfos) != 1 || !visible["ask_user_question"] {
		t.Fatalf("tool list = %d entries (%v), want only the built-in", len(got.ToolInfos), visible)
	}

	// The model searches, finds one tool, and it becomes visible from then on.
	got.Messages = append(got.Messages, &schema.Message{
		Role:       schema.Tool,
		Content:    `{"matches":["mcp_07"]}`,
		ToolName:   "tool_search",
		ToolCallID: "call_1",
	})
	_, next, err := mw.BeforeModelRewriteState(context.Background(), got, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("BeforeModelRewriteState after search: %v", err)
	}
	revealed := map[string]bool{}
	for _, info := range next.ToolInfos {
		revealed[info.Name] = true
	}
	if !revealed["mcp_07"] {
		t.Fatalf("the searched tool is still hidden: %v", revealed)
	}
	if revealed["mcp_08"] {
		t.Fatal("a tool the model did not search for was revealed")
	}
	if !revealed["ask_user_question"] {
		t.Fatal("the built-in tool disappeared across the search")
	}
}

// TestBuildKeepsDynamicToolsExecutable pins the invariant a resume depends on:
// tool search filters what the model *sees*, never what the tool node can
// *run*. If the executable set shrank with the visible one, a run that paused
// on a now-hidden tool could not be resumed at its address — and the failure
// would look like a graph mismatch rather than a missing tool.
func TestBuildKeepsDynamicToolsExecutable(t *testing.T) {
	b := testBuilder(t, Deps{}, Options{ToolSearch: ToolSearchOptions{Enabled: true, MinTools: 1}})
	res, err := b.Build(context.Background(), Request{
		BaseTools: []tool.BaseTool{&fakeTool{name: "mcp_a"}, &fakeTool{name: "mcp_b"}},
		MCPTools:  []string{"mcp_a", "mcp_b"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if want := []string{"mcp_a", "mcp_b"}; !reflect.DeepEqual(res.ToolNames, want) {
		t.Errorf("ToolNames = %v, want %v: tool search must not remove executable tools", res.ToolNames, want)
	}
}

// TestDynamicToolsKeepsNonMCPToolsVisible pins the rule that makes tool search
// safe: a tool no MCP server published is never hidden, whatever the threshold
// is. That covers the built-ins, the knowledge-base and image tools this builder
// adds, and anything else the caller resolved itself.
func TestDynamicToolsKeepsNonMCPToolsVisible(t *testing.T) {
	tools, names := toolsNamed("ask_user_question", "mcp_a", "mcp_b")
	b := NewBuilder(Deps{}, Options{ToolSearch: ToolSearchOptions{Enabled: true, MinTools: 1}})

	got := b.dynamicTools(context.Background(), Request{
		MCPTools: []string{"mcp_a", "mcp_b"},
	}, tools, names)

	for _, g := range got {
		if g.(namedTool).name == "ask_user_question" {
			t.Fatal("a tool no MCP server published was hidden by tool search")
		}
	}
	if len(got) != 2 {
		t.Errorf("dynamic tools = %d, want 2", len(got))
	}
}
