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
	"testing"

	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// fakeResolver answers with a selection a test chose, and records what it was
// asked about.
type fakeResolver struct {
	selection runtime.CommandSelection
	asked     []string
	calls     int
}

func (f *fakeResolver) Resolve(_ context.Context, agents []string) runtime.CommandSelection {
	f.calls++
	f.asked = agents
	if len(agents) == 0 {
		// The contract every implementation owes: a caller that cannot name an
		// agent has been granted nothing. Modelling it here rather than at the
		// call site keeps the rule in one place.
		return runtime.CommandSelection{}
	}
	return f.selection
}

// granted builds a selection that admits exactly the named commands.
func granted(names ...string) runtime.CommandSelection {
	sel := runtime.CommandSelection{Names: make(map[string]struct{}, len(names))}
	for _, name := range names {
		sel.Names[name] = struct{}{}
	}
	return sel
}

// treeContext builds a context whose runtime reports the given agents.
func treeContext(agents ...string) context.Context {
	return runtime.Inject(context.Background(), &runtime.DynamicRuntime{
		ResolvedAgents: runtime.FixedAgents(agents),
	})
}

// TestBuildGroups_KeepsOnlyTheGrantedCommands pins the listing half: a command
// the conversation's agent was not given is not built at all.
//
// Not building it is the point rather than a detail — the tree is rebuilt for
// every message, and constructing a command allocates an options struct, a flag
// set and a cobra tree.
func TestBuildGroups_KeepsOnlyTheGrantedCommands(t *testing.T) {
	res := &fakeResolver{selection: granted("pod", "doctor")}
	sr := &runtime.StaticRuntime{AgentCommands: res}
	ctx := treeContext("bke-agent")
	out := runtime.NewStructuredResponder(runtime.From(ctx))

	tree := NewBotCommandTree(ctx, sr, out)

	for _, name := range []string{"pod", "doctor"} {
		if _, _, err := tree.Find([]string{name}); err != nil {
			t.Errorf("Find(%q) = %v, but the command was granted", name, err)
		}
	}
	for _, name := range []string{"oncall", "askai", "job", "pipeline"} {
		if _, _, err := tree.Find([]string{name}); err == nil {
			t.Errorf("Find(%q) found a command that was not granted", name)
		}
	}

	if len(res.asked) != 1 || res.asked[0] != "bke-agent" {
		t.Errorf("the resolver was asked about %v, want the conversation's agents", res.asked)
	}
}

// TestBuildGroups_WithoutAResolverOnlyTheRootsOwnRemain pins the deployment
// that wired no resolver: no resolver means no rules, and no rules means no
// commands. Only the root's own additions survive, because they were never in
// the catalog for a rule to grant.
func TestBuildGroups_WithoutAResolverOnlyTheRootsOwnRemain(t *testing.T) {
	sr := &runtime.StaticRuntime{}
	ctx := treeContext("bke-agent")
	out := runtime.NewStructuredResponder(runtime.From(ctx))

	tree := NewBotCommandTree(ctx, sr, out)

	for _, name := range []string{"pod", "doctor", "askai", "job"} {
		if _, _, err := tree.Find([]string{name}); err == nil {
			t.Errorf("Find(%q) found a command with no resolver configured", name)
		}
	}
	if _, _, err := tree.Find([]string{"version"}); err != nil {
		t.Errorf("Find(version) = %v; the root's own additions are in no rule", err)
	}
}

// TestBuildGroups_GrantsNothingWhenTheConversationNamesNoAgent pins the surface
// without a conversation — an API caller naming none — and the first message of
// one whose agents have not been resolved yet. Neither can say which agent it
// runs as, so no rule can be about it and nothing is granted.
func TestBuildGroups_GrantsNothingWhenTheConversationNamesNoAgent(t *testing.T) {
	res := &fakeResolver{selection: granted("pod")}
	sr := &runtime.StaticRuntime{AgentCommands: res}
	ctx := treeContext()
	out := runtime.NewStructuredResponder(runtime.From(ctx))

	tree := NewBotCommandTree(ctx, sr, out)

	if _, _, err := tree.Find([]string{"askai"}); err == nil {
		t.Error("Find(askai) found a command for a request that names no agent")
	}
	if _, _, err := tree.Find([]string{"pod"}); err == nil {
		t.Error("Find(pod) found a command the request's resolver never granted")
	}
}

// TestBuildGroups_AnUngrantedCommandCannotBeRun is the assertion that matters
// on its own: not "it is absent from the menu" but "typing it does not work".
//
// The tree is what enforces the rule — an ungranted command is never built, so
// cobra rejects the name before anything else runs. ValidateGranted states the
// same rule a second time for any future tree that is not assembled this way;
// this is the half that holds today.
func TestBuildGroups_AnUngrantedCommandCannotBeRun(t *testing.T) {
	res := &fakeResolver{selection: granted("doctor")}
	sr := &runtime.StaticRuntime{AgentCommands: res}
	ctx := treeContext("bke-agent")
	out := runtime.NewStructuredResponder(runtime.From(ctx))

	tree := NewBotCommandTree(ctx, sr, out)
	tree.SetArgs([]string{"pod"})
	tree.SetOut(io.Discard)
	tree.SetErr(io.Discard)

	if err := tree.ExecuteContext(ctx); err == nil {
		t.Fatal("a command the conversation's agent was not given ran")
	}
}

// TestValidateGranted pins the redundant statement of the same rule, so that it
// keeps saying what it is for: a tree assembled some other way — once and
// reused, cached, built by a caller — would drop the filter, and this is the
// half that would still hold.
func TestValidateGranted(t *testing.T) {
	// A realistic shape: the catalog names a top-level command, and the one
	// actually executed is a subcommand of it. A rule grants "job", not
	// "job create", so the gate has to look up rather than at the leaf.
	root := &cobra.Command{Use: "/"}
	job := &cobra.Command{Use: "job"}
	create := &cobra.Command{Use: "create"}
	job.AddCommand(create)
	root.AddCommand(job)

	alpha := &cobra.Command{Use: "alpha"}
	root.AddCommand(alpha)

	for _, tc := range []struct {
		name      string
		cmd       *cobra.Command
		selection runtime.CommandSelection
		denied    bool
	}{
		{
			name:      "a granted command runs",
			cmd:       job,
			selection: granted("job"),
		},
		{
			name:      "a subcommand is covered by its parent's grant",
			cmd:       create,
			selection: granted("job"),
		},
		{
			name:      "a subcommand is refused when its parent was not granted",
			cmd:       create,
			selection: granted("pod"),
			denied:    true,
		},
		{
			name:      "a command nobody granted is refused",
			cmd:       job,
			selection: granted("pod"),
			denied:    true,
		},
		{
			// A rule grants catalogued commands, so the root's own additions —
			// alpha and version here, the MCP-only askai entry on the MCP
			// surface — are outside what this gate can decide. Leaving them open
			// is what keeps askai-mcp callable by a caller with no grants at all.
			name:      "a command outside the catalog is in no rule and stays open",
			cmd:       alpha,
			selection: granted("pod"),
		},
		{
			// The default: a caller no rule covers is granted nothing, and the
			// empty selection is what that looks like by the time it gets here.
			name:      "an empty selection admits nothing",
			cmd:       job,
			selection: runtime.CommandSelection{},
			denied:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGranted(tc.cmd, tc.selection)
			if tc.denied {
				if err == nil {
					t.Fatal("the command was allowed")
				}
				return
			}
			if err != nil {
				t.Fatalf("the command was refused: %v", err)
			}
		})
	}
}

// TestBuildGroups_AddsTheSkillCommandsTheAgentsExpose pins that a skill reaches
// the tree like any other command.
//
// A skill's name is not in the catalog — it lives inside a skill repository —
// so the only thing declared ahead of time is the group it belongs to. Building
// it here means the help card, the TUI tree and the grant gate treat it as a
// command rather than as a special case each of them has to remember.
func TestBuildGroups_AddsTheSkillCommandsTheAgentsExpose(t *testing.T) {
	res := &fakeResolver{selection: runtime.CommandSelection{
		Names: map[string]struct{}{"doctor": {}},
		Skills: []runtime.SkillCommand{
			{Name: "k8s-troubleshoot", Description: "根据现象定位 Pod 异常", Provider: "github"},
			// A skill that collides with a compiled command. The compiled one
			// has to survive: it is what every rule in this package was written
			// against, and a skill silently replacing it would change what an
			// existing name does depending on what an agent happens to expose.
			{Name: "doctor", Description: "shadowing"},
		},
	}}
	sr := &runtime.StaticRuntime{AgentCommands: res}
	ctx := runtime.Inject(context.Background(), &runtime.DynamicRuntime{
		Source:         runtime.RequestSourceLark,
		ResolvedAgents: runtime.FixedAgents([]string{"bke-agent"}),
	})
	out := runtime.NewStructuredResponder(runtime.From(ctx))

	tree := NewBotCommandTree(ctx, sr, out)

	skill, _, err := tree.Find([]string{"k8s-troubleshoot"})
	if err != nil {
		t.Fatalf("Find(k8s-troubleshoot) = %v, but the agent exposed it", err)
	}
	if skill.Hidden {
		t.Error("a skill command is hidden from the surface that can run it")
	}
	if got := skill.Annotations[chat.ChatGroup]; got != GroupSkill {
		t.Errorf("skill command group = %q, want %q", got, GroupSkill)
	}

	doctor, _, err := tree.Find([]string{"doctor"})
	if err != nil {
		t.Fatalf("Find(doctor) = %v", err)
	}
	if doctor.Short == "shadowing" {
		t.Error("a skill replaced a compiled command of the same name")
	}
}

// TestSkillCommand_RefusesWithoutARunner pins the honest failure for a skill
// command whose executor was never wired.
//
// It is refused rather than answered apart from the skill: a command that looks
// like it ran and produced a generic answer is worse than one that says it
// cannot run, because nothing tells the asker which of the two happened.
func TestSkillCommand_RefusesWithoutARunner(t *testing.T) {
	sk := runtime.SkillCommand{Name: "k8s-troubleshoot", Description: "排障"}

	cmd := newSkillCommand(&runtime.StaticRuntime{}, runtime.NewStructuredResponder(&runtime.DynamicRuntime{}), sk)

	if err := cmd.RunE(cmd, nil); err == nil {
		t.Fatal("a skill command ran with no runner configured")
	}
}

// builtIn reports whether a group list assembled by buildGroups holds a command
// by that name.
func builtIn(groups templates.CommandGroups, name string) bool {
	for _, g := range groups {
		for _, cmd := range g.Commands {
			if cmd.Name() == name {
				return true
			}
		}
	}
	return false
}

// TestBuildGroups_BuildsAccountLevelCommandsWithNoGrant pins the exception to the
// listing half: a command that acts on the caller's account is built even when
// the conversation has resolved no agents — which is exactly the state a
// first-time user is in, and the state in which the grant table is empty.
//
// The catalog it assembles is a test-local one. The reasoning above still holds
// for the mechanism, but no catalogued command carries the flag today — binding
// moved to a grant (see the comment on the catalog entry and chat.AccountLevel)
// — and a test must not keep a stale flag on a real command, nor edit the
// shipped catalog, to stay honest.
func TestBuildGroups_BuildsAccountLevelCommandsWithNoGrant(t *testing.T) {
	ctx := treeContext() // a fresh conversation: nothing resolved yet
	out := runtime.NewStructuredResponder(runtime.From(ctx))

	// Two entries differing in exactly the flag under test, so what the test
	// observes can only be the flag.
	entry := func(name string, accountLevel bool) Entry {
		return Entry{
			Name:         name,
			AccountLevel: accountLevel,
			New: func(_ *runtime.StaticRuntime, _ runtime.Responder) *cobra.Command {
				return &cobra.Command{Use: name}
			},
		}
	}
	testCatalog := []Group{{
		Key:     GroupCore,
		Message: "测试命令:",
		Label:   "测试",
		Entries: []Entry{
			entry("account-scoped", true),
			entry("agent-scoped", false),
		},
	}}

	groups := buildGroups(ctx, &runtime.StaticRuntime{}, out, testCatalog, func(string) bool { return false }, nil, nil)

	if !builtIn(groups, "account-scoped") {
		t.Error("an account-level command was left out of a conversation that was granted nothing")
	}
	// The exception must not be a hole: an entry that is not account-level is
	// still absent, for the same request.
	if builtIn(groups, "agent-scoped") {
		t.Error("a command this conversation was never granted was built")
	}
}

// TestBuildGroups_BindIsNotBuiltWithoutAGrant is the other half of taking the
// flag off /bind-onex: the command is now one of the catalogued commands a rule
// has to hand out, so a conversation whose agents were granted nothing does not
// get it — and one that was granted it does.
//
// The first half is the half that matters. It is what makes which agents offer
// binding the platform's decision (the OneX provisioner tags the agents it
// creates), and an assembled tree that still held the command would silently
// undo it: the command is dispatchable the moment it is in the tree, whatever a
// rule says.
func TestBuildGroups_BindIsNotBuiltWithoutAGrant(t *testing.T) {
	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{})

	ungranted := NewBotCommandTree(
		treeContext("bke-agent"),
		&runtime.StaticRuntime{AgentCommands: &fakeResolver{selection: granted("pod")}},
		out,
	)
	if _, _, err := ungranted.Find([]string{"bind-onex"}); err == nil {
		t.Error("bind-onex was built for a conversation whose agents were granted no such command")
	}

	// Not a tree that never builds commands: the same request grants it, and it
	// appears.
	grantedTree := NewBotCommandTree(
		treeContext("bke-agent"),
		&runtime.StaticRuntime{AgentCommands: &fakeResolver{selection: granted("bind-onex")}},
		out,
	)
	if _, _, err := grantedTree.Find([]string{"bind-onex"}); err != nil {
		t.Errorf("Find(bind-onex) = %v; a granted command has to be built", err)
	}
}

// TestValidateGranted_ExemptsAccountLevelCommands pins the enforcement half: the
// exemption is the annotation, and it survives a tree assembled some other way —
// the MCP surface builds the full catalog, and this is the gate that runs there.
//
// The command is built here rather than looked up, because no catalogued command
// carries the annotation today. It is named after one that is catalogued so the
// gate gets past its "is this a command the catalog knows" check and reaches the
// branch under test.
func TestValidateGranted_ExemptsAccountLevelCommands(t *testing.T) {
	// An empty selection is the fail-closed answer: no rule covers the
	// conversation, so a catalogued command is refused.
	empty := runtime.CommandSelection{}

	exempt := &cobra.Command{
		Use:         "pod",
		Annotations: map[string]string{chat.AccountLevel: chat.True},
	}
	if err := ValidateGranted(exempt, empty); err != nil {
		t.Errorf("ValidateGranted(exempt) = %v; an account-level command is not a capability of the agent", err)
	}

	// The same name without the annotation is refused, so the exemption is the
	// annotation and not "the selection was ignored".
	plain := &cobra.Command{Use: "pod"}
	if err := ValidateGranted(plain, empty); err == nil {
		t.Error("ValidateGranted(pod) = nil; an ungranted catalogued command must be refused")
	}
	if err := ValidateGranted(plain, granted("pod")); err != nil {
		t.Errorf("ValidateGranted(pod, granted) = %v; a granted command runs", err)
	}
}
