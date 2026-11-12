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
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

type captureCustomRunner struct {
	command runtime.CustomCommand
	params  map[string]any
}

func (r *captureCustomRunner) RunCustom(
	_ context.Context, _ *runtime.DynamicRuntime, _ runtime.Responder,
	command runtime.CustomCommand, params map[string]any,
) error {
	r.command = command
	r.params = params
	return nil
}

type nopResponder struct{}

func (nopResponder) Write(p []byte) (int, error)                            { return len(p), nil }
func (nopResponder) SendText(context.Context, string) error                 { return nil }
func (nopResponder) SendCard(context.Context, string, map[string]any) error { return nil }
func (nopResponder) SendMarkdownCard(context.Context, string, string, string) error {
	return nil
}
func (nopResponder) SendInteractiveCard(context.Context, any) error { return nil }
func (nopResponder) SendResult(context.Context, *runtime.CommandResult) error {
	return nil
}

func newTestCustomCommand(t *testing.T) runtime.CustomCommand {
	t.Helper()
	return runtime.CustomCommand{
		Name:        "deploy",
		DisplayName: "部署",
		Type:        "http",
		Aliases:     []string{"dep"},
		Usage:       "<service> [--env=dev|prod]",
		Parameters: []runtime.CustomCommandParameter{
			{Name: "service", In: "positional", Type: "string", Required: true},
			{Name: "env", Type: "string", Short: "e", DefaultJSON: `"dev"`, Enum: []string{"dev", "prod"}},
			{Name: "count", Type: "integer"},
			{Name: "verbose", Type: "boolean"},
			{Name: "labels", Type: "array"},
		},
	}
}

func TestCustomCommandParsesFlagsAndPositionals(t *testing.T) {
	runner := &captureCustomRunner{}
	sr := &runtime.StaticRuntime{CustomRunner: runner}
	cc := newTestCustomCommand(t)

	cmd := newCustomCommand(cc, []string{"dep"}, sr, nopResponder{})
	if got := cmd.Annotations[chat.CustomCommand]; got != cc.Name {
		t.Errorf("custom command annotation = %q, want %q", got, cc.Name)
	}
	cmd.SetArgs([]string{"api", "--env", "prod", "--count", "3", "--verbose", "--labels", "a,b"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if runner.params["service"] != "api" {
		t.Errorf("service = %v, want api", runner.params["service"])
	}
	if runner.params["env"] != "prod" {
		t.Errorf("env = %v, want prod", runner.params["env"])
	}
	if runner.params["count"] != int64(3) {
		t.Errorf("count = %v (%T), want int64(3)", runner.params["count"], runner.params["count"])
	}
	if runner.params["verbose"] != true {
		t.Errorf("verbose = %v", runner.params["verbose"])
	}
	labels, ok := runner.params["labels"].([]any)
	if !ok || len(labels) != 2 || labels[0] != "a" || labels[1] != "b" {
		t.Errorf("labels = %v (%T), want [a b]", runner.params["labels"], runner.params["labels"])
	}
	if !cmd.HasAlias("dep") {
		t.Errorf("alias dep was not registered")
	}
	if !strings.Contains(cmd.Use, "<service>") {
		t.Errorf("Use = %q, want it to name the positional argument", cmd.Use)
	}
}

func TestCustomCommandErrorsCarryUsage(t *testing.T) {
	runner := &captureCustomRunner{}
	sr := &runtime.StaticRuntime{CustomRunner: runner}
	cc := newTestCustomCommand(t)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"api", "--nope"}, "unknown flag"},
		{"missing positional", []string{}, "缺少位置参数"},
		{"missing required flag", []string{"api", "--env", "dev"}, ""},
		{"enum", []string{"api", "--env", "stage"}, "必须是"},
		{"bad int", []string{"api", "--count", "x"}, "invalid"},
	}
	// --env has a default, so "missing required flag" is not an error here; the
	// case is kept as an explicit no-error to pin that a default is not
	// mistaken for a missing value.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newCustomCommand(cc, nil, sr, nopResponder{})
			cmd.SetArgs(tt.args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if tt.name == "missing required flag" {
				if err != nil {
					t.Fatalf("default value should satisfy env: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Execute accepted %v", tt.args)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), "用法:") {
				t.Errorf("error %q does not carry usage", err)
			}
		})
	}
}

func TestCustomCommandUsageIsDerivedFromDeclarations(t *testing.T) {
	cc := newTestCustomCommand(t)
	cmd := newCustomCommand(cc, nil, &runtime.StaticRuntime{}, nopResponder{})

	want := "/deploy <service> [--env=<env>] [--count=<count>] [--verbose] [--labels=<labels>,...]"
	if !strings.Contains(cmd.Long, "用法: "+want) {
		t.Errorf("Long = %q, want canonical usage %q", cmd.Long, want)
	}
	// The author-editable spec.usage is an example only; it must not leak into
	// the line that errors quote.
	if strings.Contains(cmd.Long, "dev|prod") {
		t.Errorf("Long = %q, spec.usage leaked into the canonical usage", cmd.Long)
	}
	if !strings.Contains(cmd.Example, "/deploy <service> [--env=dev|prod]") {
		t.Errorf("Example = %q, want the author's example", cmd.Example)
	}
}

func TestCustomCommandHintsAtPositionalParameter(t *testing.T) {
	cc := newTestCustomCommand(t)
	cmd := newCustomCommand(cc, nil, &runtime.StaticRuntime{CustomRunner: &captureCustomRunner{}}, nopResponder{})
	cmd.SetArgs([]string{"--service", "api"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("--service was accepted for a positional parameter")
	}
	for _, want := range []string{
		`参数 "service" 是位置参数`,
		"不要使用 --service",
		"用法: /deploy <service>",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want %q", err, want)
		}
	}
}

// The live helper-public/hello definition: a required positional name plus a
// stale author usage that advertises --name. The canonical line and the hint
// must both correct it.
func TestHelloPositionalNameAndStaleUsage(t *testing.T) {
	cc := runtime.CustomCommand{
		Name:  "hello",
		Type:  "prompt",
		Usage: "--name=lkong",
		Parameters: []runtime.CustomCommandParameter{
			{Name: "name", In: "positional", Type: "string", Required: true},
		},
	}

	runner := &captureCustomRunner{}
	cmd := newCustomCommand(cc, nil, &runtime.StaticRuntime{CustomRunner: runner}, nopResponder{})
	cmd.SetArgs([]string{"cccc"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("/hello cccc: %v", err)
	}
	if runner.params["name"] != "cccc" {
		t.Errorf("name = %v, want cccc", runner.params["name"])
	}

	cmd = newCustomCommand(cc, nil, &runtime.StaticRuntime{CustomRunner: runner}, nopResponder{})
	cmd.SetArgs([]string{"--name", "cccc"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("/hello --name cccc was accepted")
	}
	for _, want := range []string{`参数 "name" 是位置参数`, "用法: /hello <name>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "--name=lkong") {
		t.Errorf("error = %q, stale spec.usage leaked into the usage line", err)
	}
}

func TestCustomCommandsDropNamesThatAreTaken(t *testing.T) {
	taken := map[string]struct{}{"doctor": {}}
	customs := []runtime.CustomCommand{
		{Name: "doctor", Type: "prompt"},
		{Name: "deploy", Type: "prompt", Aliases: []string{"doctor"}},
	}
	got := customCommands(customs, taken, &runtime.StaticRuntime{}, nopResponder{})
	if len(got) != 1 {
		t.Fatalf("built %d commands, want 1", len(got))
	}
	if got[0].Name() != "deploy" {
		t.Errorf("built %q, want deploy", got[0].Name())
	}
	if got[0].HasAlias("doctor") {
		t.Errorf("alias doctor should have been dropped")
	}
}

func TestBuildGroupsPlacesCustomCommandsInTheirGroup(t *testing.T) {
	groups := buildGroups(context.Background(), &runtime.StaticRuntime{}, nopResponder{},
		Groups(), func(string) bool { return false }, nil,
		[]runtime.CustomCommand{{Name: "hello", Type: "prompt"}})

	if len(groups) != 1 {
		t.Fatalf("groups = %d, want only the custom group", len(groups))
	}
	if groups[0].Message != "自定义命令:" {
		t.Errorf("group message = %q", groups[0].Message)
	}
	if len(groups[0].Commands) != 1 || groups[0].Commands[0].Name() != "hello" {
		t.Fatalf("commands = %v", groups[0].Commands)
	}
	if got := groups[0].Commands[0].Annotations[chat.ChatGroup]; got != GroupCustom {
		t.Errorf("chat group annotation = %q, want %q", got, GroupCustom)
	}
}

func TestValidateGrantedChecksCustomCommands(t *testing.T) {
	cmd := &cobraCommandStub{name: "deploy", group: GroupCustom}
	// A selection without the command refuses it.
	if err := ValidateGranted(cmd.command(), runtime.CommandSelection{}); err == nil {
		t.Fatalf("an unbound custom command was allowed")
	}
	selection := runtime.CommandSelection{Customs: []runtime.CustomCommand{{Name: "deploy"}}}
	if err := ValidateGranted(cmd.command(), selection); err != nil {
		t.Fatalf("a bound custom command was refused: %v", err)
	}
}

// cobraCommandStub builds a top-level cobra command carrying the annotations
// ValidateGranted reads.
type cobraCommandStub struct {
	name  string
	group string
}

func (s *cobraCommandStub) command() *cobra.Command {
	return &cobra.Command{
		Use:         s.name,
		Annotations: map[string]string{chat.ChatGroup: s.group},
	}
}

// An alias must never take another command's own name: every primary name is
// claimed before any alias is registered.
func TestCustomCommandsReserveNamesBeforeAliases(t *testing.T) {
	customs := []runtime.CustomCommand{
		{Name: "alpha", Type: "prompt", Aliases: []string{"beta"}},
		{Name: "beta", Type: "prompt"},
	}
	got := customCommands(customs, map[string]struct{}{}, &runtime.StaticRuntime{}, nopResponder{})
	if len(got) != 2 {
		t.Fatalf("built %d commands, want 2", len(got))
	}
	if got[0].Name() != "alpha" || got[1].Name() != "beta" {
		t.Fatalf("built %q and %q, want alpha and beta", got[0].Name(), got[1].Name())
	}
	if got[0].HasAlias("beta") {
		t.Error("alias beta should have been dropped once beta kept its own name")
	}
}

// An object-typed default is a JSON document. It used to fail a string decode
// and disappear, leaving the flag with no default at all.
func TestCustomCommandKeepsAJSONDefault(t *testing.T) {
	cc := runtime.CustomCommand{
		Name: "scale",
		Type: "http",
		Parameters: []runtime.CustomCommandParameter{
			{Name: "selector", Type: "object", DefaultJSON: `{"app":"api"}`},
		},
	}
	cmd := newCustomCommand(cc, nil, &runtime.StaticRuntime{}, nopResponder{})
	flag := cmd.Flags().Lookup("selector")
	if flag == nil {
		t.Fatal("flag selector was not registered")
	}
	if flag.DefValue != `{"app":"api"}` {
		t.Errorf("default = %q, want the JSON document", flag.DefValue)
	}
}
