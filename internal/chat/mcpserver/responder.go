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
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// responder is the runtime.Responder the MCP command tree is built with.
//
// It writes into the command's own output stream rather than into a buffer of
// its own, and that indirection is the whole trick. cobrax wraps each tool call
// by creating the command tree through its factory and then calling
// SetOut/SetErr on the returned root, pointing them at the pipe it is draining
// (see cobrax's runInProcess). Resolving the writer at write time — through the
// same root the factory returned — therefore lands this command's output in
// cobrax's pipe. A responder that captured an io.Writer up front, or that
// resolved a different *cobra.Command than the factory handed back, would write
// to the real stdout: the tool would return empty text and the reply would be
// lost in the server's log.
type responder struct {
	// resolve returns the root of the command tree currently being executed.
	// It is a func rather than a field because the root does not exist yet when
	// the responder is constructed — the responder is passed to the tree
	// builder, so the two have to be created in the same breath.
	resolve func() *cobra.Command
}

// newResponder creates a responder for the tree resolver returns.
func newResponder(resolve func() *cobra.Command) *responder {
	return &responder{resolve: resolve}
}

var _ runtime.Responder = (*responder)(nil)

// out resolves the command's current output stream.
func (r *responder) out() io.Writer {
	cmd := r.resolve()
	if cmd == nil {
		// Unreachable through cobrax, which only executes a tree it just built.
		// It guards against the tree builder writing during construction, before
		// the root it is being assigned to exists: discarding is a worse reply
		// than a captured one, and much better than a nil dereference that takes
		// the whole server down.
		return io.Discard
	}
	return cmd.OutOrStdout()
}

// emit writes one piece of text, masked. Every path into this responder goes
// through here: a tool result is read by a model that may be running outside
// this host, so a secret a command echoed has to be masked on the way out and
// not merely kept off a card.
func (r *responder) emit(text string) error {
	if text == "" {
		return nil
	}
	_, err := io.WriteString(r.out(), larkcard.RedactInlineSecrets(text))
	return err
}

// Write implements io.Writer, used for the raw text a command prints and for
// the warning writer cobra's tree installs.
func (r *responder) Write(p []byte) (int, error) {
	if err := r.emit(string(p)); err != nil {
		return 0, err
	}
	// Report every byte as consumed even though masking may have shortened the
	// text: io.Writer's contract is about bytes accepted from the caller's
	// slice, and returning less would make the caller retry a write that has
	// already succeeded.
	return len(p), nil
}

func (r *responder) SendText(_ context.Context, content string) error {
	return r.emit(content)
}

// SendMarkdownCard renders a markdown card as its title and body. The MCP
// surface has no card renderer, and both halves carry information the caller
// asked for, so neither is dropped.
func (r *responder) SendMarkdownCard(_ context.Context, title, content, _ string) error {
	switch {
	case title == "":
		return r.emit(content)
	case content == "":
		return r.emit(title)
	default:
		return r.emit(title + "\n\n" + content)
	}
}

// SendCard renders a template card as its variables.
//
// There is no template on this surface. None of the tools registered here
// builds a card without also setting Raw (see SendResult), so this is a
// fallback for a command that later does; it emits the variables rather than
// the template id, because the id names a card this caller cannot see.
func (r *responder) SendCard(_ context.Context, templateID string, vars map[string]any) error {
	if len(vars) == 0 {
		return r.emit(templateID)
	}
	return r.emit(formatJSON(vars))
}

// SendInteractiveCard renders an arbitrary card payload as JSON.
func (r *responder) SendInteractiveCard(_ context.Context, cardContent any) error {
	return r.emit(formatJSON(cardContent))
}

// SendResult renders a command result for a model to read.
//
// Raw wins when it is set. The commands that populate it — job/cronjob
// list/create/update, pipeline, mark, chat — put their domain structs there
// precisely because the rendered card and the one-line summary both lose
// fields, and a model that is going to reason about the result (count it,
// re-query it, act on an id) does better with the JSON than with the prose
// written for a human.
func (r *responder) SendResult(_ context.Context, res *runtime.CommandResult) error {
	if res == nil {
		return nil
	}
	if res.Raw != nil {
		return r.emit(formatJSON(res.Raw))
	}
	if res.Markdown != nil && (res.Markdown.Title != "" || res.Markdown.Content != "") {
		return r.SendMarkdownCard(context.Background(), res.Markdown.Title, res.Markdown.Content, res.Markdown.Color)
	}
	if res.PlainText != "" {
		return r.emit(res.PlainText)
	}
	if res.LarkTemplateID != "" {
		return r.SendCard(context.Background(), res.LarkTemplateID, res.LarkVariables)
	}
	return nil
}

// formatJSON renders a value as indented JSON, falling back to Go's default
// formatting when it cannot be marshalled. A command result that happens to
// carry an unmarshalable value should still reach the caller as something
// rather than as an empty reply.
func formatJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
