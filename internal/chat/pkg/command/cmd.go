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

// Package command assembles the server-side chat command tree.
package command

import (
	"context"
	"flag"
	"fmt"
	"log/slog"

	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"k8s.io/client-go/rest"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/kubectl/pkg/cmd/plugin"
	"k8s.io/kubectl/pkg/util/i18n"
	"k8s.io/kubectl/pkg/util/term"

	chat "github.com/onexstack/onexai/pkg/known/chat"

	"github.com/onexstack/onexai/internal/chat/pkg/metrics"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// NewBotCommandTree builds the chat command tree for a single invocation,
// holding the commands this request's conversation was granted and no others.
// A new instance must be created per request so that flag state does not bleed
// between calls.
func NewBotCommandTree(ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	selection := selectCommands(ctx, sr)
	return newBotCommandTree(ctx, sr, out, selection.Allows, fixedSelection(selection), selection.Skills, selection.Customs)
}

// NewCommandCatalogTree builds the same tree with every catalogued command in
// it: the catalog, rather than anybody's menu.
//
// It exists for the surfaces that have no conversation to ask about — the MCP
// bridge, which registers one tool per command and narrows the list it shows per
// caller afterwards, and the TUI tree, which is a completion list fetched
// without a conversation. Neither can name an agent, and a grant list is per
// agent, so filtering here would not narrow their answer; it would delete it.
// What a caller may actually run is decided by the gates those surfaces keep:
// the MCP tool filter and its per-call ValidateGranted, and the command
// execution path a TUI request travels (the conversation's WebSocket).
//
// Two things a reader is likely to try, and must not:
//
//   - Do not spell "every command" as a CommandSelection holding
//     CommandNames(). The catalog's name set is a shared package-level map that
//     callers must not modify, and a selection is a thing callers write to. A
//     predicate says "all of them" without aliasing anything.
//   - Do not hand this tree a different *runtime.StaticRuntime than the
//     filtered one. ValidateGranted reads the runtime the constructor was given
//     rather than the tree, so a wrapper that reported a wider grant list would
//     disable the very gate it looks like it is leaving intact.
func NewCommandCatalogTree(ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	return newBotCommandTree(ctx, sr, out, catalogGrants, selectionFromRuntime(sr), nil, nil)
}

// NewMCPCommandTree builds the tree the MCP bridge publishes as tools.
//
// It is the whole catalog plus the custom commands this deployment defines;
// which of them a given caller sees is narrowed per request by the bridge's
// tool filter (catalog grants for built-ins, the agent binding for customs).
// The per-request tree would be wrong here: cobrax derives tool schemas once,
// from the tree it is handed, and a tree built without a caller has no grants.
func NewMCPCommandTree(
	ctx context.Context,
	sr *runtime.StaticRuntime,
	out runtime.Responder,
	customs []runtime.CustomCommand,
) *cobra.Command {
	// Mark the build context as the MCP surface. HideCommands reads it to decide
	// whether a Lark-only command is usable here: a prompt custom command is
	// Lark-only because it starts a turn, and over MCP it renders the prompt
	// text instead, so dropping it would remove a tool that works.
	ctx = runtime.Inject(ctx, &runtime.DynamicRuntime{Source: runtime.RequestSourceMCP})
	return newBotCommandTree(ctx, sr, out, catalogGrants, selectionFromRuntime(sr), nil, customs)
}

// catalogGrants admits every catalogued command. It is a function rather than a
// boolean so that a catalogue build and a granted build differ in exactly one
// argument, and it is not a selection holding every name for the reason
// NewCommandCatalogTree gives.
func catalogGrants(string) bool { return true }

// fixedSelection answers the grant gate with the selection the tree was built
// from.
//
// The chat surfaces build the tree out of that selection, so asking the gate to
// resolve it again would answer the same question twice for the same request —
// and on the path a person is waiting on, the second answer costs a rule match
// per covering rule plus a customs union. It is the first answer or nothing.
func fixedSelection(sel runtime.CommandSelection) func(context.Context) runtime.CommandSelection {
	return func(context.Context) runtime.CommandSelection { return sel }
}

// selectionFromRuntime answers the grant gate by resolving the request's
// agents, which is what the catalogue trees need: they hold commands the caller
// may not have been granted, and the runtime — not the tree — is what says
// which. See ValidateGranted for why that distinction is load-bearing.
func selectionFromRuntime(sr *runtime.StaticRuntime) func(context.Context) runtime.CommandSelection {
	return func(ctx context.Context) runtime.CommandSelection { return selectCommands(ctx, sr) }
}

// newBotCommandTree assembles the tree. grants decides which catalogued command
// is built at all; skills are the skill commands to build beside them.
func newBotCommandTree(
	ctx context.Context,
	sr *runtime.StaticRuntime,
	out runtime.Responder,
	grants func(string) bool,
	gate func(context.Context) runtime.CommandSelection,
	skills []runtime.SkillCommand,
	customs []runtime.CustomCommand,
) *cobra.Command {
	warningHandler := rest.NewWarningWriter(
		out,
		rest.WarningWriterOptions{
			Deduplicate: true,
			Color:       term.AllowsColorOutput(out),
		},
	)

	warningsAsErrors := false

	cmds := &cobra.Command{
		Use:   "/",
		Short: i18n.T("OneX 是 AI 原生实战营提供的机器人，旨在通过 ChatOps 的方式提高问题处理效率和效果。"),
		Long:  templates.LongDesc(`OneX 是 AI 原生实战营提供的机器人，旨在通过 ChatOps 的方式提高问题处理效率和效果。`),
		// Error/usage output is handled uniformly by the caller (SendText /
		// WriteResponse / MCP tool error return). Without SilenceErrors, cobra
		// also prints "Error: ..." and "Run '/ --help' for usage." into the
		// responder's buffer, so a failed command (e.g. an unknown subcommand)
		// gets reported a second time when the buffer is flushed.
		SilenceErrors: true,
		SilenceUsage:  true,
		Run:           runHelp,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			metrics.M.RecordLarkCommand(cmd.Context(), cmd.Name())

			// The gates run before anything else, including --mock: a mock run
			// replaces what a command does, and it must not replace whether
			// anybody was allowed to ask for it. It matters more here than it
			// used to — the MCP bridge registers its tools from the catalogue
			// tree, so on that surface this is the only thing standing between
			// a tool call and a caller the rules never granted anything.
			if err := ValidateCommands(cmd, runtime.From(cmd.Context())); err != nil {
				return err
			}
			// The role gate above says who may run a command. This one says
			// whether this conversation's agent was given it at all, and it
			// is a gate rather than a filter for the same reason: a command
			// that is merely missing from a menu is still one keystroke away.
			if err := ValidateGranted(cmd, gate(cmd.Context())); err != nil {
				return err
			}

			// --mock is not in any MCP tool schema and is hidden from every
			// help, which is what keeps the switch below out of a model's
			// reach. It stays a switch rather than a removed flag because it is
			// how a command's card and its failure path are exercised without
			// touching a cluster.
			mockVal, err := cmd.Flags().GetString("mock")
			if err != nil {
				mockVal = chat.MockNone
			}
			switch mockVal {
			case chat.MockNone:
				// Nothing to substitute; the command runs as itself.

			case chat.MockFailed:
				return fmt.Errorf("mocked failure request")

			case chat.MockSuccess:
				if cmd.RunE != nil {
					cmd.RunE = func(*cobra.Command, []string) error { return nil }
				}
				if cmd.Run != nil {
					cmd.Run = func(*cobra.Command, []string) {}
				}

			default:
				return fmt.Errorf("invalid mock value '%s': must be empty, 'success', or 'failed'", mockVal)
			}

			slog.InfoContext(cmd.Context(), "Executing command", "command", cmd.Name())
			rest.SetDefaultWarningHandler(warningHandler)

			if cmd.Name() == cobra.ShellCompRequestCmd {
				plugin.SetupPluginCompletion(cmd, args)
			}

			return nil
		},
		PersistentPostRunE: func(*cobra.Command, []string) error {
			if warningsAsErrors {
				count := warningHandler.WarningCount()
				switch count {
				case 0:
				case 1:
					return fmt.Errorf("%d warning received", count)
				default:
					return fmt.Errorf("%d warnings received", count)
				}
			}
			return nil
		},
	}

	cmds.SetGlobalNormalizationFunc(cliflag.WarnWordSepNormalizeFunc)
	flags := cmds.PersistentFlags()
	//flags.BoolVar(&warningsAsErrors, "warnings-as-errors", warningsAsErrors,
	//"Treat warnings received from the server as errors and exit with a non-zero exit code")
	flags.String("mock", "", "Mock execution result for any subcommand: '', 'success', or 'failed'")
	flags.MarkHidden("mock")
	flags.SetNormalizeFunc(cliflag.WordSepNormalizeFunc)
	_ = viper.BindPFlags(cmds.PersistentFlags())
	cmds.PersistentFlags().AddGoFlagSet(flag.CommandLine)

	groups := buildGroups(ctx, sr, out, Groups(), grants, skills, customs)
	groups.Add(cmds)

	filters := []string{"options", "completion"}
	alpha := NewCmdAlpha(out)
	if !alpha.HasSubCommands() {
		filters = append(filters, alpha.Name())
	}
	templates.ActsAsRootCommand(cmds, filters, groups...)
	cmds.AddCommand(alpha)
	cmds.AddCommand(NewCmdVersion(out))
	cmds.AddCommand(NewCmdOptions(out))

	cmds.SetGlobalNormalizationFunc(cliflag.WordSepNormalizeFunc)

	HideCommands(cmds, runtime.From(ctx))
	return cmds
}

// buildGroups assembles the command groups for one invocation from the
// catalog.
//
// It is the single place a command's group is decided. Each command also
// carries a ChatGroup annotation of its own, which is the same fact stated a
// second time, and the two had already drifted apart — /pod is listed under the
// resource group while its annotation said Job, so the text help and the card
// named different groups for it. Overwriting the annotation here from the group
// the command is listed in is what makes the catalog authoritative rather than
// advisory.
//
// Only the commands this conversation's agents were given are built: constructing
// one allocates an options struct, a flag set and a cobra tree, and this runs for
// every message. What "given" means is the caller's grants function, which is
// how a catalogue tree is the same assembler with a different answer to that one
// question. The catalog itself is a parameter for the same reason — it is the
// other half of "what gets assembled", and taking it from the caller is what
// lets a test assemble a catalog of its own (see the account-level tests in
// grant_test.go) rather than depend on the shipped one holding a command that
// behaves a particular way.
//
// A group with nothing left in it is skipped rather than added empty: an empty
// CommandGroup renders as a heading with nothing under it.
func buildGroups(
	ctx context.Context,
	sr *runtime.StaticRuntime,
	out runtime.Responder,
	catalog []Group,
	grants func(string) bool,
	skills []runtime.SkillCommand,
	customs []runtime.CustomCommand,
) templates.CommandGroups {
	// A copy, because CommandNames returns the shared name set and this loop
	// reserves names in it as it builds.
	taken := make(map[string]struct{}, len(CommandNames()))
	for name := range CommandNames() {
		taken[name] = struct{}{}
	}

	var groups templates.CommandGroups
	for _, g := range catalog {
		cmds := make([]*cobra.Command, 0, len(g.Entries))
		for _, e := range g.Entries {
			// An account-level command is not a capability of the conversation's
			// agent, so grants cannot speak for it: it is always built. The flag
			// is on the entry rather than read off the built command so that a
			// command nobody was granted is not constructed on every message.
			if !grants(e.Name) && !e.AccountLevel {
				continue
			}
			cmd := e.New(sr, out)
			if e.AccountLevel {
				// Stated on the command as well as the entry, because
				// ValidateGranted reads the runtime's command, not the catalog.
				if cmd.Annotations == nil {
					cmd.Annotations = make(map[string]string, 1)
				}
				cmd.Annotations[chat.AccountLevel] = chat.True
			}
			cmds = append(cmds, cmd)
		}
		if g.Key == GroupSkill {
			// The skill group has no compiled entries: its commands are the
			// ones the conversation's agents exposed. They are built here so
			// that everything downstream — the help card, the TUI tree, the
			// grant gate — treats them like any other command.
			built := skillCommands(skills, sr, out)
			reserveNames(taken, built)
			cmds = append(cmds, built...)
		}
		if g.Key == GroupCustom {
			// Custom commands come last, after skills: a name already taken by
			// a built-in or a skill is dropped rather than shadowing it.
			cmds = append(cmds, customCommands(customs, taken, sr, out)...)
		}
		if len(cmds) == 0 {
			continue
		}

		// The group is stamped on after the group is complete, rather than by
		// each command as it is built, so that "a command listed here carries
		// this key" holds for every way a command can arrive — including the
		// ones that were not known until this request.
		for _, cmd := range cmds {
			if cmd.Annotations == nil {
				cmd.Annotations = make(map[string]string, 1)
			}
			cmd.Annotations[chat.ChatGroup] = g.Key
		}
		groups = append(groups, templates.CommandGroup{Message: g.Message, Commands: cmds})
	}
	return groups
}

// selectCommands resolves the commands available to the request's conversation.
//
// Every way of not having an answer yields the empty selection, because a grant
// comes from a rule about an agent and none of these can produce one: a
// deployment with no resolver wired has no rules, and a request that cannot name
// an agent has nothing for a rule to be about. The default is deny, so "I cannot
// tell" and "nobody granted anything" are the same answer.
//
// A resolution that failed lands in that same answer. It is not the same fact —
// the agents exist, we could not read them — but the only alternative is a menu
// that renders the error, and a menu that cannot name an agent has nothing to
// show either way. It is logged rather than silently swallowed so the two can be
// told apart afterwards.
func selectCommands(ctx context.Context, sr *runtime.StaticRuntime) runtime.CommandSelection {
	if sr == nil || sr.AgentCommands == nil {
		return runtime.CommandSelection{}
	}

	dr := runtime.From(ctx)
	if dr.ResolvedAgents == nil {
		return runtime.CommandSelection{}
	}
	res, err := dr.ResolvedAgents(ctx, dr)
	if err != nil {
		slog.WarnContext(ctx, "could not resolve the agents this request runs as; granting no commands", "error", err)
		return runtime.CommandSelection{}
	}
	return sr.AgentCommands.Resolve(ctx, res.Agents)
}

// ValidateGranted checks that this conversation's agent was given the command
// being run.
//
// # Where it is the only gate, and where it is the second one
//
// On the chat surfaces — Lark, and a REST call that names its conversation —
// this is the second statement of a rule the tree already enforced: buildGroups
// never builds the commands a rule left out, so cobra answers "unknown command"
// before any of this runs.
//
// On the MCP surface it is the only one that runs. The bridge registers its
// tools from NewCommandCatalogTree, which holds every command, and narrows only
// the list it shows a caller — so the tree a tool call executes against does
// contain commands the caller was not granted, and this is what refuses them.
// That is why the bridge hands the builder its own runtime rather than a
// wrapper: this gate reads the runtime, not the tree.
//
// It stays on the chat surfaces for the same reason it exists there at all:
// the two are independent statements of one rule, and the enforcement that
// matters is the one that survives a change to the other. A tree built once and
// reused, a cached tree, a caller that assembles its own — each of those would
// quietly drop the filter, and this would still hold.
//
// Only catalogued commands are checked: the root's own additions (alpha,
// version, options, the MCP-only askai entry) are in no rule, so nothing has
// said which agents may run them. On the MCP surface that leaves askai-mcp
// callable with no grant at all — it is the one tool that survives an empty
// tool list, and it is deliberately not closed here.
//
// It returns a plain error shaped like the role gate's, since the two sit side
// by side and a reader would rightly wonder at one of them being special.
func ValidateGranted(cmd *cobra.Command, selection runtime.CommandSelection) error {
	top := topLevelCommand(cmd)
	if _, ok := CommandNames()[top.Name()]; !ok {
		// A user-defined command is not in the compiled name set, so the
		// catalog branch below cannot speak for it. Its grant is the agent
		// binding the selection was built from; the group annotation is what
		// tells the two kinds of uncatalogued command apart.
		if top.Annotations[chat.ChatGroup] == GroupCustom {
			if selection.AllowsCustom(top.Name()) {
				return nil
			}
			return &notGrantedError{cmd: top.Use}
		}
		return nil
	}
	// An account-level command is not a capability of the conversation's agent:
	// it acts on the caller, and its authority is that the thing it acts on is
	// the caller's own. No grant can speak for it, so it is always allowed —
	// the same standing the uncatalogued root commands above have. Role and
	// chat-type gates still apply; they run beside this one.
	if top.Annotations[chat.AccountLevel] == chat.True {
		return nil
	}
	if selection.Allows(top.Name()) {
		return nil
	}
	return &notGrantedError{cmd: top.Use}
}

// topLevelCommand returns the command the catalog names for the one being run.
//
// It is the top-level ancestor rather than the command itself because a rule
// grants "job", not "job create": gating on the leaf would leave every
// subcommand ungated, which is exactly the half a restriction has to cover.
func topLevelCommand(cmd *cobra.Command) *cobra.Command {
	root := cmd.Root()
	for cur := cmd; cur != nil; cur = cur.Parent() {
		if cur == root || cur.Parent() == root {
			return cur
		}
	}
	return cmd
}

// notGrantedError reports that a command is outside what the conversation's
// agent was given.
type notGrantedError struct {
	cmd string
}

func (e *notGrantedError) Error() string {
	return "permission denied: command " + e.cmd + " is not enabled for this conversation's agent"
}

// HideCommands hides commands that are not applicable for the given request
// source or conversation. Lark-only commands are hidden for REST/MCP/TUI, etc.
//
// Hiding is not enforcement: a hidden command is still one keystroke away (see
// ValidateCommands for the gates that actually refuse). What it buys is that a
// command a conversation cannot run is not offered in that conversation's /help
// and card menu — which for x-p2p-only matters, because the invitation to run
// /bind-onex is what leads a user to paste a binding code into a room.
func HideCommands(root *cobra.Command, dr *runtime.DynamicRuntime) {
	for _, cmd := range root.Commands() {
		annotations := cmd.Annotations
		// Commands annotated with x-tui-only are hidden for non-TUI sources.
		if annotations[chat.TUIOnly] == chat.True && !dr.IsTUI() {
			cmd.Hidden = true
		}
		// Commands annotated with x-lark-only are hidden for sources that cannot
		// draw a turn. MCP is the exception: a prompt custom command is
		// Lark-only because it starts a turn, and the MCP bridge renders its
		// prompt text instead of starting one.
		if annotations[chat.LarkOnly] == chat.True && !dr.IsLark() && !dr.IsMCP() {
			cmd.Hidden = true
		}
		// Commands annotated with x-p2p-only are hidden outside a direct
		// conversation. Any value counts as annotated here, because the value may
		// be the refusal itself rather than True; ValidateCommands is where the
		// two forms are told apart.
		if annotations[chat.P2POnly] != "" && !dr.IsP2P() {
			cmd.Hidden = true
		}
	}
}

// ValidateCommands checks whether the caller may run the given command in the
// conversation it came from. Returns an error if access is denied.
//
// Only commands that carry a role or chat-type annotation are restricted. An
// unannotated command is one whose safety does not depend on who is asking or
// where they are asking from, which is the case for everything that reads or
// reports; the roles exist for the commands that change something, and the chat
// type for the ones whose answer is only safe in one kind of conversation.
//
// It runs from the root's PersistentPreRunE, so it is the one place that covers
// every surface a command can arrive on: a Lark message, a card button, the bot
// menu, a REST call, the TUI and an MCP tool call all come through here.
func ValidateCommands(cmd *cobra.Command, dr *runtime.DynamicRuntime) error {
	annotations := cmd.Annotations
	// Admin is checked first because it is the stronger role: a command that
	// somehow carried both annotations must not be let through by the weaker one.
	if annotations[chat.AdminOnly] == chat.True && !dr.IsAdmin {
		return &permissionError{cmd: cmd.Use, role: "admin"}
	}
	if annotations[chat.SREOnly] == chat.True && !dr.IsSRE {
		return &permissionError{cmd: cmd.Use, role: "SRE"}
	}
	// The chat-type gate runs last, and it answers a different question: the
	// roles above say whether this caller may ever run the command, this says
	// whether this conversation is one the command can answer in. A caller who
	// fails both should hear about the role first.
	if v := annotations[chat.P2POnly]; v != "" && !dr.IsP2P() {
		// The value is either True, meaning "use the generic refusal", or the
		// refusal itself — see the constant. Wrapped in "%s" so that a message
		// carrying a percent sign is not read as a format string.
		message := v
		if v == chat.True {
			message = fmt.Sprintf(runtime.ErrP2POnlyCommand, cmd.Name())
		}
		// A UserError, unlike the role gate above, because this refusal is the
		// caller's own to act on: handler.errorReplyMessage sends a UserError
		// verbatim and prefixes anything else with a trace id.
		return runtime.NewUserError("%s", message)
	}
	return nil
}

type permissionError struct {
	cmd  string
	role string
}

func (e *permissionError) Error() string {
	return "permission denied: command " + e.cmd + " requires " + e.role + " role"
}

func runHelp(cmd *cobra.Command, args []string) {
	_ = cmd.Help()
}
