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

// Package chat defines well-known constants used by the oai-chat-apiserver,
// including cobra command annotation keys and their values.
package chat

// Annotation keys used on cobra command Annotations maps. Using typed constants
// avoids misspelling in command definitions and makes grepping for usages
// straightforward.
const (
	// ChatEnabled marks a command as available in the chat command namespace.
	ChatEnabled = "x-chat-enabled"

	// ChatGroup is the display group shown in /help and command listings.
	ChatGroup = "x-chat-group"

	// SREOnly marks a command as restricted to SRE users: the chat.setting row
	// onexai-sre-user-id, or is_sre=true in a JWT.
	SREOnly = "x-sre-only"

	// AdminOnly marks a command as restricted to admins: the chat.setting row
	// onexai-admin-user-id, plus everyone on the SRE list. It is the gate for
	// commands that act on the host the bot runs on rather than on the chat.
	AdminOnly = "x-admin-only"

	// ChatOnly marks a command that only makes sense in a group (ticket) chat.
	ChatOnly = "x-chat-only"

	// P2POnly marks a command that only makes sense in a direct (peer-to-peer)
	// chat. It is the one annotation whose value is not a plain boolean: True
	// asks for the generic refusal (runtime.ErrP2POnlyCommand), while any other
	// value is sent to the caller as the refusal itself. A command does that when
	// the right next step depends on what it does — /bind-onex says the code has
	// to be replaced, because the one just spoken in a group is one the room can
	// claim. See command.ValidateCommands (which enforces) and
	// command.HideCommands (which hides it from a conversation that cannot run
	// it).
	P2POnly = "x-p2p-only"

	// TUIOnly marks a command that is only visible when the source is TUI.
	TUIOnly = "x-tui-only"

	// LarkOnly marks a command that is only visible when the source is Lark.
	LarkOnly = "x-lark-only"

	// MCPTool marks a command that should be exposed as an MCP tool.
	MCPTool = "x-mcp-tool"

	// CustomCommand marks a command built from a user-defined CustomCommand and
	// carries that command's canonical name. The MCP bridge uses it to tell a
	// tool that was actually registered from a custom command from a built-in
	// tool whose name a dropped custom command happened to claim.
	CustomCommand = "x-custom-command"

	// AccountLevel marks a command that acts on the caller's own account rather
	// than in the conversation, and is therefore not subject to the agent command
	// grant table.
	//
	// # Why the grant table is the wrong axis for these
	//
	// A grant answers "may the agent this conversation runs use this command".
	// An account-level command has no such subject: it changes something about
	// the person typing it, and its own authority is "the thing you are acting on
	// is yours" — binding a Feishu account, for instance, only records that this
	// speaker spoke a code, and the code is completed later by whoever owns it.
	// Gating it on the conversation's agents is a category error, and it fails in
	// a way that is invisible until someone tries it: a fresh private chat has no
	// resolved agents yet, so its grant set is empty (the resolver is
	// fail-closed) and a command that every user must be able to run is not even
	// in the tree.
	//
	// So these commands are always built and always executable, alongside the
	// root's own /help and /version. Role and chat-type annotations (AdminOnly,
	// P2POnly, LarkOnly) still apply — being account-level says nothing about
	// who may run it or where.
	//
	// # No catalogued command carries it today
	//
	// /bind-onex was the one user, and it moved to a grant on 2026-10-14: the
	// platform decides which agents offer binding (it tags the agents it
	// provisions), and the two audience defaults are named in the same seed. See
	// hack/onex-bind-command-seed.sql and the comment on the catalog entry. The
	// mechanism stays because the reasoning above is still true of a command that
	// genuinely must survive an empty grant table — it is the escape hatch for
	// that case, not the shape binding turned out to need.
	AccountLevel = "x-account-level"
)

// Annotation values.
const (
	// True is the value used to enable a boolean-typed annotation.
	True = "true"
)

// Chat group values for the ChatGroup annotation.
//
// A value is the key of the group the command is catalogued in
// (internal/chat/pkg/command/catalog.go), and the command catalog owns that
// fact: while assembling a tree it overwrites this annotation with the key of
// the group the command is listed in, because the two had drifted apart — /pod
// was listed under the resource group while annotated Job. These constants are
// what the command constructors set for themselves so that a command built
// outside the catalog still describes itself; the catalog decides.
//
// The keys cannot be imported from the catalog: the catalog imports this
// package, so the values are repeated here and a catalog test asserts they
// still agree. A value is also what a card button carries ("nav:group:<key>"),
// so it must not change once sent.
const (
	// ChatGroupAI is the display group for AI-related commands.
	ChatGroupAI = "ai"
	// ChatGroupCore is the display group for core (help/version) commands.
	ChatGroupCore = "core"
	// ChatGroupCronJob is the display group for cronjob commands. The cronjob
	// commands are catalogued in the job group, so the catalog replaces this
	// with the job key on any command it places; the constant is what the
	// command's own subcommands carry.
	ChatGroupCronJob = "cronjob"
	// ChatGroupDuty is the display group for duty commands.
	ChatGroupDuty = "duty"
	// ChatGroupJob is the display group for job commands.
	ChatGroupJob = "job"
	// ChatGroupMessaging is the display group for messaging commands.
	ChatGroupMessaging = "msg"
	// ChatGroupPlatform is the display group for third-party platform commands.
	ChatGroupPlatform = "platform"
	// ChatGroupResource is the display group for infrastructure query commands.
	ChatGroupResource = "resource"
	// ChatGroupSkill is the display group for the commands built at request
	// time from the skills an agent exposed as commands.
	ChatGroupSkill = "skill"
	// ChatGroupBuiltinAgent is the display group for built-in agent commands.
	ChatGroupBuiltinAgent = "builtin-agent"
)

// Legacy command annotation keys (deprecated). These predate the "x-" prefixed
// keys above and are kept for reference; new code should use the ChatEnabled /
// SREOnly / MCPTool keys instead.
const (
	SREOnlyCommandAnnotation  = "onexstack.io/onexai/sre-only-command"
	ChatOnlyCommandAnnotation = "onexstack.io/onexai/chat-only-command"
	P2POnlyCommandAnnotation  = "onexstack.io/onexai/p2p-only-command"
	MCPToolAnnotation         = "onexstack.io/onexai/mcp-enabled"
)
