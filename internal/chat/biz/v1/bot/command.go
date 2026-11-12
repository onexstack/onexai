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

package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/onexstack/onexai/internal/chat/pkg/command"
	"github.com/onexstack/onexai/internal/chat/pkg/metrics"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// rawArgCommands lists commands whose arguments must be preserved as a single
// raw string (including spaces and newlines) rather than shell-tokenised.
var rawArgCommands = map[string]bool{
	"send_msg": true,
}

// ExecuteAPICommand executes a command from a REST caller and returns the result.
//
// The caller's roles come from the token's is_sre claim, which the JWT middleware
// verified. A REST caller has no platform user id to match against the
// configured Lark role lists, so the claim is the only role signal available;
// admins are defined as a superset of SREs, so an operator token is an admin
// token. A command that must not be reachable this way is marked x-lark-only,
// which is the annotation for choosing a surface.
func (b *botBiz) ExecuteAPICommand(ctx context.Context, convID, cmdText, userID string, isSRE bool) *runtime.CommandResult {
	dr := &runtime.DynamicRuntime{
		Source:  runtime.RequestSourceAPI,
		ConvID:  convID,
		IsSRE:   isSRE,
		IsAdmin: isSRE,
		// A REST caller is a Sender with an identity and no platform handle: the
		// handle exists for the resources a Lark person binds, and a token binds
		// none.
		Sender:   v1.NewSender(userID, ""),
		Receiver: &v1.Receiver{Type: "api"},
		// A REST caller names the conversation it is acting for, so it gets
		// the same command set that conversation would see in Lark: the rule is
		// written against the agents that conversation resolves to. It resolves
		// for **this** sender, and a REST caller carries no platform handle, so a
		// `@peer` declaration resolves to nothing here — a REST caller has no Lark
		// account for a binding to name. An empty conversation names no agent
		// either, so no rule can be written against it and it is granted nothing.
		// `/v1/cli/:cmdname` passes an empty conversation on purpose and is
		// therefore refused every command; a caller that wants to run one says
		// which conversation it acts for.
		SessionM:       b.sessionFor(ctx, convID),
		ResolvedAgents: b.resolvedAgentsOnce(),
	}
	ctx = runtime.Inject(ctx, dr)
	out := runtime.NewStructuredResponder(dr)
	err := b.executeCommand(ctx, cmdText, out)

	result := out.Result()
	if err != nil {
		// A command that was refused and one that ran and said nothing are
		// both empty results, and a caller has no other way to tell them
		// apart. Refusal is the ordinary case for this: the tree does not hold
		// the commands this conversation's agents were not given, so asking
		// for one is an unknown command.
		result.Err = err.Error()
	}
	return result
}

func (b *botBiz) executeCommand(ctx context.Context, cmdText string, out runtime.Responder) error {
	// Use the same shell-quoting parser the Lark path uses. strings.Fields would
	// split `--user "Alice Wang"` into three tokens and make any argument with a
	// space impossible to pass over REST or MCP.
	args, err := shellParse(strings.TrimPrefix(strings.TrimSpace(cmdText), "/"))
	if err != nil {
		return runtime.NewUserError("argument parse error: %v", err)
	}
	if len(args) == 0 {
		return nil
	}

	tree := command.NewBotCommandTree(ctx, b.sr, out)
	tree.SetArgs(args)
	tree.SetOut(out)
	tree.SetErr(out)
	tree.SetContext(ctx)

	// A responder that streams its output (the Lark one) finishes on Flush; the
	// structured responder built here for REST and MCP calls captures instead
	// and has no Flush at all, so this branch is currently unreachable. It is
	// kept and made to report rather than deleted, because routing a Lark
	// responder through here later would otherwise reintroduce the silent
	// discard: a send that failed looks exactly like one never made.
	defer func() {
		flusher, ok := out.(interface{ Flush(context.Context) error })
		if !ok {
			return
		}
		if err := flusher.Flush(ctx); err != nil {
			metrics.M.RecordOutboundFailure(ctx, "api")
			slog.ErrorContext(ctx, "command output was not delivered", "cmd", cmdText, "error", err)
		}
	}()

	if err := tree.ExecuteContext(ctx); err != nil {
		slog.WarnContext(ctx, "Chat command execution failed", "cmd", cmdText, "error", err)
		return err
	}
	return nil
}

// CommandTree builds the chat command tree and serializes it to a nestable
// structure the TUI can use for completion. Hidden commands are omitted.
//
// It is the catalog rather than a menu: the TUI fetches this once at startup and
// carries no conversation, so there is no agent whose grants could narrow it —
// and narrowing an answer that has no question would empty the completion list.
// What a TUI request may run is decided where it runs, on the conversation's
// WebSocket, which does carry the conversation.
func (b *botBiz) CommandTree(ctx context.Context) []*v1.CommandNode {
	dr := &runtime.DynamicRuntime{
		Source:   runtime.RequestSourceTUI,
		Receiver: &v1.Receiver{Type: "tui_client"},
	}
	ctx = runtime.Inject(ctx, dr)
	out := runtime.NewStructuredResponder(dr)

	root := command.NewCommandCatalogTree(ctx, b.sr, out)

	nodes := make([]*v1.CommandNode, 0, len(root.Commands()))
	for _, c := range root.Commands() {
		if c.Hidden {
			continue
		}
		nodes = append(nodes, serializeCmd(c))
	}
	return nodes
}

// serializeCmd recursively converts a cobra command into a CommandNode.
func serializeCmd(cmd *cobra.Command) *v1.CommandNode {
	n := &v1.CommandNode{
		Name:      cmd.Name(),
		Short:     cmd.Short,
		Group:     cmd.Annotations["group"],
		Dangerous: cmd.Annotations["x-dangerous"] == "true",
	}
	cmd.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		n.Flags = append(n.Flags, &v1.CommandFlag{
			Name:      f.Name,
			Shorthand: f.Shorthand,
			Usage:     f.Usage,
		})
	})
	for _, sub := range cmd.Commands() {
		if sub.Hidden {
			continue
		}
		n.Subcommands = append(n.Subcommands, serializeCmd(sub))
	}
	return n
}

// parseMessageToArgs parses a command string into arguments.
func (b *botBiz) parseMessageToArgs(messageText string) ([]string, error) {
	firstSpace := strings.IndexAny(messageText, " \t\n")
	if firstSpace != -1 {
		cmdName := messageText[:firstSpace]
		if rawArgCommands[cmdName] {
			rest := strings.TrimSpace(messageText[firstSpace+1:])
			if rest == "" {
				return []string{cmdName}, nil
			}
			return []string{cmdName, rest}, nil
		}
	}

	args, err := shellParse(messageText)
	if err != nil {
		return nil, runtime.NewUserError("argument parse error: %v", err)
	}
	return args, nil
}

// shellParse splits a string into tokens using shell-quoting rules.
func shellParse(input string) ([]string, error) {
	if input == "" {
		return []string{}, nil
	}

	var args []string
	var current strings.Builder
	var inQuotes bool
	var quoteChar rune

	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case !inQuotes && (r == '"' || r == '\''):
			inQuotes = true
			quoteChar = r
		case inQuotes && r == quoteChar:
			inQuotes = false
			quoteChar = 0
		case inQuotes && r == '\\' && i+1 < len(runes):
			i++
			next := runes[i]
			switch next {
			case 'n':
				current.WriteRune('\n')
			case 't':
				current.WriteRune('\t')
			case 'r':
				current.WriteRune('\r')
			case '\\', '"', '\'':
				current.WriteRune(next)
			default:
				current.WriteRune(next)
			}
		case !inQuotes && (r == ' ' || r == '\t' || r == '\n'):
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if inQuotes {
		return nil, fmt.Errorf("unclosed quote: %c", quoteChar)
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args, nil
}
