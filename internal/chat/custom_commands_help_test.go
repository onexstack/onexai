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

package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/agentcmd"
	"github.com/onexstack/onexai/internal/chat/pkg/command"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// This test wires the production pieces together without Lark or a live agent
// service: the resolver reads a custom command source, the command tree is the
// one the bot builds, and /help plus /hello run through the same cobra path a
// Lark message takes.

type emptyRules struct{}

func (emptyRules) ListEnabled(context.Context) ([]*model.AgentCommandM, error) { return nil, nil }

type boundCustoms struct{}

func (boundCustoms) CustomCommandsOf(context.Context, string) ([]runtime.CustomCommand, error) {
	return []runtime.CustomCommand{{
		Name:        "hello",
		DisplayName: "打招呼",
		Type:        "prompt",
		Usage:       "<name> [--formal]",
		Parameters: []runtime.CustomCommandParameter{
			{Name: "name", In: "positional", Type: "string", Required: true},
			{Name: "formal", Type: "boolean", Short: "f"},
		},
	}}, nil
}

type captureRunner struct {
	params map[string]any
}

func (r *captureRunner) RunCustom(
	_ context.Context, _ *runtime.DynamicRuntime, _ runtime.Responder,
	_ runtime.CustomCommand, params map[string]any,
) error {
	r.params = params
	return nil
}

type captureResponder struct {
	sb strings.Builder
}

func (r *captureResponder) Write(p []byte) (int, error)                            { return r.sb.Write(p) }
func (r *captureResponder) SendText(context.Context, string) error                 { return nil }
func (r *captureResponder) SendCard(context.Context, string, map[string]any) error { return nil }
func (r *captureResponder) SendMarkdownCard(context.Context, string, string, string) error {
	return nil
}
func (r *captureResponder) SendInteractiveCard(context.Context, any) error { return nil }
func (r *captureResponder) SendResult(context.Context, *runtime.CommandResult) error {
	return nil
}

func newCustomCommandContext(t *testing.T, runner runtime.CustomRunner) (context.Context, *runtime.StaticRuntime) {
	t.Helper()
	resolver := agentcmd.New(agentcmd.Options{Rules: emptyRules{}, Customs: boundCustoms{}})
	sr := &runtime.StaticRuntime{AgentCommands: resolver, CustomRunner: runner}
	dr := &runtime.DynamicRuntime{
		Source:         runtime.RequestSourceLark,
		ResolvedAgents: runtime.FixedAgents([]string{"helper"}),
	}
	return runtime.Inject(context.Background(), dr), sr
}

func TestHelpListsABoundCustomCommand(t *testing.T) {
	runner := &captureRunner{}
	ctx, sr := newCustomCommandContext(t, runner)
	out := &captureResponder{}

	tree := command.NewBotCommandTree(ctx, sr, out)
	tree.SetOut(out)
	tree.SetErr(out)
	tree.SetArgs([]string{"help"})
	tree.SetContext(ctx)
	if err := tree.ExecuteContext(ctx); err != nil {
		t.Fatalf("help: %v", err)
	}

	text := out.sb.String()
	if !strings.Contains(text, "自定义命令:") {
		t.Errorf("help does not carry the custom group heading:\n%s", text)
	}
	if !strings.Contains(text, "hello") {
		t.Errorf("help does not list the bound command:\n%s", text)
	}
}

func TestBoundCustomCommandRunsThroughCobra(t *testing.T) {
	runner := &captureRunner{}
	ctx, sr := newCustomCommandContext(t, runner)
	out := &captureResponder{}

	tree := command.NewBotCommandTree(ctx, sr, out)
	tree.SetOut(out)
	tree.SetErr(out)
	tree.SetArgs([]string{"hello", "世界", "--formal"})
	tree.SetContext(ctx)
	if err := tree.ExecuteContext(ctx); err != nil {
		t.Fatalf("hello: %v", err)
	}

	if runner.params["name"] != "世界" {
		t.Errorf("name = %v, want 世界", runner.params["name"])
	}
	if runner.params["formal"] != true {
		t.Errorf("formal = %v, want true", runner.params["formal"])
	}
}
