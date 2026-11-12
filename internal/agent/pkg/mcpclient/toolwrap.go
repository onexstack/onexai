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

package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/pkg/contextx"
)

// wireTool adapts one einomcp tool for use by a model.
//
// It does two things the eino adapter leaves out:
//
//   - It publishes a sanitised name. einomcp hands the MCP wire name straight
//     to the model, and real MCP servers use names those APIs reject.
//   - It flattens the result. einomcp returns the whole CallToolResult as JSON
//     ({"content":[{"type":"text","text":"…"}]}), which buries the payload the
//     model actually needs one envelope deep.
//
// The call itself is delegated untouched, which is what keeps the two names in
// sync without a lookup table: einomcp sends tools/call using the wire name it
// recorded at discovery time, so a renamed tool still calls the right thing,
// and two servers exposing the same wire name cannot cross wires.
type wireTool struct {
	inner tool.InvokableTool
	info  *schema.ToolInfo
	// guard isolates this tool from a server that keeps timing out. Nil — the
	// case for a tool built outside a Manager — disables the check. See
	// breaker.go for why only per-call timeouts count.
	guard *guard
}

var _ tool.InvokableTool = (*wireTool)(nil)

// newWireTool wraps inner, exposing it to the model as llmName.
func newWireTool(ctx context.Context, inner tool.InvokableTool, llmName string) (*wireTool, error) {
	info, err := inner.Info(ctx)
	if err != nil {
		return nil, err
	}
	// Copy: the name is ours to change, the rest of the descriptor (and the
	// parameter schema, which is shared and read-only) stays as einomcp built it.
	renamed := *info
	renamed.Name = llmName
	return &wireTool{inner: inner, info: &renamed}, nil
}

// Info reports the sanitised descriptor. The returned value is shared and must
// be treated as read-only; the model layer may read it concurrently.
func (t *wireTool) Info(context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

// InvokableRun executes the tool. Errors are returned as-is and converted to a
// model-visible string by the error handler the manager installs; see
// Manager.discover for why that conversion is mandatory.
func (t *wireTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if t.guard != nil && !t.guard.allow() {
		// Refused before dialing: the point is to stop spending a run's budget
		// on a server that has repeatedly spent it without answering. The text
		// is model-visible, so it says what the model can do instead.
		return "", fmt.Errorf(
			"tool %q is temporarily unavailable: its MCP server stopped responding. "+
				"Use another tool, or try again in about a minute", t.info.Name)
	}

	out, err := t.inner.InvokableRun(ctx, argumentsInJSON, opts...)
	if t.guard != nil {
		t.record(err, ctx)
	}
	if err != nil {
		return "", err
	}
	return flattenCallToolResult(out), nil
}

// record updates the tool's timeout streak. The three outcomes are told apart
// deliberately:
//
//   - a call that returned — with or without an error — means the server
//     answered, so the streak ends;
//   - a call this tool's own timeout cut short is the failure the breaker
//     exists for;
//   - the run's deadline, or the caller cancelling, says nothing about the
//     server and is left out of both the count and the reset.
func (t *wireTool) record(err error, ctx context.Context) {
	switch {
	case err == nil:
		t.guard.succeeded()
	case isToolTimeout(ctx, err):
		if t.guard.timedOut() {
			// Logged once per opening, not once per call: the calls after this
			// one are refused without dialing, and the model is told why.
			slog.WarnContext(ctx, "mcp tool breaker opened after consecutive timeouts",
				"tool", t.info.Name, "timeouts", breakerFailures, "cooldown", breakerCooldown)
		}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		// Not the server's fault. Leave the streak as it was.
	default:
		t.guard.succeeded()
	}
}

// isToolTimeout reports whether err is the per-tool deadline firing, as opposed
// to the run's own. The marker is installed by the middleware that adds the
// deadline (see contextx.WithToolTimeout); without it the two are
// indistinguishable.
func isToolTimeout(ctx context.Context, err error) bool {
	if !contextx.IsToolTimeout(ctx) {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// callToolEnvelope is the part of mcp.CallToolResult that einomcp's adapter
// serialises into a tool's string result.
type callToolEnvelope struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// flattenCallToolResult turns einomcp's JSON envelope back into plain text.
// Anything that is not the envelope — a raw string, an empty result, a result
// made only of non-text blocks such as images — is returned unchanged, so no
// output is ever lost by the rewrite.
func flattenCallToolResult(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	var env callToolEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return raw
	}
	var b strings.Builder
	for _, c := range env.Content {
		if c.Type != "text" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(c.Text)
	}
	if b.Len() == 0 {
		return raw
	}
	return b.String()
}
