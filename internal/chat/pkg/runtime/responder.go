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

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
)

// MarkdownContent holds a Markdown message with optional title and color.
type MarkdownContent struct {
	Title   string `json:"title,omitempty"`
	Content string `json:"content,omitempty"`
	Color   string `json:"color,omitempty"` // "green" | "red" | "orange"
}

// CommandResult carries a command's output in multiple representations.
// Rendering priority (highest → lowest): LarkTemplateID > Interactive >
// Markdown > PlainText.
type CommandResult struct {
	Raw            any
	LarkTemplateID string
	LarkVariables  map[string]any
	// Interactive, when set, is a card a Lark surface sends in preference to
	// Markdown. It exists for answers that are not readable as one block of
	// text — a /pod match that lists twenty pods is a picker, not a wall — and
	// it is ignored by surfaces without a card renderer, which fall back to
	// Raw/Markdown/PlainText. The json tag keeps it out of REST serialization.
	Interactive *larkcard.Card `json:"-"`
	Markdown    *MarkdownContent
	PlainText   string
	// Err is why the command did not run to completion, empty when it did.
	//
	// It is carried on the result rather than reported through the Responder
	// because the responder is what produced the output: a command that failed
	// before writing anything leaves a responder with nothing to say, and the
	// caller would read that silence as success. It is set by the caller that
	// runs the tree, not by a command.
	Err string
}

// Responder abstracts how a command delivers its output to the caller.
// Commands call Responder methods; the implementation routes output to
// Lark, REST JSON, or MCP text.
type Responder interface {
	io.Writer
	SendText(ctx context.Context, content string) error
	SendCard(ctx context.Context, templateID string, vars map[string]any) error
	SendMarkdownCard(ctx context.Context, title, content, color string) error
	SendInteractiveCard(ctx context.Context, cardContent any) error
	SendResult(ctx context.Context, res *CommandResult) error
}

// ResultProvider is implemented by responders that capture output into a
// retrievable CommandResult (the structured REST/MCP responder). The Lark
// responder streams output instead and does not implement it, keeping the
// Responder interface free of a method that has no meaning for it.
type ResultProvider interface {
	Result() *CommandResult
}

// ──────────────────────────────────────────────────────────────────────────────
// Lark Responder — sends output to Lark via the injected LarkServices.
// ──────────────────────────────────────────────────────────────────────────────

// larkResponder implements Responder by routing output to Lark services. Plain
// text written through Write is buffered and sent as a single message on Flush.
type larkResponder struct {
	dr *DynamicRuntime

	mu  sync.Mutex
	out bytes.Buffer
}

// NewLarkResponder creates a Responder routed to Lark services.
func NewLarkResponder(dr *DynamicRuntime) Responder {
	return &larkResponder{dr: dr}
}

// Write buffers data in memory without sending any network request.
func (o *larkResponder) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.out.Write(p)
}

// Flush sends the buffered content to Lark, split across as many messages as
// the per-message budget requires.
//
// The splitting is the point. The platform rejects an over-long text message
// outright, and this used to hand the whole buffer to one send — so a long
// reply failed as a unit, and since both callers discarded the error, the user
// was told nothing at all. Pieces go out in order, and the first failure stops
// the rest and comes back naming how many parts did go: a reply with a hole in
// the middle is worse than one that stopped and said so.
//
// The buffer is drained before the first send, not after the last. A caller
// that flushes twice therefore sends once, which is the property worth having
// when the first attempt failed part-way.
func (o *larkResponder) Flush(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.out.Len() == 0 {
		return nil
	}
	content := o.out.String()
	o.out.Reset()

	chunks := larkcard.SplitByBudget(content, larkcard.TextChunkBytes)
	for sent, chunk := range chunks {
		if err := o.dr.LarkServices.SendText(ctx, o.dr.Receiver, chunk); err != nil {
			return fmt.Errorf("sent %d of %d parts before failing: %w", sent, len(chunks), err)
		}
	}
	return nil
}

func (o *larkResponder) SendText(ctx context.Context, content string) error {
	return o.SendResult(ctx, &CommandResult{PlainText: content})
}

func (o *larkResponder) SendCard(ctx context.Context, templateID string, variables map[string]any) error {
	return o.SendResult(ctx, &CommandResult{LarkTemplateID: templateID, LarkVariables: variables})
}

func (o *larkResponder) SendMarkdownCard(ctx context.Context, title, content, color string) error {
	return o.SendResult(ctx, &CommandResult{Markdown: &MarkdownContent{Title: title, Content: content, Color: color}})
}

func (o *larkResponder) SendInteractiveCard(ctx context.Context, cardContent any) error {
	return o.dr.LarkServices.SendInteractiveCard(ctx, o.dr.Receiver, cardContent)
}

func (o *larkResponder) SendResult(ctx context.Context, res *CommandResult) error {
	if res == nil {
		return nil
	}
	if res.LarkTemplateID != "" {
		if o.dr.IsCardAction && o.dr.MessageID() != "" {
			return o.dr.LarkServices.PatchCard(ctx, o.dr.MessageID(), res.LarkTemplateID, res.LarkVariables)
		}
		return o.dr.LarkServices.SendCard(ctx, o.dr.Receiver, res.LarkTemplateID, res.LarkVariables)
	}
	if res.Interactive != nil {
		// Rendered here rather than by the command so the session key is
		// injected into every button and select value, which is what lets a
		// click find the conversation the card was sent to.
		return o.dr.LarkServices.SendInteractiveCard(ctx, o.dr.Receiver,
			larkcard.RenderCardMap(res.Interactive, o.dr.Address()))
	}
	if res.Markdown != nil && res.Markdown.Content != "" {
		color := res.Markdown.Color
		if color == "" {
			color = "blue"
		}
		return o.dr.LarkServices.SendMarkdownCard(ctx, o.dr.Receiver, res.Markdown.Title, res.Markdown.Content, color)
	}
	if res.PlainText != "" {
		_, err := o.Write([]byte(res.PlainText))
		return err
	}
	if res.Raw != nil {
		b, _ := json.MarshalIndent(res.Raw, "", "  ")
		_, err := o.Write([]byte(fmt.Sprintf("```json\n%s\n```", string(b))))
		return err
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Structured Responder — REST and MCP: captures output into a CommandResult.
// ──────────────────────────────────────────────────────────────────────────────

type structuredResponder struct {
	dr     *DynamicRuntime
	buf    strings.Builder
	result *CommandResult
}

// NewStructuredResponder creates a responder for REST (Source=API) or MCP
// (Source=MCP). The returned responder captures all output; call Result() to
// retrieve the CommandResult after execution.
func NewStructuredResponder(dr *DynamicRuntime) *structuredResponder {
	return &structuredResponder{dr: dr}
}

func (r *structuredResponder) Write(p []byte) (int, error) {
	return r.buf.Write(p)
}

func (r *structuredResponder) SendText(_ context.Context, content string) error {
	r.buf.WriteString(content)
	return nil
}

func (r *structuredResponder) SendCard(_ context.Context, templateID string, vars map[string]any) error {
	if r.result == nil {
		r.result = &CommandResult{}
	}
	r.result.LarkTemplateID = templateID
	r.result.LarkVariables = vars
	return nil
}

func (r *structuredResponder) SendMarkdownCard(_ context.Context, title, content, color string) error {
	if r.result == nil {
		r.result = &CommandResult{}
	}
	r.result.Markdown = &MarkdownContent{Title: title, Content: content, Color: color}
	return nil
}

func (r *structuredResponder) SendInteractiveCard(_ context.Context, cardContent any) error {
	if r.result == nil {
		r.result = &CommandResult{}
	}
	r.result.Raw = cardContent
	return nil
}

func (r *structuredResponder) SendResult(_ context.Context, res *CommandResult) error {
	r.result = res
	return nil
}

func (r *structuredResponder) Result() *CommandResult {
	if r.result == nil {
		r.result = &CommandResult{}
	}
	if r.result.PlainText == "" {
		r.result.PlainText = r.buf.String()
	}
	return r.result
}
