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
	"testing"

	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/roleset"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// TestRequiresAdmin pins the gate in front of every command that acts on the
// host the bot runs on rather than on the chat. Two things are at stake: an SRE
// is not automatically an admin (the roles gate different commands, and folding
// them together would make one annotation stand for the other), and the caller
// is told which role they are missing — "permission denied" without the role is
// a report nobody can act on.
func TestRequiresAdmin(t *testing.T) {
	cmd := &cobra.Command{
		Use:         "new",
		Annotations: map[string]string{chat.AdminOnly: chat.True},
	}

	for _, tc := range []struct {
		name   string
		dr     *runtime.DynamicRuntime
		denied bool
	}{
		{name: "an admin may run it", dr: &runtime.DynamicRuntime{IsAdmin: true}},
		{name: "an SRE who is not an admin may not", dr: &runtime.DynamicRuntime{IsSRE: true}, denied: true},
		{name: "a user with no role may not", dr: &runtime.DynamicRuntime{}, denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCommands(cmd, tc.dr)
			if !tc.denied {
				if err != nil {
					t.Fatalf("ValidateCommands = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ValidateCommands allowed a command that requires the admin role")
			}
			if !strings.Contains(err.Error(), "admin") {
				t.Errorf("error = %v, want it to name the missing admin role", err)
			}
		})
	}
}

// TestRequiresSRERole pins that the existing role still works, and still says
// which role it is: the two annotations now share one code path, and a command
// that lost its own role name would deny admins a command they are allowed.
func TestRequiresSRERole(t *testing.T) {
	cmd := &cobra.Command{
		Use:         "job create",
		Annotations: map[string]string{chat.SREOnly: chat.True},
	}

	if err := ValidateCommands(cmd, &runtime.DynamicRuntime{IsSRE: true}); err != nil {
		t.Errorf("an SRE was denied an SRE-only command: %v", err)
	}
	if err := ValidateCommands(cmd, &runtime.DynamicRuntime{IsAdmin: true}); err == nil {
		t.Error("an admin who is not an SRE was allowed an SRE-only command")
	} else if !strings.Contains(err.Error(), "SRE") {
		t.Errorf("error = %v, want it to name the SRE role", err)
	}
}

// TestAdminAndSREBothAnnotated pins the order the two checks run in. A command
// carrying both annotations is admin-only: checking the weaker role first would
// let every SRE through it.
func TestAdminAndSREBothAnnotated(t *testing.T) {
	cmd := &cobra.Command{
		Use: "new",
		Annotations: map[string]string{
			chat.SREOnly:   chat.True,
			chat.AdminOnly: chat.True,
		},
	}

	if err := ValidateCommands(cmd, &runtime.DynamicRuntime{IsAdmin: true, IsSRE: true}); err != nil {
		t.Errorf("an admin was denied: %v", err)
	}
	// IsSRE without IsAdmin is the case that matters: it is what a caller whose
	// roles came from different sources looks like.
	if err := ValidateCommands(cmd, &runtime.DynamicRuntime{IsSRE: true}); err == nil {
		t.Error("an SRE who is not an admin was allowed a command that requires both")
	}
}

// TestNonSREIsDeniedAnSREOnlyCommand pins the restored role gate end to end for
// a caller whose role comes from the configured SRE list (via StaticRuntime),
// which is how a Lark sender and a REST token are both resolved. A caller the
// list does not name must be refused the x-sre-only command, not let through:
// the gate was once hardcoded open, with the token's role ignored, and this is
// the regression test for that decision. The listed SRE in the same test proves
// the gate still admits the people it is meant to.
func TestNonSREIsDeniedAnSREOnlyCommand(t *testing.T) {
	sr := &runtime.StaticRuntime{
		Roles: roleset.NewStatic(map[string]struct{}{"ou_sre": {}}, nil),
	}
	cmd := &cobra.Command{
		Use:         "job create",
		Annotations: map[string]string{chat.SREOnly: chat.True},
	}

	stranger := &runtime.DynamicRuntime{IsSRE: sr.IsSREUser("ou_stranger")}
	if err := ValidateCommands(cmd, stranger); err == nil {
		t.Fatal("a non-SRE caller was allowed an x-sre-only command")
	}

	sre := &runtime.DynamicRuntime{IsSRE: sr.IsSREUser("ou_sre")}
	if err := ValidateCommands(cmd, sre); err != nil {
		t.Fatalf("a listed SRE was denied an x-sre-only command: %v", err)
	}
}

// TestUnannotatedCommandIsAllowed pins that the default is unchanged: the roles
// gate the commands that change something, and everything else keeps running for
// everyone.
func TestUnannotatedCommandIsAllowed(t *testing.T) {
	plain := &cobra.Command{Use: "help"}
	annotated := &cobra.Command{
		Use:         "help",
		Annotations: map[string]string{chat.ChatGroup: chat.ChatGroupCore},
	}

	for _, cmd := range []*cobra.Command{plain, annotated} {
		if err := ValidateCommands(cmd, &runtime.DynamicRuntime{}); err != nil {
			t.Errorf("%s: ValidateCommands = %v, want nil", cmd.Use, err)
		}
	}
}

// groupSpeaker and directSpeaker are the two conversations a command can arrive
// from, as the runtime tells them apart: a chat id is a group, an open_id is a
// direct message. Everything else — a REST call, the TUI, an MCP tool call —
// carries neither, and is treated as "not a direct message" because a command
// that needs one person's ear has nothing to say to it.
func groupSpeaker() *runtime.DynamicRuntime {
	return &runtime.DynamicRuntime{
		Source:   runtime.RequestSourceLark,
		Sender:   v1.NewSender("", "ou_group_speaker"),
		Receiver: &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: "oc_group"},
	}
}

func directSpeaker() *runtime.DynamicRuntime {
	return &runtime.DynamicRuntime{
		Source:   runtime.RequestSourceLark,
		Sender:   v1.NewSender("", "ou_direct_speaker"),
		Receiver: &v1.Receiver{Type: v1.ReceiverTypeOpenID, Id: "ou_direct_speaker"},
	}
}

// TestP2POnlyIsRefusedOutsideADirectMessage pins the x-p2p-only gate. It is the
// one annotation that decides who may run a command by *where* they are asking
// from, and the failure it prevents is not a bad answer: /bind-onex writes a
// claim
// naming whoever spoke the code, so a group can claim a code it can read.
func TestP2POnlyIsRefusedOutsideADirectMessage(t *testing.T) {
	generic := &cobra.Command{
		Use:         "bind-onex <code>",
		Annotations: map[string]string{chat.P2POnly: chat.True},
	}
	withOwnWords := &cobra.Command{
		Use:         "bind-onex <code>",
		Annotations: map[string]string{chat.P2POnly: "这串码请当作已经泄露，回控制台重新获取一个。"},
	}

	if err := ValidateCommands(generic, directSpeaker()); err != nil {
		t.Errorf("a direct message was refused: %v", err)
	}
	if err := ValidateCommands(withOwnWords, directSpeaker()); err != nil {
		t.Errorf("a direct message was refused: %v", err)
	}

	err := ValidateCommands(generic, groupSpeaker())
	if err == nil {
		t.Fatal("a group was allowed an x-p2p-only command")
	}
	// A UserError is the difference between the caller reading the sentence and
	// the caller reading it behind a trace id (see handler.errorReplyMessage).
	if !runtime.IsUserError(err) {
		t.Errorf("err = %v, want a UserError", err)
	}
	// Named, because "command is not available here" without the name leaves a
	// user with several commands open unable to tell which one was refused.
	if !strings.Contains(err.Error(), "bind") {
		t.Errorf("err = %v, want it to name the command", err)
	}

	// The value itself is the refusal when it is not True — that is how a
	// command explains a next step the generic sentence would get wrong.
	err = ValidateCommands(withOwnWords, groupSpeaker())
	if err == nil {
		t.Fatal("a group was allowed an x-p2p-only command")
	}
	if got := err.Error(); got != withOwnWords.Annotations[chat.P2POnly] {
		t.Errorf("err = %q, want the command's own refusal", got)
	}

	// And a refusal carrying a percent sign is a message, not a format string.
	pct := &cobra.Command{Use: "bind-onex", Annotations: map[string]string{chat.P2POnly: "100% 私聊"}}
	if err := ValidateCommands(pct, groupSpeaker()); err == nil || err.Error() != "100% 私聊" {
		t.Errorf("err = %v, want the text verbatim", err)
	}
}

// TestP2POnlyIsCheckedAfterTheRoleGate pins the order of the two gates. A caller
// who fails both should be told about the role: it is the stronger statement
// (they may never run this command anywhere), and answering with "wrong kind of
// chat" would send them looking for a private chat that changes nothing.
func TestP2POnlyIsCheckedAfterTheRoleGate(t *testing.T) {
	cmd := &cobra.Command{
		Use: "new",
		Annotations: map[string]string{
			chat.AdminOnly: chat.True,
			chat.P2POnly:   chat.True,
		},
	}

	err := ValidateCommands(cmd, groupSpeaker())
	if err == nil {
		t.Fatal("a non-admin in a group was allowed an admin-only, x-p2p-only command")
	}
	if !strings.Contains(err.Error(), "admin") {
		t.Errorf("err = %v, want the role gate to answer first", err)
	}
}

// TestP2POnlyIsHiddenOutsideADirectMessage pins that the gate's twin in
// HideCommands reads the same annotation. Hiding is not the enforcement — the
// test above is — but it is what keeps /help in a group from inviting somebody
// to paste a binding code into the room.
func TestP2POnlyIsHiddenOutsideADirectMessage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dr      *runtime.DynamicRuntime
		hidden  bool
		because string
	}{
		{name: "a group", dr: groupSpeaker(), hidden: true, because: "the room can read what is typed there"},
		{name: "a REST call", dr: &runtime.DynamicRuntime{Source: runtime.RequestSourceAPI}, hidden: true, because: "there is no private chat on an API surface"},
		{name: "a direct message", dr: directSpeaker(), hidden: false, because: "it is the one place the command runs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{
				Use:         "bind-onex",
				Annotations: map[string]string{chat.P2POnly: "该命令只能在机器人私聊中使用。"},
			}
			// HideCommands walks the children of the root it is given, so the
			// command has to be attached to one.
			root := &cobra.Command{Use: "/"}
			root.AddCommand(cmd)

			HideCommands(root, tc.dr)

			if cmd.Hidden != tc.hidden {
				t.Errorf("hidden = %v, want %v — %s", cmd.Hidden, tc.hidden, tc.because)
			}
		})
	}
}

// TestCommandTreeRegistersBindingCommands builds the real tree and looks the
// admin-only commands up in it. Constructing the tree is what catches the failure
// a per-command test cannot: a name that collides with an existing command makes
// cobra panic at registration time, and a command added to the slice but never
// re-exported would be missing from /help without anything failing.
func TestCommandTreeRegistersBindingCommands(t *testing.T) {
	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{})
	tree := NewCommandCatalogTree(context.Background(), &runtime.StaticRuntime{}, out)

	for _, name := range []string{"new", "switch", "dir", "sessions", "doctor"} {
		cmd, _, err := tree.Find([]string{name})
		if err != nil {
			t.Errorf("Find(%q) = %v", name, err)
			continue
		}
		if cmd.Name() != name {
			t.Errorf("Find(%q) resolved to %q", name, cmd.Name())
		}
		if cmd.Annotations[chat.AdminOnly] != chat.True {
			t.Errorf("%s is reachable without the admin role", name)
		}
	}
}

// TestSwitchIsHiddenButStillBuilt pins both halves of the 2026-10-07 decision.
//
// `switch` names two things: this command swaps the conversation's *backend*
// (llm/cli), while the card's "切换 Agent" (switch Agent) button carries the action string
// `act:/switch <name>` and swaps its *agent*. The card action never reaches the
// command tree — cardnav.go matches it first — so the collision is of wording,
// not behaviour; the answer was to drop the command from the menus rather than
// rename it, because the action strings are already sitting in cards in chat.
//
// Hidden and unreachable are different things, and the second half is the one
// that matters: a conversation parked on the cli backend needs a way back that
// does not forget its history, and /new --backend cannot provide it. So the
// command must stay in the tree and stay granted.
func TestSwitchIsHiddenButStillBuilt(t *testing.T) {
	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{})
	tree := NewCommandCatalogTree(context.Background(), &runtime.StaticRuntime{}, out)

	cmd, _, err := tree.Find([]string{"switch"})
	if err != nil {
		t.Fatalf("Find(switch) = %v; hiding it must not remove it from the tree", err)
	}
	if !cmd.Hidden {
		t.Error("/switch is back in the menus; see the comment on NewSwitchCmd")
	}

	// Built is what makes it dispatchable. A command skipped at assembly would
	// answer "unknown command" no matter what Hidden says.
	built := false
	for _, g := range buildGroups(context.Background(), &runtime.StaticRuntime{}, out, Groups(), catalogGrants, nil, nil) {
		for _, c := range g.Commands {
			if c.Name() == "switch" {
				built = true
			}
		}
	}
	if !built {
		t.Error("switch is hidden and no longer built, so it is unreachable rather than quiet")
	}
}

// TestCommandTreeRegistersPlatformCommands looks the platform commands up in the
// real tree, which is the failure a per-command test cannot see: a command that
// is never added to a group — the state every platform command but this one is
// still in — builds and passes its own tests while being unreachable from every
// surface.
func TestCommandTreeRegistersPlatformCommands(t *testing.T) {
	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{})
	tree := NewCommandCatalogTree(context.Background(), &runtime.StaticRuntime{}, out)

	cmd, _, err := tree.Find([]string{"pipeline"})
	if err != nil {
		t.Fatalf("Find(pipeline) = %v", err)
	}
	if cmd.Name() != "pipeline" {
		t.Fatalf("Find(pipeline) resolved to %q", cmd.Name())
	}
	if cmd.Annotations[chat.ChatGroup] != chat.ChatGroupPlatform {
		t.Errorf("pipeline group = %q, want %q", cmd.Annotations[chat.ChatGroup], chat.ChatGroupPlatform)
	}
	if cmd.Annotations[chat.SREOnly] != chat.True {
		// It triggers a release; the role gate is what keeps it out of the hands
		// of everyone else.
		t.Error("pipeline is reachable without the SRE role")
	}
	if cmd.Hidden {
		t.Error("pipeline is hidden, so the MCP bridge would not expose it")
	}
}

// TestCommandTreeRegistersPodCommand pins that /pod is reachable from the real
// tree. It was commented out while its data source was unavailable, and a
// command that is never re-added builds and passes its own tests while being
// invisible from every surface.
func TestCommandTreeRegistersPodCommand(t *testing.T) {
	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{})
	tree := NewCommandCatalogTree(context.Background(), &runtime.StaticRuntime{}, out)

	cmd, _, err := tree.Find([]string{"pod"})
	if err != nil {
		t.Fatalf("Find(pod) = %v", err)
	}
	if cmd.Name() != "pod" {
		t.Fatalf("Find(pod) resolved to %q", cmd.Name())
	}
	if cmd.Annotations[chat.ChatGroup] != chat.ChatGroupResource {
		t.Errorf("pod group = %q, want %q", cmd.Annotations[chat.ChatGroup], chat.ChatGroupResource)
	}
	if cmd.Annotations[chat.MCPTool] != chat.True {
		t.Error("pod is not exposed as an MCP tool")
	}
	if cmd.Hidden {
		t.Error("pod is hidden, so the MCP bridge would not expose it")
	}
}

// TestCommandTreeRegistersBindCommand pins that /bind-onex is reachable from
// the real
// tree, which is the failure a per-command test cannot see: a command that is
// never added to a group builds and passes its own tests while being invisible
// from every surface. It is the other half of the binding handshake — without
// it the console tells users to send a command the bot does not know, and the
// message falls through to the model, which is the bug this command fixes.
func TestCommandTreeRegistersBindCommand(t *testing.T) {
	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{})
	tree := NewCommandCatalogTree(context.Background(), &runtime.StaticRuntime{}, out)

	cmd, _, err := tree.Find([]string{"bind-onex"})
	if err != nil {
		t.Fatalf("Find(bind-onex) = %v", err)
	}
	if cmd.Name() != "bind-onex" {
		t.Fatalf("Find(bind-onex) resolved to %q", cmd.Name())
	}
	if cmd.Annotations[chat.ChatGroup] != chat.ChatGroupCore {
		t.Errorf("bind group = %q, want %q", cmd.Annotations[chat.ChatGroup], chat.ChatGroupCore)
	}
	if cmd.Annotations[chat.AdminOnly] == chat.True || cmd.Annotations[chat.SREOnly] == chat.True {
		t.Error("bind is role-gated, but every user has to be able to bind their own account")
	}
	if cmd.Annotations[chat.MCPTool] == chat.True {
		t.Error("bind is exposed as an MCP tool, but a tool call carries no chat identity")
	}
	// It is granted like any other command now, and not by the exemption the
	// flag buys: this tree holds every command, so a flag left on the entry
	// would be invisible here and would only show up as the command surviving a
	// conversation that was granted nothing (see TestBuildGroups_BindIsNotBuiltWithoutAGrant).
	if cmd.Annotations[chat.AccountLevel] == chat.True {
		t.Error("bind-onex is account-level again, so no grant governs which agents offer it")
	}

	// A claim names a chat identity, so the command is hidden from the surfaces
	// that have none.
	HideCommands(tree, &runtime.DynamicRuntime{Source: runtime.RequestSourceAPI})
	if !cmd.Hidden {
		t.Error("bind stays visible to a non-Lark source")
	}
}

// TestBindIsVisibleOnTheLarkSurface is the other half of the test above, and it
// caught a real misunderstanding: NewBotCommandTree hides LarkOnly commands
// itself, so /bind-onex does not appear in /v1/commands/tree or /help — both of
// which
// run as the TUI or an API caller. That is correct (neither surface has a chat
// identity to claim), but it also means "the command is missing from the tree I
// can query" is not evidence that it is missing.
func TestBindIsVisibleOnTheLarkSurface(t *testing.T) {
	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{})
	lark := &runtime.DynamicRuntime{
		Source:   runtime.RequestSourceLark,
		Sender:   v1.NewSender("", "ou_someone"),
		Receiver: &v1.Receiver{Type: v1.ReceiverTypeOpenID, Id: "ou_someone"},
	}
	tree := NewCommandCatalogTree(runtime.Inject(context.Background(), lark), &runtime.StaticRuntime{}, out)

	cmd, _, err := tree.Find([]string{"bind-onex"})
	if err != nil {
		t.Fatalf("Find(bind-onex) = %v", err)
	}
	if cmd.Hidden {
		t.Error("bind is hidden from the one surface that can run it")
	}
	for _, name := range []string{"bind-onex", "doctor"} {
		c, _, err := tree.Find([]string{name})
		if err != nil || c.Hidden {
			t.Errorf("%s: Find=%v hidden=%v, want visible", name, err, c != nil && c.Hidden)
		}
	}
}

// TestBindRefusedInAGroupChat closes the loop the two tests above open: the
// annotation /bind-onex carries, the gate in the root's PersistentPreRunE that
// reads
// it, and the runtime a group message really produces. A unit test on
// ValidateCommands proves the gate works on a command it is handed; this proves
// /bind-onex is that command on the path a real message takes, and that the
// refusal
// survives the whole way out to the caller.
//
// The wording is asserted, not just the refusal. A group caller has already lost
// something — the code they typed is in the room — and the one thing the answer
// must not do is send them off to repeat it privately, which is how the reading
// is handed to whoever saw it.
func TestBindRefusedInAGroupChat(t *testing.T) {
	ctx := runtime.Inject(context.Background(), groupSpeaker())
	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{})
	tree := NewCommandCatalogTree(ctx, &runtime.StaticRuntime{}, out)
	tree.SetArgs([]string{"bind-onex", "CODECODE"})

	err := tree.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("/bind-onex ran in a group chat")
	}
	if !runtime.IsUserError(err) {
		t.Fatalf("err = %v, want a UserError, which is what reaches the caller verbatim", err)
	}
	for _, want := range []string{"私聊", "重新获取"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to mention %q", err, want)
		}
	}
}
